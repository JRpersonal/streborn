package boxlog

import (
	"sync"
	"testing"
	"time"
)

// The measured lines of one standby and one wake, fed at their real spacing,
// yield exactly one event per transition.
func TestPowerEventsOnePerTransition(t *testing.T) {
	r := New(nil, nil, nil)
	var mu sync.Mutex
	var got []PowerEvent
	r.SetPowerHandler(func(ev PowerEvent) {
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
	})
	base := time.Date(2026, 9, 6, 18, 20, 1, 0, time.UTC)
	// Standby: HSM first, scmmond four seconds later, then the sleep stage
	// and the clock line one second after that.
	r.InjectLine(lineStandby, base)
	r.InjectLine(lineLowPower, base.Add(4*time.Second))
	r.InjectLine(lineSleepStage, base.Add(5*time.Second))
	r.InjectLine(lineClock, base.Add(5*time.Second))
	// Wake eleven minutes later: scmmond first, HSM one second after, and
	// the follow-up state change that is not a standby transition at all.
	wake := base.Add(11*time.Minute + 13*time.Second)
	r.InjectLine(lineLowPowerWake, wake)
	r.InjectLine(lineWake, wake.Add(time.Second))
	r.InjectLine(lineSrcChange, wake.Add(2*time.Second))

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("want exactly one standby and one wake event, got %d: %+v", len(got), got)
	}
	if got[0].Kind != PowerStandby || got[0].Source != PowerSourceHSM || got[0].Class != ClassStandby || !got[0].At.Equal(base) {
		t.Fatalf("standby event: %+v", got[0])
	}
	if got[1].Kind != PowerWake || got[1].Source != PowerSourceScmmond || got[1].Class != ClassPowerEvent || !got[1].At.Equal(wake) {
		t.Fatalf("wake event: %+v", got[1])
	}
	snap := r.EventsSnapshot()["powerSignal"].(map[string]any)
	if snap["transitions"] != uint64(2) || snap["duplicates"] != uint64(3) {
		t.Fatalf("snapshot counters: %+v", snap)
	}
	if snap["lastKind"] != "wake" || snap["lastSource"] != "scmmond" {
		t.Fatalf("snapshot last: %+v", snap)
	}
}

// A quick off/on inside the dedupe window is two transitions, not one: the
// direction change always breaks the fold.
func TestPowerEventsQuickOffOn(t *testing.T) {
	r := New(nil, nil, nil)
	var kinds []PowerKind
	r.SetPowerHandler(func(ev PowerEvent) { kinds = append(kinds, ev.Kind) })
	base := time.Date(2026, 9, 6, 18, 20, 1, 0, time.UTC)
	r.InjectLine(lineStandby, base)
	r.InjectLine(lineLowPowerWake, base.Add(2*time.Second))
	r.InjectLine(lineWake, base.Add(3*time.Second))
	r.InjectLine(lineStandby, base.Add(6*time.Second))
	r.InjectLine(lineLowPower, base.Add(9*time.Second))
	want := []PowerKind{PowerStandby, PowerWake, PowerStandby}
	if len(kinds) != len(want) {
		t.Fatalf("want %v, got %v", want, kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("want %v, got %v", want, kinds)
		}
	}
}

// Lines without a direction never produce an event, and a reader without a
// handler still counts the transition.
func TestPowerEventsNoDirection(t *testing.T) {
	for _, line := range []string{lineClock, lineSrcChange, lineWiFiStatus, lineBadURL} {
		l, ok := ParseLine(line)
		if !ok {
			t.Fatalf("parse %q", line)
		}
		class, _ := Classify(l)
		if _, _, ok := powerTransition(Event{Class: class, Message: l.Message}); ok {
			t.Fatalf("%q must not be a transition", line)
		}
	}
	r := New(nil, nil, nil)
	r.InjectLine(lineWake, time.Now())
	snap := r.EventsSnapshot()["powerSignal"].(map[string]any)
	if snap["transitions"] != uint64(1) {
		t.Fatalf("handlerless reader must still count: %+v", snap)
	}
}
