package webui

// Snooze for a ringing alarm (discussion #1415).
//
// Snooze is a key action (internal/keyactions "snooze"), bound in the app's
// remote key map like the sleep timer, to a thumbs key or to AUX. Whether an
// alarm can be snoozed at all is decided by that binding; how long it stays
// quiet is the alarm's own Snooze field (5, 9, 10 or 15 minutes, 9 when the
// alarm predates the field).
//
// RINGING, precisely. An alarm is ringing from the moment its (re)fire's
// recall returned until the first of these ends it:
//
//   - the user stops or pauses playback in the app or on the phone page
//     (NoteExplicitStop, a REQUEST, never a stop inferred from box state);
//   - the speaker goes to standby: power key, standby, the alarm's own
//     switch-off (the source change to STANDBY, and the #197 standby stamp);
//   - something else is played through STR (the recall generation moved on);
//   - the speaker plays another input (Bluetooth, AUX, AirPlay) at the press;
//   - its switch-off time passes (or, for an alarm without one, two hours);
//   - another alarm fires and takes over, or the recall could not play.
//
// Between the fire and the recall returning, the alarm is STARTING, not yet
// ringing: the firmware's own power-on resume of yesterday's input passes
// through then, and a press is ignored rather than snoozing a recall that is
// still on its way.
//
// A snoozed alarm still counts as ringing for the end conditions above, and
// each of them cancels the pending snooze, with one allowance: a drop to
// standby in the first seconds after the snooze's own stop is that stop
// settling on firmware that powers STR's source off when its transport stops
// (alarmSnoozeSettle), not the power key, and the re-fire wakes the speaker. A press while ringing stops
// playback (the speaker stays on and silent), shows SNOOZE and the wake time
// on a speaker with STR's panel, and arms ONE deadline: no polling loop, no
// standing timer. When it runs out, the deadline re-checks the end conditions
// and the speaker's live state once, and then rings again through driveAlarm,
// the same path as a normal fire: same preset, same volume, same 45 s retry.
// Snoozes are unlimited.
//
// AUX is special: STR only sees that key as the source change to AUX, so the
// firmware's own reaction (switch to the AUX input) has always run already.
// While an alarm rings that is the silence snooze wants, so no STOP is sent on
// top. Outside a ringing alarm the press does nothing extra: AUX stays AUX.
// With nothing plugged into AUX, the firmware skips the empty input and lands
// on BLUETOOTH instead (SoundTouch 10, 27.0.6). A switch to BLUETOOTH
// therefore counts as the AUX press too, but only with a key frame within
// alarmAuxKeyWindow of it: a phone connecting by itself brings none. Where the
// key trace names AUX_INPUT, that counts as well (AlarmAuxKeyPress).
//
// The alarm's switch-off counts from the latest (re)fire: a snooze cancels
// the sleep timer the alarm armed (only that one, never a timer the user set
// since) and the re-fire arms it afresh. Otherwise a 15-minute switch-off
// with a 15-minute snooze would switch the speaker off mid-snooze.
//
// Everything here is RAM only. A press writes nothing to NAND, and an agent
// restart mid-snooze loses the snooze (the snooze log line says so). Groups
// are not touched: an alarm wakes only its own speaker, and a snooze acts only
// on that speaker.

import (
	"context"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/alarm"
	"github.com/JRpersonal/streborn/internal/keyactions"
	"github.com/JRpersonal/streborn/internal/oled"
)

var (
	// alarmSnoozeUnit is one snooze minute. A var so the tests can shrink it.
	alarmSnoozeUnit = time.Minute
	// alarmRingMax is how long an alarm without a switch-off counts as
	// ringing. Past that, the snooze key no longer reaches back to it: an
	// alarm somebody has been listening to all morning is just music.
	alarmRingMax = 2 * time.Hour
	// snoozeMinGap drops a repeat of the same press (the key trace already
	// folds one press into one event; this only guards a stray repeat).
	snoozeMinGap = time.Second
	// alarmLiveTimeout bounds the one now_playing read a press or a re-fire
	// makes.
	alarmLiveTimeout = 5 * time.Second
	// alarmSnoozeSettle is how long after a snooze's own stop a drop to
	// STANDBY is put down to that stop rather than to the power key. Some
	// firmware drops STR's UPnP source to STANDBY when its transport stops
	// (the resume_standby.go discriminator exists for exactly that), and a
	// snooze cancelled by its own stop would never ring again.
	alarmSnoozeSettle = 10 * time.Second
	// alarmAuxKeyWindow is how close a key frame has to be to a switch to
	// BLUETOOTH for the switch to count as the AUX key (either side: the
	// frame may arrive before or after the source change).
	alarmAuxKeyWindow = 2 * time.Second
	// alarmAuxKeyPoll is how often that window is checked for the frame.
	alarmAuxKeyPoll = 50 * time.Millisecond
	// alarmAuxRepeat: a second AUX signal this soon after an AUX snooze (the
	// key trace and the source change of one press) is the same press.
	alarmAuxRepeat = 4 * time.Second
)

// Seams for the tests, in the sleep timer's style: the real implementations
// reach the firmware on fixed ports.
var (
	// alarmLiveState reads what the speaker is on right now.
	alarmLiveState = func(s *Server, ctx context.Context) (source string, playing bool) {
		if s.boxHost == "" {
			return "", false
		}
		np := fetchNowPlaying(ctx, s.boxHost)
		return np.Source, np.PlayStatus == "PLAY_STATE" || np.PlayStatus == "BUFFERING_STATE"
	}
	// alarmSilence stops the alarm's playback for a snooze.
	alarmSilence = func(s *Server, ctx context.Context) { s.silenceTransport(ctx) }
	// alarmKeyActivity is when the speaker last reported a key press (the bare
	// userActivityUpdate frame or the key trace, whichever is newer).
	alarmKeyActivity = func(s *Server) time.Time {
		if s.userActivityFn == nil {
			return time.Time{}
		}
		return s.userActivityFn()
	}
	// showSnoozeNotice puts SNOOZE and the wake time on this speaker's panel.
	// It shows even with the notices switched off, like the sleep key: it is
	// the only answer to a key the user just pressed.
	showSnoozeNotice = func(until string) {
		if !oled.Supported() {
			return
		}
		n := oled.Snooze(until)
		n.Bypass = true
		oled.Show(n)
	}
)

// alarmRing is the alarm that is ringing (or snoozed) on this speaker.
type alarmRing struct {
	mu sync.Mutex
	// active: an alarm fired and nothing has ended it yet.
	active bool
	// armed: the current (re)fire's recall has returned, so the alarm is
	// ringing rather than starting.
	armed bool
	alarm alarm.Alarm
	due   time.Time
	// since is when the current (re)fire or snooze started; the end
	// conditions only count what happened after it.
	since time.Time
	// gen is the recall generation the alarm's own play (or the snooze) left
	// behind; a different one means somebody played something else.
	gen uint64
	// sleepGen is the sleep-timer generation of the switch-off this ring
	// armed, 0 when none.
	sleepGen uint64
	// snoozeUntil is when a snoozed alarm rings again, zero when not snoozed.
	snoozeUntil time.Time
	// snoozedViaAux: the snooze came from AUX, so the speaker sitting on AUX
	// (or on BLUETOOTH, where the AUX key lands when AUX is empty) at the
	// deadline is the snooze itself, not another input.
	snoozedViaAux bool
	// standbyAfter: a drop to standby before this instant is the snooze's own
	// stop settling, not the power key (alarmSnoozeSettle). Zero outside a
	// snooze.
	standbyAfter time.Time
	timer        *time.Timer
	// seq moves on with every (re)fire, snooze and end, so a deadline, a
	// verify or a press that raced one of them can tell it lost.
	seq       uint64
	snoozes   int
	lastPress time.Time
}

// ringFacts is what ringEndReason decides on, gathered under the locks.
type ringFacts struct {
	since        time.Time
	ringGen      uint64
	curGen       uint64
	explicitStop time.Time
	standbyStop  time.Time
	// standbySince, when later than since, is where standby stamps start to
	// count (a snooze's own stop settling, see alarmSnoozeSettle).
	standbySince time.Time
	// maxRing bounds how long the alarm counts as ringing; 0 skips the check
	// (a snooze deadline, which the user asked for).
	maxRing time.Duration
	now     time.Time
}

// ringEndReason reports why an armed alarm is no longer ringing, "" when it
// still is. Pure, so the policy is table-testable.
func ringEndReason(f ringFacts) string {
	standbyRef := f.since
	if f.standbySince.After(standbyRef) {
		standbyRef = f.standbySince
	}
	switch {
	case f.curGen != f.ringGen:
		return "something else was played since"
	case !f.standbyStop.IsZero() && f.standbyStop.After(standbyRef):
		return "the speaker was switched off"
	case !f.explicitStop.IsZero() && f.explicitStop.After(f.since):
		return "playback was stopped in the app or on the phone"
	case f.maxRing > 0 && f.now.Sub(f.since) > f.maxRing:
		return "the alarm started too long ago"
	}
	return ""
}

// otherInput reports whether src is an input the alarm never plays on. The
// alarm plays a preset, which lands on UPNP, a native radio source or
// Spotify, never on these.
func otherInput(src string) bool {
	switch src {
	case "BLUETOOTH", "AUX", "AIRPLAY", "PRODUCT":
		return true
	}
	return false
}

// ringMaxFor is how long a fire of a counts as ringing.
func ringMaxFor(a alarm.Alarm) time.Duration {
	if a.AutoOff > 0 {
		return time.Duration(a.AutoOff) * time.Minute
	}
	return alarmRingMax
}

// stopStamps reads the server-wide signals the end conditions are made of.
func (s *Server) stopStamps() (curGen uint64, explicit, standby time.Time) {
	s.lastPlayMu.Lock()
	curGen = s.recallGen
	s.lastPlayMu.Unlock()
	s.lastExplicitStopMu.Lock()
	explicit = s.lastExplicitStop
	s.lastExplicitStopMu.Unlock()
	s.standbyStopMu.Lock()
	standby = s.lastStandbyStop
	s.standbyStopMu.Unlock()
	return curGen, explicit, standby
}

// ringStart records a (re)fire of a, starting now. A snooze still pending
// for an earlier alarm is dropped: the newer alarm takes over.
func (s *Server) ringStart(a alarm.Alarm, due, started time.Time) uint64 {
	s.ring.mu.Lock()
	again := s.ring.active && s.ring.alarm.ID == a.ID && s.ring.due.Equal(due)
	replaced := s.ring.active && !again
	prevID := s.ring.alarm.ID
	if s.ring.timer != nil {
		s.ring.timer.Stop()
		s.ring.timer = nil
	}
	if !again {
		s.ring.snoozes = 0
	}
	s.ring.active, s.ring.armed = true, false
	s.ring.alarm, s.ring.due, s.ring.since = a, due, started
	s.ring.gen, s.ring.sleepGen = 0, 0
	s.ring.snoozeUntil, s.ring.snoozedViaAux, s.ring.standbyAfter = time.Time{}, false, time.Time{}
	s.ring.seq++
	seq := s.ring.seq
	s.ring.mu.Unlock()
	if replaced {
		s.logger.Info("alarm: a new alarm takes over from the one still ringing", "id", a.ID, "previous", prevID)
	}
	return seq
}

// ringArmed marks the (re)fire seq as ringing: its recall returned with gen.
func (s *Server) ringArmed(seq, gen uint64) {
	s.ring.mu.Lock()
	defer s.ring.mu.Unlock()
	if s.ring.active && s.ring.seq == seq {
		s.ring.armed = true
		s.ring.gen = gen
	}
}

// ringDisarm puts the (re)fire seq back to starting while the verify's retry
// wakes and recalls again; ringArmed with the retry's generation ends it.
func (s *Server) ringDisarm(seq uint64) {
	s.ring.mu.Lock()
	defer s.ring.mu.Unlock()
	if s.ring.active && s.ring.seq == seq {
		s.ring.armed = false
	}
}

// ringNoteAutoOff records the sleep timer the (re)fire seq armed.
func (s *Server) ringNoteAutoOff(seq, sleepGen uint64) {
	s.ring.mu.Lock()
	defer s.ring.mu.Unlock()
	if s.ring.active && s.ring.seq == seq {
		s.ring.sleepGen = sleepGen
	}
}

// ringMovedOn reports whether the (re)fire seq was snoozed, ended or replaced
// since, and how, for the fire's verify: a verify must never re-push a
// snoozed alarm.
func (s *Server) ringMovedOn(seq uint64) (bool, string) {
	s.ring.mu.Lock()
	defer s.ring.mu.Unlock()
	if s.ring.seq == seq {
		return false, ""
	}
	if s.ring.active && !s.ring.snoozeUntil.IsZero() {
		return true, "snoozed"
	}
	return true, "the alarm ended or was replaced"
}

// endRing ends the ring (and cancels a pending snooze). seq 0 ends whatever
// rings; otherwise only that (re)fire or snooze. Reports whether it ended one.
func (s *Server) endRing(seq uint64, why string) bool {
	s.ring.mu.Lock()
	if !s.ring.active || (seq != 0 && seq != s.ring.seq) {
		s.ring.mu.Unlock()
		return false
	}
	snoozed := !s.ring.snoozeUntil.IsZero()
	if s.ring.timer != nil {
		s.ring.timer.Stop()
		s.ring.timer = nil
	}
	id := s.ring.alarm.ID
	s.ring.active, s.ring.armed = false, false
	s.ring.snoozeUntil, s.ring.snoozedViaAux, s.ring.standbyAfter = time.Time{}, false, time.Time{}
	s.ring.seq++
	s.ring.mu.Unlock()
	if snoozed {
		s.logger.Info("alarm: snooze cancelled", "id", id, "why", why)
	} else {
		s.logger.Info("alarm: no longer ringing", "id", id, "why", why)
	}
	return true
}

// AlarmSnoozePress is the press handler of a thumbs key bound to snooze.
// Wired into the key handler in cmd/agent; it reads the speaker once, so the
// caller runs it off the log reader's goroutine.
func (s *Server) AlarmSnoozePress(key string) { s.snoozePress(key, "") }

// AlarmSourceChanged follows the speaker's source for the alarm: STANDBY ends
// a ringing alarm (power key, standby, the switch-off), and the switch to AUX
// is the AUX key, which snoozes when AUX carries the snooze action. Called on
// the gabbo read loop, so it never blocks.
func (s *Server) AlarmSourceChanged(from, to string) {
	switch to {
	case "STANDBY":
		s.ring.mu.Lock()
		armed, seq := s.ring.active && s.ring.armed, s.ring.seq
		settling := !s.ring.standbyAfter.IsZero() && time.Now().Before(s.ring.standbyAfter)
		s.ring.mu.Unlock()
		if armed && settling {
			s.logger.Info("alarm: the speaker dropped to standby right after the snooze stopped it; the snooze stays, the alarm wakes it again", "from", from)
			return
		}
		if armed {
			s.endRing(seq, "the speaker was switched off")
		}
	case "AUX":
		if s.keyActions.Action(keyactions.KeyAux) != keyactions.ActionSnooze {
			return
		}
		go s.snoozePress(keyactions.KeyAux, from)
	case "BLUETOOTH":
		if s.keyActions.Action(keyactions.KeyAux) != keyactions.ActionSnooze {
			return
		}
		at := time.Now()
		go s.bluetoothSnoozeCandidate(from, at)
	}
}

// AlarmAuxKeyPress is the AUX key as the speaker's key trace names it
// (AUX_INPUT), for a speaker whose firmware logs it. It snoozes when AUX
// carries snooze; the source change that follows the same press is then a
// repeat and changes nothing.
func (s *Server) AlarmAuxKeyPress() {
	if s.keyActions.Action(keyactions.KeyAux) != keyactions.ActionSnooze {
		return
	}
	s.snoozePress(keyactions.KeyAux, "")
}

// bluetoothSnoozeCandidate decides whether a switch to BLUETOOTH is the AUX
// key. Measured on a SoundTouch 10 (27.0.6, PR #1418): with nothing plugged
// into AUX, the AUX key skips the empty input and lands on BLUETOOTH, and the
// only sign of a key is the bare userActivityUpdate frame, 21 ms after the
// source change. So a switch to Bluetooth counts as the AUX press only with a
// key frame within alarmAuxKeyWindow either side of it; a phone that connects
// by itself brings no key frame and never snoozes anything.
//
// The wait (at most alarmAuxKeyWindow, a few cheap reads) runs only while an
// alarm rings, so a speaker without a ringing alarm pays nothing.
func (s *Server) bluetoothSnoozeCandidate(from string, at time.Time) {
	s.ring.mu.Lock()
	ringing := s.ring.active && s.ring.armed && s.ring.snoozeUntil.IsZero()
	s.ring.mu.Unlock()
	if !ringing {
		s.logger.Debug("snooze: switch to Bluetooth with no alarm ringing, nothing to do", "from", from)
		return
	}
	deadline := at.Add(alarmAuxKeyWindow)
	for {
		if k := alarmKeyActivity(s); !k.IsZero() && absDur(k.Sub(at)) <= alarmAuxKeyWindow {
			s.logger.Info("snooze: switch to Bluetooth came with a key press, taking it as the AUX key (nothing plugged into AUX)",
				"from", from, "keyOffsetMs", k.Sub(at).Milliseconds())
			s.snoozePress(keyactions.KeyAux, from)
			return
		}
		if time.Now().After(deadline) {
			s.logger.Info("snooze: switch to Bluetooth without a key press (a phone connecting?), not a snooze", "from", from)
			return
		}
		time.Sleep(alarmAuxKeyPoll)
	}
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// snoozePress decides what a press of a snooze key does. prevSource is the
// source the speaker was on before the press, known for AUX (the source change
// carries it) and read live for the thumbs keys ("").
func (s *Server) snoozePress(key, prevSource string) {
	now := time.Now()
	viaAux := key == keyactions.KeyAux
	curGen, explicit, standby := s.stopStamps()

	s.ring.mu.Lock()
	if !s.ring.lastPress.IsZero() && now.Sub(s.ring.lastPress) < snoozeMinGap {
		s.ring.mu.Unlock()
		s.logger.Debug("snooze key: press ignored, same press reported twice", "key", key)
		return
	}
	s.ring.lastPress = now
	if !s.ring.active {
		s.ring.mu.Unlock()
		if viaAux {
			s.logger.Info("snooze key pressed, but no alarm is ringing: AUX stays the normal input", "key", key)
		} else {
			s.logger.Info("snooze key pressed, but no alarm is ringing: nothing to do", "key", key)
		}
		return
	}
	if until := s.ring.snoozeUntil; !until.IsZero() {
		id := s.ring.alarm.ID
		repeat := viaAux && s.ring.snoozedViaAux && now.Sub(s.ring.since) < alarmAuxRepeat
		s.ring.mu.Unlock()
		if repeat {
			s.logger.Debug("snooze: second signal of the same AUX press (key trace and source change), already snoozed", "id", id)
			return
		}
		wake := s.alarmClockText(until)
		s.logger.Info("snooze key pressed, the alarm is already snoozed", "key", key, "id", id, "until", wake)
		showSnoozeNotice(wake)
		return
	}
	if !s.ring.armed {
		id := s.ring.alarm.ID
		s.ring.mu.Unlock()
		s.logger.Info("snooze key pressed while the alarm is still starting: ignored", "key", key, "id", id)
		return
	}
	a, seq := s.ring.alarm, s.ring.seq
	facts := ringFacts{
		since: s.ring.since, ringGen: s.ring.gen, curGen: curGen,
		explicitStop: explicit, standbyStop: standby,
		maxRing: ringMaxFor(a), now: now,
	}
	s.ring.mu.Unlock()

	if why := ringEndReason(facts); why != "" {
		s.endRing(seq, why)
		s.logger.Info("snooze key pressed, but the alarm is no longer ringing: nothing to do", "key", key, "id", a.ID, "why", why)
		return
	}
	src := prevSource
	if !viaAux {
		ctx, cancel := context.WithTimeout(context.Background(), alarmLiveTimeout)
		src, _ = alarmLiveState(s, ctx)
		cancel()
	}
	if src == "STANDBY" {
		s.endRing(seq, "the speaker is switched off")
		s.logger.Info("snooze key pressed, but the speaker is switched off: nothing to do", "key", key, "id", a.ID)
		return
	}
	if otherInput(src) {
		s.endRing(seq, "the speaker plays another input ("+src+")")
		s.logger.Info("snooze key pressed, but the speaker plays another input: nothing to do", "key", key, "id", a.ID, "source", src)
		return
	}

	length := time.Duration(a.SnoozeMinutes()) * alarmSnoozeUnit
	s.ring.mu.Lock()
	if !s.ring.active || s.ring.seq != seq {
		s.ring.mu.Unlock()
		s.logger.Info("snooze key pressed, but the alarm changed in the meantime: nothing to do", "key", key, "id", a.ID)
		return
	}
	start := time.Now()
	until := start.Add(length)
	s.ring.seq++
	snoozeSeq := s.ring.seq
	s.ring.snoozeUntil, s.ring.snoozedViaAux = until, viaAux
	s.ring.since = start
	s.ring.standbyAfter = start.Add(alarmSnoozeSettle)
	s.ring.gen = curGen
	s.ring.snoozes++
	n := s.ring.snoozes
	sleepGen := s.ring.sleepGen
	s.ring.sleepGen = 0
	s.ring.timer = time.AfterFunc(length, func() { s.snoozeOver(snoozeSeq) })
	s.ring.mu.Unlock()

	if sleepGen != 0 && s.cancelSleepIfGen(sleepGen, "alarm snoozed") {
		s.logger.Info("alarm: its switch-off is paused for the snooze and counts again from the next ring", "id", a.ID)
	}
	s.silenceForSnooze(viaAux)
	wake := s.alarmClockText(until)
	s.logger.Info("alarm snoozed (kept in memory only: an agent restart before then drops the snooze)",
		"id", a.ID, "key", key, "minutes", a.SnoozeMinutes(), "until", wake, "snoozes", n, "source", src)
	showSnoozeNotice(wake)
}

// silenceForSnooze stops the alarm's playback, marked as the user's stop so
// no recovery path pushes it back. The speaker stays on. After AUX the
// firmware already left the stream, so only the bookkeeping runs.
func (s *Server) silenceForSnooze(viaAux bool) {
	s.NoteUserStop()
	s.tellSpotifyUserStopped("snooze")
	s.stopQueueIfActive("the alarm was snoozed")
	if viaAux {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	alarmSilence(s, ctx)
}

// silenceTransport is the Stop button's transport half: the box's own player
// for a source it fetches itself, the UPnP transport otherwise.
func (s *Server) silenceTransport(ctx context.Context) {
	if s.transportKeyFallback(ctx, "STOP") {
		return
	}
	if s.renderer == nil {
		return
	}
	if err := s.renderer.Stop(ctx); err != nil && !isWrongTransportState(err) {
		s.logger.Warn("alarm: could not stop playback for the snooze", "err", err)
	}
}

// snoozeOver is the snooze deadline: check once that nothing ended the alarm
// meanwhile, then ring again through the normal fire path.
func (s *Server) snoozeOver(seq uint64) {
	curGen, explicit, standby := s.stopStamps()
	s.ring.mu.Lock()
	if !s.ring.active || s.ring.seq != seq {
		s.ring.mu.Unlock()
		return // cancelled or replaced while the snooze ran
	}
	s.ring.timer = nil
	a, due, viaAux := s.ring.alarm, s.ring.due, s.ring.snoozedViaAux
	facts := ringFacts{
		since: s.ring.since, ringGen: s.ring.gen, curGen: curGen,
		explicitStop: explicit, standbyStop: standby, standbySince: s.ring.standbyAfter,
		now: time.Now(),
	}
	s.ring.mu.Unlock()

	if why := ringEndReason(facts); why != "" {
		s.endRing(seq, why+" during the snooze")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), alarmLiveTimeout)
	src, playing := alarmLiveState(s, ctx)
	cancel()
	// The input an AUX snooze landed on is the snooze itself: AUX, or
	// BLUETOOTH on firmware that skips an empty AUX. AUX may report a play
	// state with nothing plugged in, so it never counts as playing; Bluetooth
	// that actually plays is a phone the user started streaming from.
	snoozeInput := viaAux && (src == "AUX" || src == "BLUETOOTH")
	playingElse := playing && (!viaAux || src != "AUX")
	switch {
	case src == "STANDBY":
		// Not a reason to stand down on its own: a power-off during the
		// snooze already ended it through the source change. Standby here is
		// the firmware's own reaction to the snooze's stop, and the fire
		// path wakes the speaker as it does at the alarm time.
		s.logger.Info("alarm: the speaker went to standby during the snooze (after its own stop), waking it for the alarm", "id", a.ID)
	case otherInput(src) && !snoozeInput:
		s.endRing(seq, "the speaker plays another input ("+src+")")
		return
	case playingElse:
		s.endRing(seq, "something else is playing ("+src+")")
		return
	}
	cur, ok := s.alarmByID(a.ID)
	if !ok || !cur.Enabled {
		s.endRing(seq, "the alarm was deleted or switched off during the snooze")
		return
	}
	s.logger.Info("alarm: snooze over, ringing again", "id", cur.ID, "slot", cur.Slot, "volume", cur.Volume)
	s.alarmBackground.Add(1)
	go func() {
		defer s.alarmBackground.Done()
		s.driveAlarm(cur, due)
	}()
}

// alarmByID returns the alarm as the document holds it now, so a snooze
// re-fires with the slot and volume of the latest save.
func (s *Server) alarmByID(id string) (alarm.Alarm, bool) {
	if s.alarms == nil {
		return alarm.Alarm{}, false
	}
	for _, a := range s.alarms.Get().Alarms {
		if a.ID == id {
			return a, true
		}
	}
	return alarm.Alarm{}, false
}

// alarmClockText formats t as the alarm document's local "15:04".
func (s *Server) alarmClockText(t time.Time) string {
	loc := time.Local
	if s.alarms != nil {
		if l, err := s.alarms.Get().Location(); err == nil {
			loc = l
		}
	}
	return t.In(loc).Format("15:04")
}

// alarmRingView is the status block's and the debug section's view of a
// ringing alarm.
type alarmRingView struct {
	ID           string `json:"id"`
	Ringing      bool   `json:"ringing"`
	SnoozedUntil string `json:"snoozedUntil,omitempty"`
	Snoozes      int    `json:"snoozes,omitempty"`
}

// ringView is nil when no alarm rings.
func (s *Server) ringView() *alarmRingView {
	s.ring.mu.Lock()
	defer s.ring.mu.Unlock()
	if !s.ring.active {
		return nil
	}
	v := &alarmRingView{
		ID:      s.ring.alarm.ID,
		Ringing: s.ring.armed && s.ring.snoozeUntil.IsZero(),
		Snoozes: s.ring.snoozes,
	}
	if !s.ring.snoozeUntil.IsZero() {
		v.SnoozedUntil = s.ring.snoozeUntil.Format(time.RFC3339)
	}
	return v
}
