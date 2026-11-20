// Carrying on with the music after an agent update.
//
// An update restarts the speaker (or, on a coprocessor Wi-Fi ST20, only the
// agent), and a speaker that was playing came back silent: the transfer is
// quietened first (hushforupload.go) and the automatic power-on resume stands
// down after an update on purpose, because a speaker that was SILENT before the
// update must not start playing after it (see otaRebootMarkerPath). What was
// missing is the other half: a speaker that WAS playing carries on.
//
// It is plain behaviour, tied to the update and not to the "resume on power
// on" setting, so it also runs with that setting off. Nothing new is
// remembered about the station: the play on record (last-play.json, the
// power-on resume's own memory) is what is played again, through the same
// push the power-on resume uses (resumePushLocked). The only new state is one
// small marker on the NAND that says "this record was playing when the update
// was accepted, at this volume", written once per update and removed when it
// is used.
//
// The moving parts, in the order they happen:
//
//  1. An upload quietens the speaker. The desktop app usually delivers the
//     Spotify engine right before the agent, so by the time the agent upload
//     arrives the speaker is already silent. noteHushStopped remembers what
//     the earlier transfer stopped, in memory.
//  2. The agent upload is accepted. planUpdateResume decides whether the
//     speaker counts as "was playing" and, when it does, the marker is
//     written right before the restart (writeUpdateResumeMarker).
//  3. The new agent starts, loads the marker (loadUpdateResume) and waits for
//     the firmware to connect. That event, not a timer, starts the resume
//     (ResumeAfterUpdate from RecoverAfterReconnect).
//  4. An engine that could not be delivered before the restart arrives after
//     it and quietens the speaker once more. afterEngineUpload puts the music
//     back when that transfer is over.

package webui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/atomicfile"
	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/clocksync"
)

// defaultUpdateResumePath is the NAND marker an accepted update leaves when the
// speaker was playing. One small file per update, removed when it is used.
const defaultUpdateResumePath = "/mnt/nv/streborn/resume-after-update"

// updateResumeMaxAge is how old the marker may be and still bring the music
// back. An update restart takes a minute or two, twice that on a chassis that
// reboots a second time for its boot files. Anything older is a speaker that
// was unplugged in between, and music starting on its own then is a surprise.
const updateResumeMaxAge = 15 * time.Minute

// updateResumeMarker is the marker file. BoxURL names the play on record
// (last-play.json) the resume is for: another station on record since is
// somebody's newer choice and is left alone. PlayTS is when that play
// started; a newer time on the same station only means it was announced or
// started again (see updateResumeSuperseded).
type updateResumeMarker struct {
	BoxURL string    `json:"boxURL"`
	PlayTS time.Time `json:"playTS"`
	// Volume is the level the speaker had, -1 when it could not be read.
	Volume int `json:"volume"`
	// At is when the update was accepted, by the wall clock. The speakers
	// have no battery clock, so the reader waits for a plausible clock
	// before it judges the age.
	At time.Time `json:"at"`
	// followUp marks the second resume, after the engine delivery that
	// followed the restart. Never on the NAND.
	followUp bool
}

// updateResumeTiming is every wait of the resume. All of them are bounded
// waits inside one goroutine that ends; nothing here repeats on its own.
type updateResumeTiming struct {
	// settle lets the freshly connected firmware report an unambiguous state.
	settle time.Duration
	// clockWait and clockStep bound the wait for a plausible wall clock.
	clockWait, clockStep time.Duration
	// boxReads and boxReadGap bound the wait for the speaker's first answer.
	boxReads   int
	boxReadGap time.Duration
	// pushes and pushGap bound the attempts to start the stream: right after
	// a boot the renderer can refuse the first one.
	pushes  int
	pushGap time.Duration
	// selfStartChecks and selfStartGap bound how long a firmware that
	// announced the station on waking gets to start it by itself.
	selfStartChecks int
	selfStartGap    time.Duration
	// volumeChecks and volumeCheckGap bound the read-back of the level
	// after the wake.
	volumeChecks   int
	volumeCheckGap time.Duration
	// volumeSettle is how long after the start the level is checked again
	// (the firmware settles its own volume as the source comes up).
	volumeSettle time.Duration
}

var productionUpdateResumeTiming = updateResumeTiming{
	settle:          2 * time.Second,
	clockWait:       60 * time.Second,
	clockStep:       3 * time.Second,
	boxReads:        5,
	boxReadGap:      4 * time.Second,
	pushes:          3,
	pushGap:         5 * time.Second,
	selfStartChecks: 4,
	selfStartGap:    1500 * time.Millisecond,
	volumeChecks:    6,
	volumeCheckGap:  500 * time.Millisecond,
	volumeSettle:    startVolumeApplyDelay,
}

// updateResumeFx is what the resume does to the world. Production reaches the
// speaker; the tests install their own.
type updateResumeFx struct {
	now     func() time.Time
	sleep   func(time.Duration)
	clockOK func(time.Time) bool
	// nowPlaying reads what the speaker plays; an empty Source means it did
	// not answer.
	nowPlaying func() nowPlayingSnapshot
	volume     func() (int, bool)
	setVolume  func(int) error
	// follower reports whether the speaker follows a group.
	follower func() (bool, string)
	wake     func()
	// push starts the stream; done reports that the speaker turned out to be
	// playing already and nothing was sent.
	push func(m updateResumeMarker) (done bool, err error)
}

// updateResumeState is the in-memory half. mu is taken alone.
type updateResumeState struct {
	mu sync.Mutex
	// hushedAt is when an upload last stopped STR's playback, hushedURL and
	// hushedTS the play on record at that moment.
	hushedAt  time.Time
	hushedURL string
	hushedTS  time.Time
	// pending is the resume still to run, running whether its goroutine is
	// under way, waitingForEngine that it stood back for an engine transfer.
	pending          *updateResumeMarker
	running          bool
	waitingForEngine bool
	// resumed is what was brought back after the update and when, kept so an
	// engine delivery that quietens the speaker afterwards can put it back.
	resumed   *updateResumeMarker
	resumedAt time.Time
}

func (s *Server) updateResumeFile() string {
	if s.updateResumePath != "" {
		return s.updateResumePath
	}
	return defaultUpdateResumePath
}

func (s *Server) updateResumeTimes() *updateResumeTiming {
	if s.updateResumeTiming != nil {
		return s.updateResumeTiming
	}
	return &productionUpdateResumeTiming
}

func (s *Server) updateResumeEffects() *updateResumeFx {
	if s.updateResumeFx != nil {
		return s.updateResumeFx
	}
	return &updateResumeFx{
		now:     time.Now,
		sleep:   time.Sleep,
		clockOK: func(t time.Time) bool { return !clocksync.Implausible(t) },
		nowPlaying: func() nowPlayingSnapshot {
			np, err := s.readBoxNowWithin(5 * time.Second)
			if err != nil {
				return nowPlayingSnapshot{}
			}
			return snapshotOf(np)
		},
		volume:    s.readOwnVolume,
		setVolume: s.setOwnVolume,
		follower:  s.zonePushWouldFightGroup,
		wake: func() {
			if s.boxHost == "" {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			_ = boxcli.WakeAndWait(ctx, s.boxHost, 6*time.Second, s.logger)
		},
		push: s.pushUpdateResume,
	}
}

// readOwnVolume reads the speaker's level; ok is false when it did not answer.
func (s *Server) readOwnVolume() (int, bool) {
	if s.boxHost == "" {
		return 0, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	v, err := levelsClient(s.boxHost).GetVolume(ctx)
	if err != nil {
		return 0, false
	}
	return v.Target, true
}

func (s *Server) setOwnVolume(vol int) error {
	if s.boxHost == "" {
		return errors.New("no speaker address")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	s.noteOwnVolumeWrite(vol)
	return levelsClient(s.boxHost).SetVolume(ctx, vol)
}

// ---------------------------------------------------------------------------
// Before the restart: deciding and writing the marker.
// ---------------------------------------------------------------------------

// noteHushStopped remembers that an upload just stopped STR's playback, and
// which play was on record. The agent upload that follows an engine delivery
// finds a silent speaker and needs this to know it was playing a moment ago.
func (s *Server) noteHushStopped() {
	st := &s.updResume
	st.mu.Lock()
	defer st.mu.Unlock()
	st.hushedAt = s.updateResumeEffects().now()
	st.hushedURL, st.hushedTS = "", time.Time{}
	if lp, ok := s.play.snapshot(); ok {
		st.hushedURL, st.hushedTS = lp.boxURL, lp.ts
	}
}

// updateResumeInput is everything the "was it playing" decision looks at.
type updateResumeInput struct {
	// np is the speaker's answer when the update arrived, before the hush.
	np nowPlayingSnapshot
	// last is the play on record, hasLast whether there is one.
	last    lastPlayInfo
	hasLast bool
	// hushedAt, hushedURL and hushedTS describe the last upload that stopped
	// STR's playback (zero when none did).
	hushedAt  time.Time
	hushedURL string
	hushedTS  time.Time
	// stoppedSince: the user asked for a stop or a pause after that hush.
	stoppedSince bool
	now          time.Time
	// follower is asked last and only when everything else says resume: it
	// costs a read of the speaker's zone.
	follower func() (bool, string)
}

// updateResumeSkipReason decides whether a speaker that is about to restart
// for an update gets its music back afterwards. Empty means yes; anything
// else is the reason it does not, worded for the log.
func updateResumeSkipReason(in updateResumeInput) string {
	live := isPlayingStatus(in.np.PlayStatus)
	switch {
	case live && in.np.Source != "UPNP" && in.np.Source != nativeRadioSource:
		// Bluetooth, AUX, AirPlay, the speaker's own Spotify: nothing STR
		// started and nothing STR can start again.
		return "the speaker is playing a source of its own (" + in.np.Source + "), which STR did not start"
	case live:
		// playing STR's source right now: judged on the record below
	case in.hushedAt.IsZero() || in.now.Sub(in.hushedAt) > updateResumeMaxAge || in.now.Before(in.hushedAt):
		return "the speaker was not playing (source " + orDash(in.np.Source) + ", " + orDash(in.np.PlayStatus) + ")"
	case !resumeSafeSource(in.np.Source):
		return "another source was selected on the speaker (" + in.np.Source + ")"
	case in.stoppedSince:
		return "playback was stopped or paused after an earlier transfer of this update quietened the speaker"
	case !in.hasLast || in.last.boxURL != in.hushedURL || !in.last.ts.Equal(in.hushedTS):
		return "something else was played after an earlier transfer of this update quietened the speaker"
	}
	if !in.hasLast || in.last.boxURL == "" {
		return "STR has no record of what the speaker is playing"
	}
	if isSpotifyBoxURL(in.last.boxURL) {
		return "Spotify is not started again after an update: its engine is replaced along with the agent"
	}
	if live && in.np.Source == "UPNP" && !locationServedBySTR(in.np.Location, in.last.boxURL) {
		return "the speaker is playing something STR did not start"
	}
	if in.follower != nil {
		if follows, why := in.follower(); follows {
			return "the speaker is a group member and not the master (" + why + "); a group is not rebuilt after an update"
		}
	}
	return ""
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// isSpotifyBoxURL reports whether a play on record is STR's Spotify stream.
func isSpotifyBoxURL(boxURL string) bool {
	return looksLikeSpotifyStreamURL(boxURL) || strings.Contains(boxURL, "/spotify/")
}

// planUpdateResume runs when an agent update is accepted, with what the
// speaker reported right before it was quietened. It returns the marker to
// write before the restart, nil when the speaker does not get its music back.
// Every outcome is one INFO line.
func (s *Server) planUpdateResume(np nowPlayingSnapshot) *updateResumeMarker {
	fx := s.updateResumeEffects()
	in := updateResumeInput{np: np, now: fx.now(), follower: fx.follower}
	in.last, in.hasLast = s.play.snapshot()
	st := &s.updResume
	st.mu.Lock()
	in.hushedAt, in.hushedURL, in.hushedTS = st.hushedAt, st.hushedURL, st.hushedTS
	st.mu.Unlock()
	if !in.hushedAt.IsZero() {
		in.stoppedSince = s.explicitStopAfter(in.hushedAt)
	}
	if reason := updateResumeSkipReason(in); reason != "" {
		s.logger.Info("resume after update: the speaker will not be started again after this update", "reason", reason)
		return nil
	}
	m := &updateResumeMarker{BoxURL: in.last.boxURL, PlayTS: in.last.ts, Volume: -1}
	if v, ok := fx.volume(); ok {
		m.Volume = v
	}
	return m
}

// writeUpdateResumeMarker puts the marker on the NAND right before the
// restart. The write is synced (atomicfile), so it is also safe in front of
// the exit that hands over to the swap helper. A nil marker writes nothing
// and clears one a failed earlier attempt may have left.
func (s *Server) writeUpdateResumeMarker(m *updateResumeMarker) {
	path := s.updateResumeFile()
	if m == nil {
		_ = os.Remove(path)
		return
	}
	m.At = s.updateResumeEffects().now().UTC()
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	if err := atomicfile.WriteFile(path, b, 0o644); err != nil {
		s.logger.Warn("resume after update: could not write the marker, the speaker stays silent after this update", "err", err)
		return
	}
	s.logger.Info("resume after update: the speaker was playing, it carries on after the restart",
		"title", s.updateResumeTitle(m.BoxURL), "volume", m.Volume)
}

// updateResumeTitle is the title of the play on record when it is still url.
func (s *Server) updateResumeTitle(url string) string {
	if lp, ok := s.play.snapshot(); ok && lp.boxURL == url {
		return lp.title
	}
	return ""
}

// dropUpdateResumeMarker removes the marker of an update that did not restart
// the speaker after all.
func (s *Server) dropUpdateResumeMarker() {
	_ = os.Remove(s.updateResumeFile())
}

// ---------------------------------------------------------------------------
// After the restart: using the marker, once.
// ---------------------------------------------------------------------------

// loadUpdateResume reads the marker once at agent start. The file stays where
// it is until the resume is decided, so a boot that ends before that (the
// second reboot some chassis take for their boot files) leaves it for the
// next one. Its age is judged later, once the clock is known to be right.
func (s *Server) loadUpdateResume() {
	path := s.updateResumeFile()
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var m updateResumeMarker
	if json.Unmarshal(b, &m) != nil || m.BoxURL == "" || m.At.IsZero() {
		_ = os.Remove(path)
		return
	}
	s.updResume.mu.Lock()
	s.updResume.pending = &m
	s.updResume.mu.Unlock()
}

// ResumeAfterUpdate starts the resume a marker asks for. Called when the
// firmware's socket connects, which is the moment the speaker is back. It
// reports whether there is such a resume, so the ordinary reconnect recovery
// leaves the speaker to it.
func (s *Server) ResumeAfterUpdate() bool {
	st := &s.updResume
	st.mu.Lock()
	if st.pending == nil {
		st.mu.Unlock()
		return false
	}
	if st.running {
		st.mu.Unlock()
		return true
	}
	st.running = true
	st.waitingForEngine = false
	m := *st.pending
	st.mu.Unlock()
	go s.runUpdateResume(m)
	return true
}

func (s *Server) runUpdateResume(m updateResumeMarker) {
	keep := s.attemptUpdateResume(m)
	if !keep {
		// Normally gone already (it is removed before the stream starts);
		// this covers every reason the resume was called off.
		s.dropUpdateResumeMarker()
	}
	st := &s.updResume
	st.mu.Lock()
	st.running = false
	if keep {
		st.waitingForEngine = true
	} else {
		st.pending = nil
	}
	st.mu.Unlock()
	if !keep {
		return
	}
	// The transfer may have ended while the lines above ran.
	if s.engineUploads.Load() == 0 {
		s.afterEngineUpload()
	}
}

// updateResumeStaleReason judges the marker's age; empty means fresh.
func updateResumeStaleReason(m updateResumeMarker, now time.Time) string {
	age := now.Sub(m.At)
	switch {
	case age > updateResumeMaxAge:
		return "the update was too long ago"
	case age < -time.Minute:
		return "the marker is from the future, the clock cannot be trusted"
	}
	return ""
}

// attemptUpdateResume is the resume itself. keep reports that it stood back
// for an engine transfer and has to run again when that is over; every other
// outcome uses the marker up.
func (s *Server) attemptUpdateResume(m updateResumeMarker) (keep bool) {
	fx, tm := s.updateResumeEffects(), s.updateResumeTimes()
	skip := func(reason string, args ...any) bool {
		s.logger.Info("resume after update: not starting the speaker again", append([]any{"reason", reason}, args...)...)
		return false
	}
	// The wall clock starts in the past on every boot and is corrected from
	// the network. Until then the marker's age cannot be judged.
	for waited := time.Duration(0); !fx.clockOK(fx.now()); waited += tm.clockStep {
		if waited >= tm.clockWait {
			return skip("the clock was not set in time, so the marker's age is unknown")
		}
		fx.sleep(tm.clockStep)
	}
	if reason := updateResumeStaleReason(m, fx.now()); reason != "" {
		return skip(reason, "ageMin", int(fx.now().Sub(m.At).Minutes()))
	}
	if s.autoResumeBlocked() {
		return skip("the last automatic resumes each ended in a reboot (crash-loop guard, #381)")
	}
	fx.sleep(tm.settle)
	if s.engineUploads.Load() > 0 {
		s.logger.Info("resume after update: the Spotify engine is being delivered, starting the speaker again once that is done")
		return true
	}
	if cur := s.play.current(); updateResumeSuperseded(m, cur) {
		return skip("another station is on record than the one the update interrupted, or none", s.updateResumeRecordArgs(m, cur)...)
	}
	// Stop requests live in memory only, so any on record is from after the
	// restart: the user's last word. (The follow-up was checked for this
	// before it was armed.)
	if !m.followUp && s.explicitStopAfter(time.Time{}) {
		return skip("playback was stopped or paused after the restart")
	}
	var np nowPlayingSnapshot
	for i := 0; ; i++ {
		if np = fx.nowPlaying(); np.Source != "" {
			break
		}
		if i+1 >= tm.boxReads {
			return skip("the speaker did not answer after the restart")
		}
		fx.sleep(tm.boxReadGap)
	}
	switch {
	case isPlayingStatus(np.PlayStatus) || np.PlayStatus == "PAUSE_STATE":
		return skip("the speaker is already playing", "source", np.Source, "playStatus", np.PlayStatus)
	case !resumeSafeSource(np.Source):
		return skip("another source was selected on the speaker", "source", np.Source)
	}
	if follows, why := fx.follower(); follows {
		return skip("the speaker is a group member and not the master", "why", why)
	}
	// Used up from here on, whatever the start below does: a stream that
	// takes the speaker down must not be tried again by the next boot.
	s.dropUpdateResumeMarker()
	s.lowerVolumeTo(fx, m.Volume, "before the wake")
	fx.wake()
	// The wake is where the level is lost: the firmware applies its own
	// power-on volume when it leaves standby, a moment after the wake
	// returns (Portable, field run of 2026-11-18: 5 before the update, set
	// back to 5 before the wake, 30 again half a second into it). So the
	// level is brought down and read back here, before any stream is pushed
	// and before a firmware that starts the station by itself is audible.
	s.holdVolumeDown(fx, tm, m.Volume)
	// The hush latched a deliberate stop; this start is wanted.
	s.lastUserStopMu.Lock()
	s.lastUserStop = time.Time{}
	s.lastUserStopMu.Unlock()
	var (
		err  error
		done bool
	)
	for i := 0; i < tm.pushes; i++ {
		if i > 0 {
			fx.sleep(tm.pushGap)
		}
		if done, err = fx.push(m); err == nil {
			if done {
				s.logger.Info("resume after update: the speaker started the station again by itself, nothing to push", "url", m.BoxURL)
			}
			break
		}
		if errors.Is(err, errUpdateResumeSuperseded) {
			return skip("another station was started while the speaker woke up", s.updateResumeRecordArgs(m, s.play.current())...)
		}
		s.logger.Info("resume after update: the speaker did not take the stream yet", "attempt", i+1, "err", err)
	}
	if err != nil {
		s.logger.Warn("resume after update: could not start the speaker again", "err", err, "url", m.BoxURL)
		return false
	}
	switch {
	case done:
		// logged above
	case m.followUp:
		s.logger.Info("resume after update: carried on again after the Spotify engine delivery", "url", m.BoxURL)
	default:
		s.logger.Info("resume after update: the speaker carries on with what it was playing before the update",
			"url", m.BoxURL, "title", s.updateResumeTitle(m.BoxURL), "ageSec", int(fx.now().Sub(m.At).Seconds()))
	}
	st := &s.updResume
	st.mu.Lock()
	if m.followUp {
		st.resumed = nil // one follow-up per update
	} else {
		st.resumed, st.resumedAt = &m, fx.now()
	}
	st.mu.Unlock()
	fx.sleep(tm.volumeSettle)
	s.lowerVolumeTo(fx, m.Volume, "after playback started")
	return false
}

// holdVolumeDown brings the level down after the wake and keeps reading it
// back until it stayed down for two readings in a row, or the bound is
// reached. A level that could not be read counts as not confirmed.
func (s *Server) holdVolumeDown(fx *updateResumeFx, tm *updateResumeTiming, want int) {
	if want < 0 {
		return
	}
	calm := 0
	for i := 0; i < tm.volumeChecks; i++ {
		if i > 0 {
			fx.sleep(tm.volumeCheckGap)
		}
		switch changed, ok := s.lowerVolumeTo(fx, want, "after the wake"); {
		case !ok || changed:
			calm = 0
		default:
			if calm++; calm >= 2 {
				return
			}
		}
	}
	if calm == 0 {
		s.logger.Warn("resume after update: the level could not be confirmed after the wake, the station may start louder than before the update", "want", want)
	}
}

// lowerVolumeTo ends at the level the speaker had before the update when it
// is louder now (a Portable that played at 5 restarted at 30). It never turns
// it up: a speaker that came back quieter stays quieter. step names the
// moment for the log; every change is one INFO line. changed reports that
// the level was set, ok that it is known to be at or below want now.
func (s *Server) lowerVolumeTo(fx *updateResumeFx, want int, step string) (changed, ok bool) {
	if want < 0 {
		return false, true
	}
	cur, read := fx.volume()
	if !read {
		return false, false
	}
	if cur <= want {
		return false, true
	}
	if err := fx.setVolume(want); err != nil {
		s.logger.Warn("resume after update: could not turn the speaker back down to its level", "err", err, "from", cur, "to", want, "step", step)
		return false, false
	}
	s.logger.Info("resume after update: the speaker was louder than before the update, turned it down to the level it had", "from", cur, "to", want, "step", step)
	return true, true
}

// errUpdateResumeSuperseded: another station took the record while the
// resume waited for the speaker.
var errUpdateResumeSuperseded = errors.New("another station was started")

// updateResumeSuperseded reports whether the play on record is no longer the
// station the marker is for.
//
// The station decides, not the record's timestamp. A speaker that wakes up
// announces the native radio preset it was on, and that announcement is
// recorded as a fresh play of the SAME station (NoteNativeLastPlay): same
// URL, new time. Comparing the time took that for somebody's newer choice,
// so a Portable that had played a native preset stayed silent after its
// update (field test of 2026-11-18: the record changed during the wake and
// the speaker sat on INVALID_SOURCE).
func updateResumeSuperseded(m updateResumeMarker, cur *lastPlayInfo) bool {
	return cur == nil || cur.boxURL != m.BoxURL
}

// updateResumeRecordArgs are both sides of that comparison for a log line.
func (s *Server) updateResumeRecordArgs(m updateResumeMarker, cur *lastPlayInfo) []any {
	args := []any{"markerURL", m.BoxURL, "markerPlayTS", m.PlayTS.UTC().Format(time.RFC3339Nano)}
	if cur == nil {
		return append(args, "recordURL", "(none)")
	}
	return append(args, "recordURL", cur.boxURL, "recordPlayTS", cur.ts.UTC().Format(time.RFC3339Nano), "recordFromSpeaker", cur.fromBox)
}

// pushUpdateResume starts the play on record again through the power-on
// resume's own push (resumePushLocked).
func (s *Server) pushUpdateResume(m updateResumeMarker) (done bool, err error) {
	if s.renderer == nil {
		return false, errNoRenderer
	}
	cur := s.play.current()
	if updateResumeSuperseded(m, cur) {
		return false, errUpdateResumeSuperseded
	}
	// The same station recorded anew since the marker was written: the
	// waking firmware announced it and is trying to start it by itself (or
	// somebody just started it). It gets a bounded moment to do so, because
	// a push into that start cuts it off. A start that does not come, as on
	// the Portable right after its boot, is ours to make.
	if !cur.ts.Equal(m.PlayTS) {
		fx, tm := s.updateResumeEffects(), s.updateResumeTimes()
		for i := 0; i < tm.selfStartChecks; i++ {
			if _, busy := s.boxPlayState(); busy {
				return true, nil
			}
			fx.sleep(tm.selfStartGap)
		}
	}
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	cur = s.play.current()
	if updateResumeSuperseded(m, cur) {
		return false, errUpdateResumeSuperseded
	}
	// A wake can make the firmware, or the power-on resume it triggers,
	// start the station while this waited for the lock.
	if _, busy := s.boxPlayState(); busy {
		return true, nil
	}
	// Counted and persisted before the push, like every automatic resume:
	// if this stream takes the speaker down, the next boot knows (#381).
	s.noteAutoResumeAttempt()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err = s.resumePushLocked(ctx, "resume after update", true, cur.boxURL, cur.title, cur.art, cur.mime, cur.ts)
	return false, err
}

// afterEngineUpload runs when an engine transfer is over, delivered or not.
// It starts the resume that stood back for the transfer, and puts the music
// back when the transfer quietened a speaker that had just been resumed.
func (s *Server) afterEngineUpload() {
	now := s.updateResumeEffects().now()
	st := &s.updResume
	st.mu.Lock()
	switch {
	case st.pending != nil && st.waitingForEngine && !st.running:
		st.mu.Unlock()
		s.ResumeAfterUpdate()
		return
	case st.pending == nil && st.resumed != nil && st.hushedAt.After(st.resumedAt) &&
		st.hushedURL == st.resumed.BoxURL && now.Sub(st.hushedAt) <= updateResumeMaxAge:
		hushedAt := st.hushedAt
		st.mu.Unlock()
		if s.explicitStopAfter(hushedAt) {
			s.logger.Info("resume after update: not starting the speaker again after the engine delivery", "reason", "playback was stopped or paused meanwhile")
			return
		}
		st.mu.Lock()
		if st.pending != nil || st.resumed == nil {
			st.mu.Unlock()
			return
		}
		m := *st.resumed
		m.PlayTS, m.At, m.followUp = st.hushedTS, now.UTC(), true
		st.resumed = nil
		st.pending = &m
		st.mu.Unlock()
		s.ResumeAfterUpdate()
		return
	}
	st.mu.Unlock()
}
