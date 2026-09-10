package webui

import (
	"context"
	"fmt"
	"time"

	"github.com/JRpersonal/streborn/internal/alarm"
	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/clocksync"
)

// The alarm clock: at a set time on set weekdays, wake the speaker and recall
// one of the six presets. The scheduler runs HERE, on the box, because the
// thing that sets an alarm (a phone, the desktop app) is not awake at 06:30.
//
// Two rules keep it honest, both about the clock rather than the alarm, because
// these speakers have no battery-backed RTC:
//
//  1. Nothing fires while the clock is implausible (clocksync.Implausible), so
//     a box that boots believing it is 2015 rings nothing.
//  2. A due instant more than alarmGrace old is skipped, so a restart at 06:31
//     still wakes you and a box first plugged in at 09:00 stays quiet.
//
// That is deliberately all. An earlier draft also refused to fire for anything
// due before the runner watched the clock become trustworthy, on the principle
// that it should only act on time it had witnessed. It was wrong: once the
// clock IS repaired it is accurate (the HTTP Date header is the true time), so
// "06:30 was two minutes ago" is a fact, not a reconstruction. Rule 2 already
// discards instants from the wrong decade, because they are nowhere near
// alarmGrace old on the corrected clock, and the fired state stops a repeat.
// The extra rule only ever ate a legitimate alarm whose clock repair landed a
// few minutes late - power restored at 06:31 for an 06:30 alarm - which is
// exactly the morning someone wants to be woken.
var (
	// alarmGrace is how late a due alarm may still fire. Long enough to cover
	// an agent restart or an OTA plus the wake itself, short enough that a late
	// boot is silent.
	alarmGrace = 5 * time.Minute
	// alarmMaxSleep caps how long the runner sleeps between evaluations.
	//
	// This cap is the whole reason the runner is a loop rather than one
	// time.AfterFunc per alarm, as the sleep timer does it. Go timers run on
	// the MONOTONIC clock: a timer armed for eight hours while the wall clock
	// reads 2015 still fires eight real hours later, long after clocksync
	// jumped the clock to the right decade, and the alarm would be silently
	// hours out. Re-deriving the next fire from the wall clock at least twice a
	// minute bounds that damage to one cycle.
	alarmMaxSleep = 30 * time.Second
	// alarmMinSleep keeps the loop from spinning if a fire leaves an alarm due.
	alarmMinSleep = time.Second
	// alarmFireTimeout bounds one wake-and-recall.
	alarmFireTimeout = 90 * time.Second
	// alarmWakeTimeout bounds the wake alone, so a speaker that never comes out
	// of standby cannot hold the recall up for the whole fire budget.
	alarmWakeTimeout = 20 * time.Second
	// alarmVerifyDelay is how long to let the recall's own verify (three
	// attempts, five seconds apart) finish before second-guessing it. A var so
	// the tests can shorten it.
	alarmVerifyDelay = 45 * time.Second
)

// Seams for the tests, in the sleep timer's style: the real implementations
// reach the firmware on fixed ports, which a test server on a random port
// cannot stand in for. Production always uses these.
var (
	alarmWakeBox = func(s *Server, ctx context.Context, vol int) {
		s.wakeForAlarm(ctx, vol)
	}
	alarmRecallSlot = func(s *Server, ctx context.Context, slot int) int {
		return s.recallSlotClean(ctx, slot)
	}
	alarmPlayingCheck = func(s *Server, slot int, since time.Time) bool {
		return s.alarmPlaying(slot, since)
	}
)

// alarmNowTime is the runner's clock. Nil means time.Now; the tests set it.
func (s *Server) alarmNowTime() time.Time {
	if s.alarmNow != nil {
		return s.alarmNow()
	}
	return time.Now()
}

// KickAlarms tells the runner the document changed, so an alarm moved earlier
// is picked up at once instead of at the next evaluation. Never blocks: the
// channel is buffered and a send that would block means a kick is already
// pending, which does the same job.
func (s *Server) KickAlarms() {
	if s.alarmReload == nil {
		return
	}
	select {
	case s.alarmReload <- struct{}{}:
	default:
	}
}

// runAlarms is the scheduler. One goroutine owns the whole feature: it is the
// only thing that reads the document to fire from and the only thing that
// fires, so unlike the sleep timer it needs no generation counter to prove a
// cancelled timer lost its race. An edit just kicks alarmReload.
func (s *Server) runAlarms(ctx context.Context) {
	if s.alarms == nil {
		return
	}
	s.logger.Info("alarms: scheduler started")
	for {
		now := s.alarmNowTime()
		s.evaluateAlarms(now)

		wait := s.alarms.Get().UntilNextFire(now, alarmMaxSleep)
		if wait > alarmMaxSleep {
			wait = alarmMaxSleep
		}
		if wait < alarmMinSleep {
			wait = alarmMinSleep
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			s.logger.Info("alarms: scheduler stopped")
			return
		case <-s.alarmReload:
			t.Stop()
		case <-t.C:
		}
	}
}

// evaluateAlarms fires everything that is due and not yet fired. Called only
// from runAlarms, which is the single owner of the whole feature.
func (s *Server) evaluateAlarms(now time.Time) {
	if s.alarms == nil || s.alarmState == nil {
		return
	}
	if !s.alarmClockUsable(now) {
		return
	}
	doc := s.alarms.Get()
	loc, err := doc.Location()
	if err != nil {
		// A zone that no longer resolves schedules nothing rather than quietly
		// reverting to UTC and going off an hour out.
		if !s.alarmZoneWarned {
			s.logger.Warn("alarms: the stored time zone is unknown, nothing is scheduled", "zone", doc.Zone, "err", err)
			s.alarmZoneWarned = true
		}
		return
	}
	s.alarmZoneWarned = false

	keep := make(map[string]bool, len(doc.Alarms))
	for _, a := range doc.Alarms {
		keep[a.ID] = true
	}
	s.alarmState.Prune(keep)

	for _, a := range doc.Alarms {
		if !a.Enabled {
			continue
		}
		due, ok := a.MostRecentDue(now, loc)
		if !ok || now.Sub(due) > alarmGrace {
			continue
		}
		if !s.alarmState.FiredFor(a.ID).Before(due) {
			continue
		}
		s.fireAlarm(a, due)
	}
}

// alarmClockUsable reports whether the wall clock can be trusted at all. The
// clockBad flag exists only to log the two transitions once each, rather than
// on every evaluation while a box waits for its clock.
func (s *Server) alarmClockUsable(now time.Time) bool {
	if clocksync.Implausible(now) {
		s.alarmMu.Lock()
		first := !s.alarmClockBad
		s.alarmClockBad = true
		s.alarmMu.Unlock()
		if first {
			s.logger.Warn("alarms: the speaker does not know the time yet, nothing will go off until it does", "clock", now.UTC())
		}
		return false
	}
	s.alarmMu.Lock()
	healed := s.alarmClockBad
	if healed {
		s.alarmClockBad = false
	}
	s.alarmMu.Unlock()
	if healed {
		// Anything still inside its grace window on the corrected clock fires
		// from here, which is what a power cut just before an alarm deserves.
		s.logger.Info("alarms: the clock is trustworthy again", "now", now.UTC())
	}
	return true
}

// AlarmClockTrusted reports whether the scheduler currently believes the clock,
// for the status block the editor shows.
func (s *Server) AlarmClockTrusted() bool {
	return !clocksync.Implausible(s.alarmNowTime())
}

// fireAlarm marks the alarm fired and drives the box in the background, so a
// wake that takes eight seconds does not hold up the evaluation loop.
func (s *Server) fireAlarm(a alarm.Alarm, due time.Time) {
	// An empty slot is not worth waking a bedroom for.
	if s.presets != nil {
		if _, ok := s.presets.Get(a.Slot); !ok {
			s.logger.Warn("alarm: that preset is empty, not waking the speaker", "id", a.ID, "slot", a.Slot)
			_ = s.alarmState.MarkFired(a.ID, due)
			s.alarmState.NoteOutcome(alarm.FireOutcome{
				ID: a.ID, At: due, Slot: a.Slot, OK: false, Detail: "the preset is empty",
			})
			return
		}
	}
	// Marked BEFORE the wake, not after: a crash midway through costs one
	// missed alarm instead of a re-fire loop on a box whose memory guard
	// reboots it.
	if err := s.alarmState.MarkFired(a.ID, due); err != nil {
		// Still fire. A repeat after a restart is a smaller problem than a
		// morning of silence because one NAND write failed.
		s.logger.Warn("alarm: could not record the fire, it may repeat after a restart", "id", a.ID, "err", err)
	}
	s.alarmBackground.Add(1)
	go func() {
		defer s.alarmBackground.Done()
		s.driveAlarm(a, due)
	}()
}

func (s *Server) driveAlarm(a alarm.Alarm, due time.Time) {
	firedAt := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), alarmFireTimeout)
	defer cancel()

	// Armed BEFORE the wake, not after the recall: the firmware resumes its own
	// last station on power-on (wakeForAlarm stops it a moment later), and that
	// resume already reports a play start over gabbo, which is a group re-form
	// trigger. By the time the recall runs it would be too late.
	s.hushGroupForm(groupFormHushWindow)
	// Clears the stop latches, so the recall's own verify does not read a
	// stale user-stop as a reason to stand down (#419).
	s.NoteUserPlay()
	alarmWakeBox(s, ctx, a.Volume)
	status := alarmRecallSlot(s, ctx, a.Slot)
	gen := s.RecallGeneration()
	s.logger.Info("alarm: fired", "id", a.ID, "slot", a.Slot, "volume", a.Volume,
		"autoOffMin", a.AutoOff, "status", status)

	if status >= 400 {
		// The recall refused for a structural reason (an unplayable Spotify
		// preset, no Spotify login). Waking the box a second time would not
		// change that answer.
		detail := fmt.Sprintf("the preset could not be played (%d)", status)
		s.logger.Warn("alarm: the preset could not be played, not retrying", "id", a.ID, "status", status)
		s.alarmState.NoteOutcome(alarm.FireOutcome{ID: a.ID, At: due, Slot: a.Slot, OK: false, Detail: detail})
		return
	}
	if a.AutoOff > 0 {
		// The sleep timer, armed from here: the same standby, the same "already
		// asleep, stand down" guard and the same NoteUserStop (#419), and it
		// shows on the phone's sleep card so the user can see it and cancel it.
		// group=false for the same reason the group re-form is hushed above: an
		// alarm is per speaker, and switching off other rooms because THIS
		// alarm timed out would be the worse default.
		//
		// A monotonic timer is right here, unlike the scheduler above: this is
		// armed at fire time, a moment the scheduler has already judged the
		// clock trustworthy, and it counts tens of minutes rather than hours.
		s.armSleep(time.Duration(a.AutoOff)*time.Minute, false)
	}
	s.alarmBackground.Add(1)
	go func() {
		defer s.alarmBackground.Done()
		s.verifyAlarm(a, due, gen, firedAt)
	}()
}

// verifyAlarm sits AFTER the recall's own verify (three attempts, five seconds
// apart) has run out, and gives the alarm exactly one more try. One, not a
// loop: a dead station at 06:30 should leave a diagnosable log line, not a
// speaker re-pushing the same URL until somebody unplugs it.
func (s *Server) verifyAlarm(a alarm.Alarm, due time.Time, gen uint64, firedAt time.Time) {
	time.Sleep(alarmVerifyDelay)
	if reason := s.recallStandDownReason(gen, firedAt); reason != "" {
		// The user already dealt with it. Pushing the station back thirty
		// seconds after they hit stop is the #197 vector all over again.
		s.logger.Info("alarm: standing down", "id", a.ID, "why", reason)
		s.alarmState.NoteOutcome(alarm.FireOutcome{ID: a.ID, At: due, Slot: a.Slot, OK: true, Detail: reason})
		return
	}
	if alarmPlayingCheck(s, a.Slot, firedAt) {
		s.alarmState.NoteOutcome(alarm.FireOutcome{ID: a.ID, At: due, Slot: a.Slot, OK: true})
		return
	}

	s.logger.Warn("alarm: the stream did not start, trying once more", "id", a.ID, "slot", a.Slot)
	retryAt := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), alarmFireTimeout)
	// A second arm rather than one wider window on the first: this retry is a
	// whole second wake and recall, landing a minute or so after the fire, and
	// it re-opens every group re-form path the first one did.
	s.hushGroupForm(groupFormHushWindow)
	s.NoteUserPlay()
	alarmWakeBox(s, ctx, a.Volume)
	alarmRecallSlot(s, ctx, a.Slot)
	retryGen := s.RecallGeneration()
	cancel()

	time.Sleep(alarmVerifyDelay)
	if reason := s.recallStandDownReason(retryGen, retryAt); reason != "" {
		s.logger.Info("alarm: standing down after the retry", "id", a.ID, "why", reason)
		s.alarmState.NoteOutcome(alarm.FireOutcome{ID: a.ID, At: due, Slot: a.Slot, OK: true, Detail: reason})
		return
	}
	if alarmPlayingCheck(s, a.Slot, retryAt) {
		s.alarmState.NoteOutcome(alarm.FireOutcome{ID: a.ID, At: due, Slot: a.Slot, OK: true, Detail: "played after a retry"})
		return
	}
	s.logger.Warn("alarm: the speaker never started playing", "id", a.ID, "slot", a.Slot)
	s.alarmState.NoteOutcome(alarm.FireOutcome{
		ID: a.ID, At: due, Slot: a.Slot, OK: false, Detail: "the speaker never started playing",
	})
}

// alarmPlaying reports whether the recall actually produced audio. The slot
// proxy is the strongest signal available, because it requires a SUSTAINED
// fetch rather than the box opening a socket once; Spotify and library presets
// do not go through it, so a busy transport stands in for them.
func (s *Server) alarmPlaying(slot int, since time.Time) bool {
	if s.streamProxy != nil && s.streamProxy.SlotPulledSince(slot, since) {
		return true
	}
	_, busy := s.boxPlayLocation()
	return busy
}

// wakeForAlarm is quietWake's sequence with the mute replaced by the alarm's
// own level and no restore armed: an alarm is the one wake that IS about
// listening to the speaker.
//
// vol == 0 means leave the speaker at whatever level it remembers. There is no
// restore afterwards on purpose, following the same verdict the hardware recall
// reached: a control that cannot tell whose intent it is enforcing should not
// enforce one, and putting the level back at some arbitrary later moment would
// fight a user who reached over and turned it down.
func (s *Server) wakeForAlarm(ctx context.Context, vol int) {
	if s.boxHost == "" {
		return
	}
	c := boxapi.New(s.boxHost)
	if !quietWakeNeeded(quietWakeNowPlaying(ctx, s.boxHost)) {
		// Already up, so the write lands and stays.
		if vol > 0 {
			if err := c.SetVolume(ctx, vol); err != nil {
				s.logger.Warn("alarm: could not set the level", "vol", vol, "err", err)
			}
		}
		return
	}
	// The level has to land AFTER the power-on: one written while the speaker
	// sleeps is accepted by its API and then overwritten by the firmware's own
	// remembered level the moment it powers on (measured 2026-09-06, see
	// quietWake). So a watcher polls for the first sign of life and writes it
	// a few hundred milliseconds into the resume.
	// The watcher polls until its context ends, so it gets one of its own
	// rather than the whole fire's: on a speaker that never comes back, waiting
	// for it on the 90 s fire context would hold the recall up for a minute and
	// a half instead of letting it try anyway.
	wctx, cancel := context.WithTimeout(ctx, alarmWakeTimeout)
	var levelDone <-chan struct{}
	if vol > 0 {
		levelDone = s.setVolumeOnFirstLife(wctx, vol)
	}
	if err := boxcli.WakeAndWait(wctx, s.boxHost, 8*time.Second, s.logger); err != nil {
		s.logger.Warn("alarm: the speaker did not come out of standby", "err", err)
	}
	if levelDone != nil {
		<-levelDone
	}
	cancel()
	if vol > 0 {
		// Belt and braces: the watcher may have raced the firmware's own level
		// restore, so the level is written once more now the box is up.
		_ = c.SetVolume(ctx, vol)
	}
	// Whatever the firmware resumed on power-on is stopped, so the alarm's own
	// preset starts on an idle speaker. Without this the room gets half a
	// minute of yesterday's station, at the alarm's level, before the recall
	// lands.
	if np := quietWakeNowPlaying(ctx, s.boxHost); np.PlayStatus != "" && np.PlayStatus != "STOP_STATE" && np.Source != "STANDBY" {
		_ = c.Key(ctx, "STOP")
	}
}
