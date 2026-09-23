package main

import (
	"testing"
	"time"
)

// The native-preset latch was written as "off for the rest of this run", on the
// assumption that the UPnP fallback keeps the hardware keys working. Two field
// bundles on 2026-09-23 showed what happens when that assumption fails: one
// speaker answered 1036 to every UPnP recall, another 404 to every UPnP push
// since install day. Both owners lost all six keys, one for two days and one for
// a week, on speakers whose native form had been fine until the latch.
//
// So the latch is a timeout now. These tests pin that, and pin the drop counter
// ageing, because four drops spread over a week used to read exactly like four
// drops in a minute.

func withNativeClock(t *testing.T, at *time.Time) {
	t.Helper()
	prev := nativeNow
	nativeNow = func() time.Time { return *at }
	t.Cleanup(func() { nativeNow = prev })
}

func withCleanLatch(t *testing.T) {
	t.Helper()
	nativeReady.Lock()
	// Field by field: the struct embeds a Mutex, and copying it wholesale is
	// what go vet rightly refuses.
	prevDisabled, prevWhy, prevFailures := nativeReady.disabled, nativeReady.why, nativeReady.failures
	prevAt, prevChecked, prevOK := nativeReady.disabledAt, nativeReady.checked, nativeReady.ok
	nativeReady.disabled, nativeReady.why, nativeReady.failures = false, "", 0
	nativeReady.disabledAt, nativeReady.checked = time.Time{}, time.Time{}
	nativeReady.Unlock()
	t.Cleanup(func() {
		nativeReady.Lock()
		nativeReady.disabled, nativeReady.why, nativeReady.failures = prevDisabled, prevWhy, prevFailures
		nativeReady.disabledAt, nativeReady.checked, nativeReady.ok = prevAt, prevChecked, prevOK
		nativeReady.Unlock()
	})
}

func TestTheLatchComesOffAfterItsCoolOff(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	withNativeClock(t, &now)
	withCleanLatch(t)
	withNoDropsRecorded(t)

	forceDisableNativePresets("box repeatedly abandoned a native station it had accepted")
	if off, _ := nativePresetsDisabled(); !off {
		t.Fatal("the latch did not go on")
	}

	// Still inside the cool-off: it must hold, or a speaker that really does
	// drop its stations would thrash.
	now = now.Add(nativeLatchCoolOff - time.Minute)
	if off, why := nativePresetsDisabled(); !off {
		t.Fatalf("latch lifted early (why=%q); it must stand for the whole cool-off", why)
	}

	now = now.Add(2 * time.Minute)
	off, why := nativePresetsDisabled()
	if off {
		t.Fatalf("latch still on %v after it went on, so the keys stay dead until a reboot (why=%q)",
			nativeLatchCoolOff+time.Minute, why)
	}
	if why != "" {
		t.Errorf("latch reason survived the expiry: %q", why)
	}
}

// Coming off a latch has to mean a clean slate. A speaker that kept its old
// counters would latch again on its very next stumble and the hour would have
// bought nothing.
func TestComingOffTheLatchGivesTheSpeakerAFreshBudget(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	withNativeClock(t, &now)
	withCleanLatch(t)
	withNoDropsRecorded(t)
	withSlotMiss(t, 0, 0, false) // no proxy refusal of our own in the way

	for range nativeDropBudget {
		noteNativeStreamDropped()
	}
	if off, _ := nativePresetsDisabled(); !off {
		t.Fatalf("%d drops did not latch", nativeDropBudget)
	}

	now = now.Add(nativeLatchCoolOff + time.Minute)
	if off, _ := nativePresetsDisabled(); off {
		t.Fatal("latch did not expire")
	}
	if n := recordedDrops(); n != 0 {
		t.Errorf("drop history = %d after the latch lifted, want 0", n)
	}
	nativeReady.Lock()
	fails, checked := nativeReady.failures, nativeReady.checked
	nativeReady.Unlock()
	if fails != 0 {
		t.Errorf("consecutive write failures = %d after the latch lifted, want 0", fails)
	}
	if !checked.IsZero() {
		t.Error("the cached source-registration verdict survived: the next sweep would trust a reading taken while the latch was on")
	}
}

// A speaker that genuinely keeps dropping stations must still earn its latch
// back, or the hour would simply turn the protection off.
func TestASpeakerThatKeepsDroppingLatchesAgain(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	withNativeClock(t, &now)
	withCleanLatch(t)
	withNoDropsRecorded(t)
	withSlotMiss(t, 0, 0, false)

	for range nativeDropBudget {
		noteNativeStreamDropped()
	}
	now = now.Add(nativeLatchCoolOff + time.Minute)
	if off, _ := nativePresetsDisabled(); off {
		t.Fatal("latch did not expire")
	}
	for range nativeDropBudget {
		noteNativeStreamDropped()
	}
	if off, _ := nativePresetsDisabled(); !off {
		t.Fatal("a speaker that dropped four more stations did not latch again")
	}
}

// Four drops spread over a week are not a storm. The counter only ever grew, so
// they latched the speaker exactly as hard as four in a minute.
func TestDropsFallOutOfTheWindow(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	withNativeClock(t, &now)
	withCleanLatch(t)
	withNoDropsRecorded(t)
	withSlotMiss(t, 0, 0, false)

	for range nativeDropBudget * 2 {
		noteNativeStreamDropped()
		now = now.Add(nativeDropWindow / 2) // every drop ages out before the next but one
	}
	if off, why := nativePresetsDisabled(); off {
		t.Fatalf("latched on drops spread wider than the window (why=%q)", why)
	}
	if n := recordedDrops(); n >= nativeDropBudget {
		t.Errorf("drop history = %d, want fewer than the budget %d: old drops must fall out",
			n, nativeDropBudget)
	}
}

// And a real storm inside the window still latches, which is the case the
// budget was measured against (twelve presses, twelve drops, SoundTouch 20).
func TestAStormInsideTheWindowStillLatches(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	withNativeClock(t, &now)
	withCleanLatch(t)
	withNoDropsRecorded(t)
	withSlotMiss(t, 0, 0, false)

	for range nativeDropBudget {
		noteNativeStreamDropped()
		now = now.Add(5 * time.Second)
	}
	if off, _ := nativePresetsDisabled(); !off {
		t.Fatal("a drop storm inside the window did not latch")
	}
}
