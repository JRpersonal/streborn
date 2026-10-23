package webui

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/alarm"
	"github.com/JRpersonal/streborn/internal/keyactions"
)

// snoozeProbe records what the box-facing snooze seams were asked to do.
type snoozeProbe struct {
	silenced atomic.Int32
	notices  atomic.Int32
	mu       sync.Mutex
	source   string
	playing  bool
	lastWake string
	// keyAt is the last key frame the speaker reported (alarmKeyActivity).
	keyAt time.Time
	// selects are the ContentItems handed to the speaker's /select.
	selects []string
}

func (p *snoozeProbe) selected() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.selects...)
}

func (p *snoozeProbe) keyFrame(at time.Time) {
	p.mu.Lock()
	p.keyAt = at
	p.mu.Unlock()
}

func (p *snoozeProbe) live(src string, playing bool) {
	p.mu.Lock()
	p.source, p.playing = src, playing
	p.mu.Unlock()
}

// newSnoozeHarness is the alarm harness with the snooze seams replaced and a
// snooze minute shrunk to a few milliseconds, so a 9-minute snooze runs out
// in about 45 ms. The speaker reads as on UPNP and stopped unless a test says
// otherwise.
func newSnoozeHarness(t *testing.T, doc alarm.Document) (*alarmHarness, *snoozeProbe) {
	t.Helper()
	h := newAlarmHarness(t, doc)
	p := &snoozeProbe{source: "UPNP"}
	prevLive, prevSilence, prevNotice := alarmLiveState, alarmSilence, showSnoozeNotice
	prevUnit, prevGap, prevSettle := alarmSnoozeUnit, snoozeMinGap, alarmSnoozeSettle
	prevKey, prevWin := alarmKeyActivity, alarmAuxKeyWindow
	prevLeave, prevLeavePoll := alarmAuxLeaveWindow, alarmAuxLeavePoll
	t.Cleanup(func() {
		alarmLiveState, alarmSilence, showSnoozeNotice = prevLive, prevSilence, prevNotice
		alarmSnoozeUnit, snoozeMinGap, alarmSnoozeSettle = prevUnit, prevGap, prevSettle
		alarmKeyActivity, alarmAuxKeyWindow = prevKey, prevWin
		alarmAuxLeaveWindow, alarmAuxLeavePoll = prevLeave, prevLeavePoll
	})
	alarmAuxLeaveWindow, alarmAuxLeavePoll = 150*time.Millisecond, 10*time.Millisecond
	// The speaker's /select: AUX takes the speaker to AUX.
	h.s.selectFn = func(_ context.Context, body string) error {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.selects = append(p.selects, body)
		if strings.Contains(body, `source="AUX"`) {
			p.source = "AUX"
		}
		return nil
	}
	alarmKeyActivity = func(*Server) time.Time {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.keyAt
	}
	// Short, so a "no key frame" case gives up quickly.
	alarmAuxKeyWindow = 150 * time.Millisecond
	// Runs before the seams are restored (cleanups run last-in first-out):
	// no snooze deadline may outlive the test.
	t.Cleanup(func() { h.s.endRing(0, "test over") })
	alarmLiveState = func(*Server, context.Context) (string, bool) {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.source, p.playing
	}
	alarmSilence = func(*Server, context.Context) { p.silenced.Add(1) }
	showSnoozeNotice = func(until string) {
		p.mu.Lock()
		p.lastWake = until
		p.mu.Unlock()
		p.notices.Add(1)
	}
	alarmSnoozeUnit = 5 * time.Millisecond
	snoozeMinGap = 0
	// No settle window unless a test is about it: a STANDBY right after the
	// snooze then reads as the power key.
	alarmSnoozeSettle = 0
	return h, p
}

// ring fires the weekday alarm and waits until it is ringing.
func (h *alarmHarness) ring(t *testing.T) {
	t.Helper()
	now := berlinTime(t, 2026, time.September, 7, 6, 30) // a Monday
	h.at(now)
	h.s.evaluateAlarms(now)
	if !waitFor(func() bool { v := h.s.ringView(); return v != nil && v.Ringing }) {
		t.Fatalf("the alarm never started ringing: %+v", h.s.ringView())
	}
}

func (h *alarmHarness) snoozed() bool {
	v := h.s.ringView()
	return v != nil && v.SnoozedUntil != ""
}

func withAuxSnooze(t *testing.T, s *Server) {
	t.Helper()
	ka, err := keyactions.Load(filepath.Join(t.TempDir(), "key-actions.json"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := ka.Set(keyactions.Document{Keys: map[string]string{keyactions.KeyAux: keyactions.ActionSnooze}}, nil); err != nil {
		t.Fatal(err)
	}
	s.keyActions = ka
}

func TestRingEndReason(t *testing.T) {
	since := time.Date(2026, 9, 7, 6, 30, 0, 0, time.UTC)
	base := ringFacts{since: since, ringGen: 4, curGen: 4, maxRing: time.Hour, now: since.Add(time.Minute)}
	tests := []struct {
		name    string
		edit    func(f *ringFacts)
		ringing bool
	}{
		{"nothing happened", func(*ringFacts) {}, true},
		{"another play", func(f *ringFacts) { f.curGen = 5 }, false},
		{"power key after the fire", func(f *ringFacts) { f.standbyStop = since.Add(time.Second) }, false},
		{"power-off before the fire", func(f *ringFacts) { f.standbyStop = since.Add(-time.Minute) }, true},
		{"stop in the app after the fire", func(f *ringFacts) { f.explicitStop = since.Add(time.Second) }, false},
		{"stop in the app before the fire", func(f *ringFacts) { f.explicitStop = since.Add(-time.Second) }, true},
		{"past the switch-off", func(f *ringFacts) { f.now = since.Add(61 * time.Minute) }, false},
		{"no limit at a snooze deadline", func(f *ringFacts) { f.maxRing = 0; f.now = since.Add(5 * time.Hour) }, true},
	}
	for _, tt := range tests {
		f := base
		tt.edit(&f)
		if got := ringEndReason(f) == ""; got != tt.ringing {
			t.Errorf("%s: ringing = %v, want %v (%q)", tt.name, got, tt.ringing, ringEndReason(f))
		}
	}
}

// The core of discussion #1415: a press while the alarm rings silences it,
// the speaker stays on, and the same alarm rings again after its snooze time.
func TestSnoozeWhileRingingRingsAgainAfterTheLength(t *testing.T) {
	h, p := newSnoozeHarness(t, weekdayDoc())
	h.ring(t)
	h.s.AlarmSnoozePress(keyactions.KeyThumbsUp)
	if !h.snoozed() {
		t.Fatalf("a press while ringing did not snooze: %+v", h.s.ringView())
	}
	if p.silenced.Load() != 1 {
		t.Errorf("playback was stopped %d times, want once", p.silenced.Load())
	}
	if p.notices.Load() != 1 {
		t.Error("the display was not told about the snooze")
	}
	if h.s.sleepStatus()["active"] == true {
		t.Error("a snooze must not switch the speaker off")
	}
	if !waitFor(func() bool { return h.firedCount() == 2 }) {
		t.Fatalf("the alarm did not ring again after the snooze: %d recalls", h.firedCount())
	}
	// Same preset, same level, through the normal wake.
	if !waitFor(func() bool { v := h.s.ringView(); return v != nil && v.Ringing }) {
		t.Fatalf("not ringing after the re-fire: %+v", h.s.ringView())
	}
	if v := h.s.ringView(); v.Snoozes != 1 || v.ID != "wake" {
		t.Errorf("ring view after the re-fire: %+v", v)
	}
	var vols []int
	for len(h.volumes) > 0 {
		vols = append(vols, <-h.volumes)
	}
	if len(vols) != 2 || vols[1] != 25 {
		t.Errorf("the re-fire woke at %v, want the alarm's 25 twice", vols)
	}
}

// Old alarms snooze for nine minutes; the length on the alarm wins otherwise.
func TestSnoozeUsesTheAlarmsLength(t *testing.T) {
	doc := weekdayDoc()
	doc.Alarms[0].Snooze = 15
	h, _ := newSnoozeHarness(t, doc)
	alarmSnoozeUnit = time.Minute // only the deadline is read, nothing runs out
	h.ring(t)
	before := time.Now()
	h.s.AlarmSnoozePress(keyactions.KeyThumbsUp)
	h.s.ring.mu.Lock()
	until := h.s.ring.snoozeUntil
	h.s.ring.mu.Unlock()
	if d := until.Sub(before); d < 15*time.Minute || d > 15*time.Minute+5*time.Second {
		t.Errorf("snoozed for %v, want 15 minutes", d)
	}
}

// Unlimited snoozes: every re-fire can be snoozed again.
func TestSnoozeIsUnlimited(t *testing.T) {
	h, p := newSnoozeHarness(t, weekdayDoc())
	h.ring(t)
	for i := 1; i <= 3; i++ {
		h.s.AlarmSnoozePress(keyactions.KeyThumbsUp)
		if !h.snoozed() {
			t.Fatalf("snooze %d did not take", i)
		}
		if !waitFor(func() bool { return h.firedCount() == int64(i+1) }) {
			t.Fatalf("no re-fire after snooze %d", i)
		}
		if !waitFor(func() bool { v := h.s.ringView(); return v != nil && v.Ringing }) {
			t.Fatalf("not ringing after re-fire %d", i)
		}
	}
	if p.silenced.Load() != 3 {
		t.Errorf("stopped %d times for three snoozes", p.silenced.Load())
	}
}

// Outside a ringing alarm the key does nothing at all.
func TestSnoozePressWithoutAnAlarmDoesNothing(t *testing.T) {
	h, p := newSnoozeHarness(t, weekdayDoc())
	h.s.AlarmSnoozePress(keyactions.KeyThumbsDown)
	if h.s.ringView() != nil || p.silenced.Load() != 0 || p.notices.Load() != 0 {
		t.Errorf("a press without an alarm did something: ring=%+v stops=%d notices=%d",
			h.s.ringView(), p.silenced.Load(), p.notices.Load())
	}
}

// While the alarm is still waking the speaker, a press must not snooze a
// recall that is on its way.
func TestSnoozePressWhileStartingIsIgnored(t *testing.T) {
	h, p := newSnoozeHarness(t, weekdayDoc())
	h.s.ringStart(weekdayDoc().Alarms[0], time.Now(), time.Now())
	h.s.AlarmSnoozePress(keyactions.KeyThumbsUp)
	if h.snoozed() || p.silenced.Load() != 0 {
		t.Error("a press while the alarm was starting snoozed it")
	}
}

func TestSnoozeCancelledByStopInTheApp(t *testing.T) {
	h, _ := newSnoozeHarness(t, weekdayDoc())
	h.ring(t)
	h.s.AlarmSnoozePress(keyactions.KeyThumbsUp)
	h.s.NoteExplicitStop() // the Stop button on the phone or in the app
	if h.s.ringView() != nil {
		t.Fatal("a stop in the app left the snooze pending")
	}
	time.Sleep(150 * time.Millisecond)
	if h.firedCount() != 1 {
		t.Errorf("the alarm rang again after a stop: %d recalls", h.firedCount())
	}
}

func TestSnoozeCancelledByThePowerKey(t *testing.T) {
	h, _ := newSnoozeHarness(t, weekdayDoc())
	h.ring(t)
	h.s.AlarmSnoozePress(keyactions.KeyThumbsUp)
	h.s.AlarmSourceChanged("UPNP", "STANDBY")
	if h.s.ringView() != nil {
		t.Fatal("switching the speaker off left the snooze pending")
	}
	time.Sleep(150 * time.Millisecond)
	if h.firedCount() != 1 {
		t.Errorf("the alarm switched a speaker back on that was switched off: %d recalls", h.firedCount())
	}
}

// Firmware that powers STR's source off when its transport stops drops to
// STANDBY right after the snooze's own stop. That is not the power key: the
// snooze stays, and the alarm wakes the speaker at the end of it.
func TestSnoozeSurvivesItsOwnStopDroppingToStandby(t *testing.T) {
	h, p := newSnoozeHarness(t, weekdayDoc())
	alarmSnoozeSettle = time.Minute
	h.ring(t)
	h.s.AlarmSnoozePress(keyactions.KeyThumbsUp)
	p.live("STANDBY", false)
	h.s.AlarmSourceChanged("UPNP", "STANDBY")
	if !h.snoozed() {
		t.Fatal("the snooze's own stop settling into standby cancelled the snooze")
	}
	if !waitFor(func() bool { return h.firedCount() == 2 }) {
		t.Fatal("the alarm did not wake the speaker from standby after the snooze")
	}
	if h.wakes.Load() != 2 {
		t.Errorf("the re-fire did not go through the wake: %d wakes", h.wakes.Load())
	}
}

func TestSnoozeCancelledByAnotherPlay(t *testing.T) {
	h, _ := newSnoozeHarness(t, weekdayDoc())
	h.ring(t)
	h.s.AlarmSnoozePress(keyactions.KeyThumbsUp)
	h.s.bumpRecallGen() // the user started a station from the phone
	time.Sleep(150 * time.Millisecond)
	if h.firedCount() != 1 {
		t.Errorf("the alarm cut into a play the user started: %d recalls", h.firedCount())
	}
	if h.s.ringView() != nil {
		t.Error("the ring outlived a play of something else")
	}
}

// Something the firmware plays by itself (a Bose preset, Bluetooth) never
// moves the recall generation, so the deadline reads the speaker once.
func TestSnoozeDeadlineStandsDownWhenSomethingElsePlays(t *testing.T) {
	h, p := newSnoozeHarness(t, weekdayDoc())
	h.ring(t)
	h.s.AlarmSnoozePress(keyactions.KeyThumbsUp)
	p.live("BLUETOOTH", true)
	time.Sleep(150 * time.Millisecond)
	if h.firedCount() != 1 {
		t.Errorf("the alarm cut into Bluetooth playback: %d recalls", h.firedCount())
	}
}

// A press while the speaker plays another input is not a press on the alarm.
func TestSnoozePressOnAnotherInputDoesNothing(t *testing.T) {
	h, p := newSnoozeHarness(t, weekdayDoc())
	h.ring(t)
	p.live("BLUETOOTH", true)
	h.s.AlarmSnoozePress(keyactions.KeyThumbsUp)
	if h.snoozed() || p.silenced.Load() != 0 {
		t.Error("a press on Bluetooth snoozed the alarm")
	}
}

// AUX bound to snooze: while the alarm rings the firmware's own switch to AUX
// is the silence, so STR sends no stop on top, and the alarm rings again.
func TestAuxSnoozesARingingAlarm(t *testing.T) {
	h, p := newSnoozeHarness(t, weekdayDoc())
	withAuxSnooze(t, h.s)
	h.ring(t)
	p.live("AUX", true) // the firmware is on AUX now
	h.s.AlarmSourceChanged("UPNP", "AUX")
	if !waitFor(h.snoozed) {
		t.Fatalf("AUX while ringing did not snooze: %+v", h.s.ringView())
	}
	if p.silenced.Load() != 0 {
		t.Error("a STOP went out on top of the firmware's switch to AUX")
	}
	if !waitFor(func() bool { return h.firedCount() == 2 }) {
		t.Fatal("the alarm did not ring again after an AUX snooze, although the speaker sat on AUX")
	}
}

// Outside a ringing alarm a snooze-bound AUX is just AUX: nothing extra.
func TestAuxWithoutAnAlarmDoesNothingExtra(t *testing.T) {
	h, p := newSnoozeHarness(t, weekdayDoc())
	withAuxSnooze(t, h.s)
	h.s.AlarmSourceChanged("UPNP", "AUX")
	time.Sleep(100 * time.Millisecond)
	if h.s.ringView() != nil || p.silenced.Load() != 0 || p.notices.Load() != 0 {
		t.Error("AUX outside an alarm did something on top of switching the input")
	}
}

// The field case (ST10, 27.0.6): with nothing plugged into AUX the AUX key
// lands on BLUETOOTH, and the key frame arrives 21 ms after the source change.
// That is the AUX press: snooze, no stop on top, and the alarm comes back from
// Bluetooth at the end of the snooze.
func TestAuxLandingOnBluetoothWithAKeyFrameSnoozes(t *testing.T) {
	h, p := newSnoozeHarness(t, weekdayDoc())
	withAuxSnooze(t, h.s)
	h.ring(t)
	p.live("BLUETOOTH", false)
	h.s.AlarmSourceChanged("LOCAL_INTERNET_RADIO", "BLUETOOTH")
	time.Sleep(20 * time.Millisecond)
	p.keyFrame(time.Now()) // the bare userActivityUpdate, just after
	if !waitFor(h.snoozed) {
		t.Fatalf("AUX landing on Bluetooth with a key frame did not snooze: %+v", h.s.ringView())
	}
	if p.silenced.Load() != 0 {
		t.Error("a STOP went out on top of the firmware's own input switch")
	}
	if !waitFor(func() bool { return h.firedCount() == 2 }) {
		t.Fatal("the alarm did not come back from Bluetooth after the snooze")
	}
}

// Left on Bluetooth for the whole snooze, the speaker would stay open for
// pairing and anybody nearby could connect a phone. The snooze moves it on to
// AUX, which is just as silent.
func TestAuxSnoozeOnBluetoothMovesToAux(t *testing.T) {
	h, p := newSnoozeHarness(t, weekdayDoc())
	withAuxSnooze(t, h.s)
	alarmSnoozeUnit = time.Minute // keep the re-fire out of this test
	h.ring(t)
	p.live("BLUETOOTH", false)
	p.keyFrame(time.Now())
	h.s.AlarmSourceChanged("LOCAL_INTERNET_RADIO", "BLUETOOTH")
	if !waitFor(h.snoozed) {
		t.Fatal("no snooze")
	}
	if !waitFor(func() bool { return len(p.selected()) == 1 }) {
		t.Fatalf("the snooze left the speaker on Bluetooth: selects=%q", p.selected())
	}
	if got := p.selected()[0]; !strings.Contains(got, `source="AUX"`) {
		t.Errorf("select after the snooze = %q, want AUX", got)
	}
	h.s.AlarmSourceChanged("BLUETOOTH", "AUX") // the firmware follows the select
	time.Sleep(100 * time.Millisecond)
	if v := h.s.ringView(); v == nil || v.Snoozes != 1 || v.SnoozedUntil == "" {
		t.Errorf("the step to AUX counted as another press: %+v", v)
	}
}

// A speaker that snoozed straight onto AUX (something plugged in, or a
// firmware that does not skip the empty input) is left alone.
func TestAuxSnoozeOnAuxSelectsNothing(t *testing.T) {
	h, p := newSnoozeHarness(t, weekdayDoc())
	withAuxSnooze(t, h.s)
	alarmSnoozeUnit = time.Minute
	h.ring(t)
	p.live("AUX", true)
	h.s.AlarmSourceChanged("UPNP", "AUX")
	if !waitFor(h.snoozed) {
		t.Fatal("no snooze")
	}
	time.Sleep(200 * time.Millisecond)
	if got := p.selected(); len(got) != 0 {
		t.Errorf("a speaker already on AUX got a select: %q", got)
	}
}

// A key frame just BEFORE the switch counts too.
func TestBluetoothKeyFrameBeforeTheSwitchCounts(t *testing.T) {
	h, p := newSnoozeHarness(t, weekdayDoc())
	withAuxSnooze(t, h.s)
	h.ring(t)
	p.keyFrame(time.Now().Add(-50 * time.Millisecond))
	h.s.AlarmSourceChanged("UPNP", "BLUETOOTH")
	if !waitFor(h.snoozed) {
		t.Fatal("a key frame just before the Bluetooth switch was not taken as the AUX press")
	}
}

// A phone connecting by itself switches to Bluetooth without any key frame:
// never a snooze.
func TestBluetoothWithoutAKeyFrameDoesNotSnooze(t *testing.T) {
	h, p := newSnoozeHarness(t, weekdayDoc())
	withAuxSnooze(t, h.s)
	h.ring(t)
	p.keyFrame(time.Now().Add(-10 * time.Second)) // an old press, long before
	h.s.AlarmSourceChanged("UPNP", "BLUETOOTH")
	time.Sleep(300 * time.Millisecond)
	if h.snoozed() {
		t.Error("a Bluetooth switch without a key press snoozed the alarm")
	}
}

// Outside a ringing alarm, the AUX key landing on Bluetooth does nothing extra.
func TestBluetoothWithAKeyFrameOutsideAnAlarmDoesNothing(t *testing.T) {
	h, p := newSnoozeHarness(t, weekdayDoc())
	withAuxSnooze(t, h.s)
	p.keyFrame(time.Now())
	h.s.AlarmSourceChanged("UPNP", "BLUETOOTH")
	time.Sleep(200 * time.Millisecond)
	if h.s.ringView() != nil || p.silenced.Load() != 0 || p.notices.Load() != 0 {
		t.Error("the AUX key landing on Bluetooth did something outside an alarm")
	}
}

// Without the snooze action on AUX, a Bluetooth switch with a key frame is
// just the user choosing Bluetooth.
func TestBluetoothWithAKeyFrameNeedsAuxBoundToSnooze(t *testing.T) {
	h, p := newSnoozeHarness(t, weekdayDoc())
	h.ring(t)
	p.keyFrame(time.Now())
	h.s.AlarmSourceChanged("UPNP", "BLUETOOTH")
	time.Sleep(200 * time.Millisecond)
	if h.snoozed() {
		t.Error("Bluetooth snoozed although AUX carries no snooze")
	}
}

// Snoozed onto Bluetooth, then a phone actually streams: that is the user's
// music, and the alarm stands down.
func TestBluetoothSnoozeStandsDownWhenAPhoneStreams(t *testing.T) {
	h, p := newSnoozeHarness(t, weekdayDoc())
	withAuxSnooze(t, h.s)
	h.ring(t)
	p.keyFrame(time.Now())
	h.s.AlarmSourceChanged("UPNP", "BLUETOOTH")
	if !waitFor(h.snoozed) {
		t.Fatal("no snooze")
	}
	p.live("BLUETOOTH", true)
	time.Sleep(150 * time.Millisecond)
	if h.firedCount() != 1 {
		t.Errorf("the alarm cut into a phone streaming over Bluetooth: %d recalls", h.firedCount())
	}
}

// Where the key trace names AUX_INPUT, that snoozes; the source change of the
// same press is a repeat and changes nothing.
func TestAuxKeyTraceAndSourceChangeAreOnePress(t *testing.T) {
	h, p := newSnoozeHarness(t, weekdayDoc())
	withAuxSnooze(t, h.s)
	alarmSnoozeUnit = time.Minute // keep the re-fire out of this test
	h.ring(t)
	h.s.AlarmAuxKeyPress()
	if !h.snoozed() {
		t.Fatal("AUX_INPUT from the key trace did not snooze")
	}
	h.s.AlarmSourceChanged("UPNP", "AUX")
	time.Sleep(100 * time.Millisecond)
	if p.notices.Load() != 1 || p.silenced.Load() != 0 {
		t.Errorf("one AUX press acted twice: notices=%d stops=%d", p.notices.Load(), p.silenced.Load())
	}
	if v := h.s.ringView(); v == nil || v.Snoozes != 1 {
		t.Errorf("ring after one AUX press: %+v", v)
	}
}

// An AUX that does NOT carry snooze never snoozes; the switch to AUX simply
// ends the alarm's claim on the key at the next press.
func TestUnboundAuxDoesNotSnooze(t *testing.T) {
	h, p := newSnoozeHarness(t, weekdayDoc())
	h.ring(t)
	h.s.AlarmSourceChanged("UPNP", "AUX")
	time.Sleep(100 * time.Millisecond)
	if h.snoozed() || p.silenced.Load() != 0 {
		t.Error("an AUX without the snooze action snoozed the alarm")
	}
}

// The alarm's switch-off counts from the latest ring: the snooze cancels the
// one the alarm armed, the re-fire arms it again.
func TestSnoozePausesTheAlarmsSwitchOff(t *testing.T) {
	h, _ := newSnoozeHarness(t, autoOffDoc(45))
	h.ring(t)
	if !waitFor(func() bool { return h.s.sleepStatus()["active"] == true }) {
		t.Fatal("the alarm armed no switch-off")
	}
	h.s.AlarmSnoozePress(keyactions.KeyThumbsUp)
	if h.s.sleepStatus()["active"] == true {
		t.Error("the switch-off kept counting through the snooze")
	}
	if !waitFor(func() bool { return h.firedCount() == 2 && h.s.sleepStatus()["active"] == true }) {
		t.Error("the re-fire did not arm the switch-off again")
	}
}

// A sleep timer the user set themselves is theirs: a snooze leaves it alone.
func TestSnoozeKeepsTheUsersOwnSleepTimer(t *testing.T) {
	h, _ := newSnoozeHarness(t, autoOffDoc(45))
	h.ring(t)
	if !waitFor(func() bool { return h.s.sleepStatus()["active"] == true }) {
		t.Fatal("the alarm armed no switch-off")
	}
	h.s.armSleep(50*time.Minute, false) // set on the phone's sleep card
	alarmSnoozeUnit = time.Minute       // keep the re-fire out of this test
	h.s.AlarmSnoozePress(keyactions.KeyThumbsUp)
	if h.s.sleepStatus()["active"] != true {
		t.Error("the snooze cancelled a sleep timer the user set")
	}
	h.s.cancelSleep("test over")
}

// The fire's own verify must never re-push a snoozed alarm: that would ring
// it again long before the snooze is over.
func TestSnoozeStandsTheFiresRetryDown(t *testing.T) {
	h, _ := newSnoozeHarness(t, weekdayDoc())
	alarmPlayingCheck = func(*Server, int, time.Time) bool { return false } // would retry
	alarmVerifyDelay = 150 * time.Millisecond
	alarmSnoozeUnit = time.Minute // keep the snooze's own re-fire out of this test
	h.ring(t)
	h.s.AlarmSnoozePress(keyactions.KeyThumbsUp)
	time.Sleep(400 * time.Millisecond)
	if got := h.firedCount(); got != 1 {
		t.Errorf("the fire's retry rang a snoozed alarm: %d recalls", got)
	}
	last, ok := h.s.alarmState.LastOutcome()
	if !ok || last.Detail != "snoozed" {
		t.Errorf("outcome %+v, want the stand-down recorded as snoozed", last)
	}
}

// The status block shows a ringing and a snoozed alarm.
func TestAlarmStatusShowsTheRing(t *testing.T) {
	h, _ := newSnoozeHarness(t, weekdayDoc())
	if h.s.alarmView().Status.Ring != nil {
		t.Fatal("a ring is reported before anything fired")
	}
	h.ring(t)
	if r := h.s.alarmView().Status.Ring; r == nil || !r.Ringing || r.SnoozedUntil != "" {
		t.Errorf("ringing alarm reported as %+v", r)
	}
	alarmSnoozeUnit = time.Minute
	h.s.AlarmSnoozePress(keyactions.KeyThumbsUp)
	if r := h.s.alarmView().Status.Ring; r == nil || r.Ringing || r.SnoozedUntil == "" {
		t.Errorf("snoozed alarm reported as %+v", r)
	}
}
