package boxlog

import (
	"testing"
	"time"
)

// PhysicalPressesSince is what the double-press detector asks inside a group,
// where one press arrives as two selection frames (#1110): it counts a person's
// presses of one key, and nothing else.
func TestPhysicalPressesSinceCountsOnlyAPersonsPressesOfThatKey(t *testing.T) {
	r := New(nil, nil, nil)
	base := time.Now()
	preset1 := KeyNumber("PRESET_1")
	inject := func(key int, p Producer, s State, o Origin, at time.Duration) {
		r.Inject(KeyEvent{Key: key, Name: KeyName(key), Producer: p, State: s, Origin: o, At: base.Add(at)})
	}
	inject(preset1, ProducerConsole, StatePressed, OriginTrace, 0)
	inject(preset1, ProducerConsole, StateReleased, OriginTrace, 100*time.Millisecond)
	inject(preset1, ProducerConsole, StatePressed, OriginTrace, 250*time.Millisecond)
	inject(preset1, ProducerGabbo, StatePressed, OriginTrace, 300*time.Millisecond) // a network key
	inject(KeyNumber("PRESET_2"), ProducerIRRemote, StatePressed, OriginTrace, 320*time.Millisecond)

	if n := r.PhysicalPressesSince("PRESET_1", base.Add(-time.Second)); n != 2 {
		t.Fatalf("two physical presses of key 1, counted %d", n)
	}
	if n := r.PhysicalPressesSince("PRESET_1", base.Add(200*time.Millisecond)); n != 1 {
		t.Fatalf("one press since 200 ms, counted %d", n)
	}
	if n := r.PhysicalPressesSince("PRESET_3", base.Add(-time.Second)); n != 0 {
		t.Fatalf("key 3 was never pressed, counted %d", n)
	}
}

// The sm2 chassis reports a press only through the analytics line, one per
// press; two quick presses are two lines, both counted.
func TestPhysicalPressesSinceCountsTheAnalyticsLine(t *testing.T) {
	r := New(nil, nil, nil)
	base := time.Now()
	key := KeyNumber("PRESET_4")
	for i, at := range []time.Duration{0, 280 * time.Millisecond} {
		r.Inject(KeyEvent{Key: key, Name: "PRESET_4", Producer: ProducerConsole, State: StatePressed, Origin: OriginStats, At: base.Add(at)})
		if n := r.PhysicalPressesSince("PRESET_4", base.Add(-time.Second)); n != i+1 {
			t.Fatalf("after %d analytics lines counted %d", i+1, n)
		}
	}
}
