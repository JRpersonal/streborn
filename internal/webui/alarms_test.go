package webui

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/alarm"
	"github.com/JRpersonal/streborn/internal/presets"
)

// alarmHarness is a Server with every box-facing step replaced, so no test can
// reach the firmware's fixed ports.
type alarmHarness struct {
	s       *Server
	recalls *atomic.Int64
	wakes   *atomic.Int64
	volumes chan int
	// clock is read through s.alarmNow, which is assigned once and never
	// reassigned: the scheduler goroutine reads that field concurrently, and in
	// production it is set at construction and left alone.
	clock *atomic.Pointer[time.Time]
}

func newAlarmHarness(t *testing.T, doc alarm.Document) *alarmHarness {
	t.Helper()
	st := alarm.New()
	if err := st.Set(doc); err != nil {
		t.Fatalf("seed the document: %v", err)
	}
	h := &alarmHarness{
		s: &Server{
			logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
			alarms:      st,
			alarmState:  alarm.LoadState("", slog.New(slog.NewTextHandler(io.Discard, nil))),
			alarmReload: make(chan struct{}, 1),
		},
		recalls: &atomic.Int64{},
		wakes:   &atomic.Int64{},
		volumes: make(chan int, 8),
		clock:   &atomic.Pointer[time.Time]{},
	}
	start := time.Now()
	h.clock.Store(&start)
	h.s.alarmNow = func() time.Time { return *h.clock.Load() }

	prevWake, prevRecall, prevPlaying := alarmWakeBox, alarmRecallSlot, alarmPlayingCheck
	prevDelay := alarmVerifyDelay
	// Registered first, so it runs LAST: the seams must not be put back while a
	// fire's verify goroutine is still reading them.
	t.Cleanup(func() {
		alarmWakeBox, alarmRecallSlot, alarmPlayingCheck = prevWake, prevRecall, prevPlaying
		alarmVerifyDelay = prevDelay
	})
	t.Cleanup(h.s.alarmBackground.Wait)
	alarmWakeBox = func(_ *Server, _ context.Context, vol int) {
		h.wakes.Add(1)
		select {
		case h.volumes <- vol:
		default:
		}
	}
	alarmRecallSlot = func(_ *Server, _ context.Context, _ int) int {
		h.recalls.Add(1)
		return 200
	}
	// The default is "it played", so a test that is not about the fallback
	// never waits for one.
	alarmPlayingCheck = func(*Server, int, time.Time) bool { return true }
	alarmVerifyDelay = time.Millisecond
	return h
}

func (h *alarmHarness) at(when time.Time) { h.clock.Store(&when) }

func (h *alarmHarness) firedCount() int64 { return h.recalls.Load() }

func berlinTime(t *testing.T, y int, m time.Month, d, hh, mm int) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("Europe/Berlin: %v", err)
	}
	return time.Date(y, m, d, hh, mm, 0, 0, loc)
}

func weekdayDoc() alarm.Document {
	return alarm.Document{Zone: "Europe/Berlin", Alarms: []alarm.Alarm{{
		ID: "wake", Enabled: true, Hour: 6, Minute: 30,
		Days: []int{1, 2, 3, 4, 5}, Slot: 3, Volume: 25,
	}}}
}

func TestAlarmFiresWhenDue(t *testing.T) {
	h := newAlarmHarness(t, weekdayDoc())
	h.at(berlinTime(t, 2026, time.September, 7, 6, 30)) // a Monday
	h.s.evaluateAlarms(h.s.alarmNowTime())
	if !waitFor(func() bool { return h.firedCount() == 1 }) {
		t.Fatalf("the alarm did not fire: %d recalls", h.firedCount())
	}
	if !waitFor(func() bool { return h.wakes.Load() == 1 }) {
		t.Error("the speaker was never woken")
	}
	select {
	case vol := <-h.volumes:
		if vol != 25 {
			t.Errorf("woke at volume %d, want the alarm's 25", vol)
		}
	default:
		t.Error("no volume was passed to the wake")
	}
}

// The evaluation runs at least twice a minute, so "already fired" has to hold.
func TestAlarmFiresOnlyOnce(t *testing.T) {
	h := newAlarmHarness(t, weekdayDoc())
	now := berlinTime(t, 2026, time.September, 7, 6, 30)
	h.at(now)
	h.s.evaluateAlarms(now)
	if !waitFor(func() bool { return h.firedCount() == 1 }) {
		t.Fatal("the alarm did not fire at all")
	}
	for i := 1; i <= 4; i++ {
		later := now.Add(time.Duration(i) * 30 * time.Second)
		h.at(later)
		h.s.evaluateAlarms(later)
	}
	time.Sleep(50 * time.Millisecond)
	if got := h.firedCount(); got != 1 {
		t.Errorf("fired %d times across the grace window, want exactly 1", got)
	}
}

func TestAlarmDisabledNeverFires(t *testing.T) {
	doc := weekdayDoc()
	doc.Alarms[0].Enabled = false
	h := newAlarmHarness(t, doc)
	now := berlinTime(t, 2026, time.September, 7, 6, 30)
	h.at(now)
	h.s.evaluateAlarms(now)
	time.Sleep(50 * time.Millisecond)
	if got := h.firedCount(); got != 0 {
		t.Errorf("a disabled alarm fired %d times", got)
	}
}

// A box first plugged in at 09:00 must not start the radio for a 06:30 alarm.
func TestAlarmSkippedWhenTooLate(t *testing.T) {
	h := newAlarmHarness(t, weekdayDoc())
	now := berlinTime(t, 2026, time.September, 7, 9, 0)
	h.at(now)
	h.s.evaluateAlarms(now)
	time.Sleep(50 * time.Millisecond)
	if got := h.firedCount(); got != 0 {
		t.Errorf("an alarm two and a half hours late fired %d times", got)
	}
}

// A restart or an OTA at 06:31 still has to wake you.
func TestAlarmFiresInsideTheGraceWindow(t *testing.T) {
	h := newAlarmHarness(t, weekdayDoc())
	now := berlinTime(t, 2026, time.September, 7, 6, 33)
	h.at(now)
	h.s.evaluateAlarms(now)
	if !waitFor(func() bool { return h.firedCount() == 1 }) {
		t.Errorf("an alarm three minutes late did not fire: %d recalls", h.firedCount())
	}
}

func TestAlarmNeverFiresOnAnImplausibleClock(t *testing.T) {
	h := newAlarmHarness(t, weekdayDoc())
	// The firmware build epoch the speakers boot with, having no RTC.
	now := berlinTime(t, 2015, time.June, 1, 6, 30)
	h.at(now)
	h.s.evaluateAlarms(now)
	time.Sleep(50 * time.Millisecond)
	if got := h.firedCount(); got != 0 {
		t.Errorf("fired %d times on a 2015 clock", got)
	}
	if h.s.AlarmClockTrusted() {
		t.Error("a 2015 clock must not be reported as trusted")
	}
}

// A box whose power was restored just before an alarm boots believing it is
// 2015, repairs the clock from the network, and lands a minute or two past the
// alarm. It has to ring: that is the morning somebody most wants waking, and
// the repaired clock is the true time, so "06:30 was a minute ago" is a fact.
//
// An earlier draft refused this, on the principle that the runner should only
// act on time it had witnessed. It was wrong, and this test is the case that
// showed it.
func TestAlarmFiresWhenTheClockIsRepairedInsideTheGraceWindow(t *testing.T) {
	h := newAlarmHarness(t, weekdayDoc())

	// Several evaluations on the pre-repair clock. None may fire (rule 1).
	for i := 0; i < 3; i++ {
		bad := berlinTime(t, 2015, time.June, 1+i, 6, 31)
		h.at(bad)
		h.s.evaluateAlarms(bad)
	}
	time.Sleep(50 * time.Millisecond)
	if got := h.firedCount(); got != 0 {
		t.Fatalf("fired %d times on a 2015 clock, want 0", got)
	}

	// The repair lands one minute past the 06:30 alarm, inside the grace window.
	healed := berlinTime(t, 2026, time.September, 7, 6, 31)
	h.at(healed)
	h.s.evaluateAlarms(healed)
	if !waitFor(func() bool { return h.firedCount() == 1 }) {
		t.Fatalf("the alarm did not ring after the clock repair: %d recalls", h.firedCount())
	}

	// And it does not ring again on the next evaluation.
	h.s.evaluateAlarms(healed.Add(30 * time.Second))
	time.Sleep(50 * time.Millisecond)
	if got := h.firedCount(); got != 1 {
		t.Errorf("rang %d times, want exactly 1", got)
	}
}

// The other half of the same seam: a repair that lands hours late must stay
// quiet. A station starting at 09:40 because the box was offline at 06:30 is
// the failure everyone remembers.
func TestAlarmStaysQuietWhenTheClockIsRepairedTooLate(t *testing.T) {
	h := newAlarmHarness(t, weekdayDoc())
	bad := berlinTime(t, 2015, time.June, 1, 6, 31)
	h.at(bad)
	h.s.evaluateAlarms(bad)

	late := berlinTime(t, 2026, time.September, 7, 9, 40)
	h.at(late)
	h.s.evaluateAlarms(late)
	time.Sleep(80 * time.Millisecond)
	if got := h.firedCount(); got != 0 {
		t.Errorf("an alarm three hours stale fired %d times", got)
	}
}

func TestAlarmEmptyPresetDoesNotWakeTheSpeaker(t *testing.T) {
	h := newAlarmHarness(t, weekdayDoc())
	h.s.presets = presets.New() // slot 3 is empty
	now := berlinTime(t, 2026, time.September, 7, 6, 30)
	h.at(now)
	h.s.evaluateAlarms(now)
	time.Sleep(50 * time.Millisecond)
	if h.wakes.Load() != 0 || h.firedCount() != 0 {
		t.Errorf("an empty preset woke the speaker: %d wakes, %d recalls", h.wakes.Load(), h.firedCount())
	}
	// It still counts as handled, so it is not retried every 30 seconds for the
	// whole grace window.
	last, ok := h.s.alarmState.LastOutcome()
	if !ok || last.OK {
		t.Errorf("expected a recorded failure, got %+v (ok=%v)", last, ok)
	}
}

func TestAlarmFiresWithAPopulatedPreset(t *testing.T) {
	h := newAlarmHarness(t, weekdayDoc())
	ps, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatalf("preset store: %v", err)
	}
	if err := ps.SetSlot(presets.Preset{Slot: 3, Name: "Radio", StreamURL: "http://example.invalid/s"}); err != nil {
		t.Fatalf("seed the preset: %v", err)
	}
	h.s.presets = ps
	now := berlinTime(t, 2026, time.September, 7, 6, 30)
	h.at(now)
	h.s.evaluateAlarms(now)
	if !waitFor(func() bool { return h.firedCount() == 1 }) {
		t.Errorf("a populated preset did not fire: %d recalls", h.firedCount())
	}
}

// A zone that no longer resolves schedules nothing rather than quietly
// reverting to UTC and going off an hour out. Set validates, so the only way to
// hold such a document is to have loaded it from NAND, which is exactly the
// case this guards: a file written by a newer agent, or a zone dropped from a
// future tzdata.
func TestAlarmUnknownZoneSchedulesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alarms.json")
	raw := `{"zone":"Mars/Olympus","alarms":[{"id":"wake","enabled":true,"hour":6,"minute":30,"days":[1,2,3,4,5],"slot":3}]}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := alarm.Load(path, quiet)
	if err != nil {
		t.Fatalf("a parseable document is not a load error: %v", err)
	}
	h := newAlarmHarness(t, weekdayDoc())
	h.s.alarms = st
	now := berlinTime(t, 2026, time.September, 7, 6, 30)
	h.at(now)
	h.s.evaluateAlarms(now)
	time.Sleep(50 * time.Millisecond)
	if got := h.firedCount(); got != 0 {
		t.Errorf("an unresolvable zone fired %d alarms", got)
	}
}

// A save has to be picked up at once, not at the next evaluation.
func TestAlarmReloadKickWakesTheRunner(t *testing.T) {
	h := newAlarmHarness(t, weekdayDoc())
	h.at(berlinTime(t, 2026, time.September, 7, 3, 0)) // hours before the alarm
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.s.runAlarms(ctx)

	// Nothing is due, so the runner is parked on its timer.
	time.Sleep(30 * time.Millisecond)
	if got := h.firedCount(); got != 0 {
		t.Fatalf("fired %d times before anything was due", got)
	}
	// Move the clock into the alarm and kick.
	h.at(berlinTime(t, 2026, time.September, 7, 6, 30))
	h.s.KickAlarms()
	if !waitFor(func() bool { return h.firedCount() == 1 }) {
		t.Errorf("the kick did not make the runner re-evaluate: %d recalls", h.firedCount())
	}
}

func TestKickAlarmsNeverBlocks(t *testing.T) {
	s := &Server{alarmReload: make(chan struct{}, 1)}
	for i := 0; i < 10; i++ {
		s.KickAlarms() // the second and later sends would block on a full channel
	}
	// A server with no channel at all (a bare test Server) must be safe too.
	(&Server{}).KickAlarms()
}

// The retry exists, but only one, and only when nothing played.
func TestAlarmRetriesOnceWhenNothingPlayed(t *testing.T) {
	h := newAlarmHarness(t, weekdayDoc())
	alarmPlayingCheck = func(*Server, int, time.Time) bool { return false }
	now := berlinTime(t, 2026, time.September, 7, 6, 30)
	h.at(now)
	h.s.evaluateAlarms(now)
	if !waitFor(func() bool { return h.firedCount() == 2 }) {
		t.Fatalf("expected one fire plus one retry, got %d", h.firedCount())
	}
	// And it stops there rather than looping.
	time.Sleep(80 * time.Millisecond)
	if got := h.firedCount(); got != 2 {
		t.Errorf("the retry looped: %d recalls", got)
	}
	last, ok := h.s.alarmState.LastOutcome()
	if !ok || last.OK {
		t.Errorf("expected a recorded failure after the retry, got %+v (ok=%v)", last, ok)
	}
}

// An alarm is per speaker. The hush is armed before the wake, not after the
// recall, because the firmware's own power-on resume of yesterday's station
// already reports a play start over gabbo, and that is a group re-form trigger.
func TestAlarmHushesTheGroupReForm(t *testing.T) {
	h := newAlarmHarness(t, weekdayDoc())
	if h.s.groupFormHushed() {
		t.Fatal("nothing has fired yet, so nothing should be hushed")
	}
	now := berlinTime(t, 2026, time.September, 7, 6, 30)
	h.at(now)
	h.s.evaluateAlarms(now)
	if !waitFor(func() bool { return h.s.groupFormHushed() }) {
		t.Error("the alarm fired without hushing the group re-form; the whole house would wake")
	}
}

// autoOffDoc is weekdayDoc with a switch-off. Deliberately long: the real
// time.AfterFunc must never fire during a test, or fireSleep would reach the
// unstubbed sleepReadSource against an empty boxHost.
func autoOffDoc(minutes int) alarm.Document {
	d := weekdayDoc()
	d.Alarms[0].AutoOff = minutes
	return d
}

func TestAlarmArmsTheAutoOff(t *testing.T) {
	h := newAlarmHarness(t, autoOffDoc(45))
	now := berlinTime(t, 2026, time.September, 7, 6, 30)
	h.at(now)
	h.s.evaluateAlarms(now)
	if !waitFor(func() bool { return h.s.sleepStatus()["active"] == true }) {
		t.Fatal("the alarm did not arm a switch-off; a forgotten alarm would play all day")
	}
	st := h.s.sleepStatus()
	rem, _ := st["remainingSec"].(int)
	if rem <= 44*60 || rem > 45*60 {
		t.Errorf("remainingSec = %d, want just under 45 minutes", rem)
	}
	// Per speaker, like the group hush: switching off other rooms because THIS
	// alarm timed out would be the worse default.
	if st["group"] != false {
		t.Errorf("the switch-off is armed for the whole group: %v", st["group"])
	}
}

func TestAlarmWithoutAnAutoOffArmsNothing(t *testing.T) {
	h := newAlarmHarness(t, autoOffDoc(0))
	now := berlinTime(t, 2026, time.September, 7, 6, 30)
	h.at(now)
	h.s.evaluateAlarms(now)
	if !waitFor(func() bool { return h.firedCount() == 1 }) {
		t.Fatal("the alarm did not fire at all")
	}
	time.Sleep(150 * time.Millisecond)
	if h.s.sleepStatus()["active"] == true {
		t.Error("zero minutes has to mean never, not a hidden default")
	}
}

// Pins the placement AFTER the status >= 400 return: a preset that could not be
// played must not leave the speaker scheduled to switch itself off.
func TestAlarmThatCouldNotPlayArmsNoAutoOff(t *testing.T) {
	h := newAlarmHarness(t, autoOffDoc(45))
	alarmRecallSlot = func(_ *Server, _ context.Context, _ int) int { return 500 }
	now := berlinTime(t, 2026, time.September, 7, 6, 30)
	h.at(now)
	h.s.evaluateAlarms(now)
	time.Sleep(150 * time.Millisecond)
	if h.s.sleepStatus()["active"] == true {
		t.Error("a refused recall still armed a switch-off")
	}
}
