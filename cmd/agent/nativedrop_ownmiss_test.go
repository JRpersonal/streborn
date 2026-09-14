package main

import (
	"testing"
	"time"
)

// The native-drop watchdog latches every slot on a speaker onto the slower UPnP
// form after four strikes, and a strike is meant to mean "this speaker cannot
// hold a native station it accepted".
//
// Three grouped SoundTouch 10s on 2026-09-13 collected four of them in six
// minutes for something else entirely: the group master was asked for a slot it
// did not have, its own proxy 404ed, and the box left the station each time. The
// speaker was fine. Its keys were downgraded anyway.
func TestOurOwn404IsNotAStrikeAgainstTheSpeaker(t *testing.T) {
	withNoDropsRecorded(t)
	withSlotMiss(t, 6, 200*time.Millisecond, true)

	for i := 0; i < nativeDropBudget+2; i++ {
		noteNativeStreamDropped()
	}

	if n := recordedDrops(); n != 0 {
		t.Errorf("drops = %d, want 0: a station we refused ourselves is not evidence about the speaker", n)
	}
}

// The watchdog still has to work. A speaker that abandons stations with no
// refusal of ours in front of it collects its strikes as before.
func TestADropWithNoRefusalOfOursStillCounts(t *testing.T) {
	withNoDropsRecorded(t)
	withSlotMiss(t, 0, 0, false)

	noteNativeStreamDropped()
	noteNativeStreamDropped()

	if n := recordedDrops(); n != 2 {
		t.Errorf("drops = %d, want 2", n)
	}
}

// An old refusal is not an excuse. The chain in the field was sub-second; a
// speaker that drops a station a minute later did it on its own.
func TestAStaleRefusalDoesNotExcuseADrop(t *testing.T) {
	withNoDropsRecorded(t)
	withSlotMiss(t, 6, nativeDropOwnMissWindow+time.Second, true)

	noteNativeStreamDropped()

	if n := recordedDrops(); n != 1 {
		t.Errorf("drops = %d, want 1", n)
	}
}

func withSlotMiss(t *testing.T, slot int, ago time.Duration, ok bool) {
	t.Helper()
	prev := lastSlotMiss
	lastSlotMiss = func() (int, time.Duration, bool) { return slot, ago, ok }
	t.Cleanup(func() { lastSlotMiss = prev })
}

func withNoDropsRecorded(t *testing.T) {
	t.Helper()
	nativeDrops.Lock()
	prev := nativeDrops.n
	nativeDrops.n = 0
	nativeDrops.Unlock()
	t.Cleanup(func() {
		nativeDrops.Lock()
		nativeDrops.n = prev
		nativeDrops.Unlock()
	})
}

func recordedDrops() int {
	nativeDrops.Lock()
	defer nativeDrops.Unlock()
	return nativeDrops.n
}
