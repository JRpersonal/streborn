package main

import (
	"testing"
	"time"
)

// The 2026-08-22 ST20 case: the user switched the speaker on, STR resumed his
// station over UPnP and it was audibly playing, and six seconds later the
// power-wake re-sync wrote six preset slots. The source flipped
// UPNP -> LOCAL_INTERNET_RADIO -> STANDBY and the music was gone. Only the
// 20-minute insurance pass was ever gated; every forced asker wrote regardless.

func TestForcedWriteHoldWaitsForPlayback(t *testing.T) {
	now := time.Now()

	hold, ceiling := forcedWriteHold("UPNP", true, true, true, time.Time{}, now)
	if !hold || ceiling {
		t.Fatalf("a playing speaker must hold the first time: hold=%v ceiling=%v", hold, ceiling)
	}
}

func TestForcedWriteHoldReleasesWhenPlaybackStops(t *testing.T) {
	now := time.Now()
	held := now.Add(-time.Minute)

	// Still inside the ceiling, but nothing is playing any more: write now.
	if hold, ceiling := forcedWriteHold("UPNP", false, true, true, held, now); hold || ceiling {
		t.Fatalf("silence must release the hold: hold=%v ceiling=%v", hold, ceiling)
	}
}

func TestForcedWriteHoldGivesUpAtTheCeiling(t *testing.T) {
	now := time.Now()

	// Just inside: still holding.
	held := now.Add(-forcedPlayHoldCeiling + time.Second)
	if hold, ceiling := forcedWriteHold("UPNP", true, true, true, held, now); !hold || ceiling {
		t.Fatalf("inside the ceiling it must keep holding: hold=%v ceiling=%v", hold, ceiling)
	}

	// At the ceiling: the keys matter more than one interrupted stream.
	held = now.Add(-forcedPlayHoldCeiling)
	hold, ceiling := forcedWriteHold("UPNP", true, true, true, held, now)
	if hold || !ceiling {
		t.Fatalf("at the ceiling it must run anyway: hold=%v ceiling=%v", hold, ceiling)
	}
}

func TestForcedWriteHoldNeverHoldsTheFirstRegistration(t *testing.T) {
	// everFullDone false is the first full pass after the agent started. An
	// agent that starts while the box is playing must still register the
	// hardware keys, or they stay dead for the whole session (#4).
	if hold, ceiling := forcedWriteHold("UPNP", true, true, false, time.Time{}, time.Now()); hold || ceiling {
		t.Fatalf("the first registration must never be held: hold=%v ceiling=%v", hold, ceiling)
	}
}

func TestForcedWriteHoldTreatsAnUnreadableBoxAsWritable(t *testing.T) {
	// A now_playing read that failed says nothing about playback. Holding on
	// "unknown" would let one unreachable probe defer the write for five
	// minutes, every time, on a box that is merely slow to answer.
	if hold, ceiling := forcedWriteHold("", false, false, true, time.Time{}, time.Now()); hold || ceiling {
		t.Fatalf("an unknown play state must not hold: hold=%v ceiling=%v", hold, ceiling)
	}
	// Even if the (meaningless) playing flag is set, unknown wins.
	if hold, _ := forcedWriteHold("", true, false, true, time.Time{}, time.Now()); hold {
		t.Fatal("playing=true with playKnown=false must not hold")
	}
}

// TestForcedWriteHoldCoversAPairingSession is #961. His ledger reads
// addpreset@BLUETOOTH 3: STR registered hardware keys while the speaker was
// being paired, and the source flipped BLUETOOTH -> LOCAL_INTERNET_RADIO ->
// BLUETOOTH four times in six seconds while his PC was looking for it.
//
// The play test alone could never catch that. A box in pairing mode reports
// playStatus INVALID, which is neither PLAY_STATE nor BUFFERING_STATE, so
// playing is false and the write went straight through.
func TestForcedWriteHoldCoversAPairingSession(t *testing.T) {
	now := time.Now()

	hold, ceiling := forcedWriteHold("BLUETOOTH", false, true, true, time.Time{}, now)
	if !hold || ceiling {
		t.Fatalf("a box being paired must hold: hold=%v ceiling=%v", hold, ceiling)
	}
	if got := forcedHoldReason("BLUETOOTH", false, true); got != "user-chosen source" {
		t.Errorf("reason = %q, want the source, so a bundle says what was protected", got)
	}

	// Bounded like every other hold: the keys still have to be registered.
	held := now.Add(-forcedPlayHoldCeiling)
	if hold, ceiling := forcedWriteHold("BLUETOOTH", false, true, true, held, now); hold || !ceiling {
		t.Fatalf("the ceiling must release a source hold too: hold=%v ceiling=%v", hold, ceiling)
	}
}

// A silent box on a source nobody picked is exactly when the keys should be
// written, and holding there would delay every wake by the ceiling.
func TestForcedWriteHoldWritesToAnIdleBox(t *testing.T) {
	for _, src := range []string{"STANDBY", "UPNP", "INVALID_SOURCE", ""} {
		if hold, ceiling := forcedWriteHold(src, false, true, true, time.Time{}, time.Now()); hold || ceiling {
			t.Errorf("src=%q must stay writable: hold=%v ceiling=%v", src, hold, ceiling)
		}
		if forcedWriteBusy(src, false, true) {
			t.Errorf("src=%q must not count as busy", src)
		}
	}
	for _, src := range []string{"BLUETOOTH", "AUX", "SPOTIFY", "PRODUCT"} {
		if !forcedWriteBusy(src, false, true) {
			t.Errorf("src=%q is somebody's choice and must hold the write", src)
		}
	}
}

// LOCAL_INTERNET_RADIO is STR's OWN source on a native-preset box, so a
// STOPPED station there is leftover state and not a listening session. Holding
// on it would defer the dead-key self-heal (#342) by the full ceiling on every
// box that has ever played a native station, and that heal exists to bring a
// dead key back in seconds. Read play state decides; an unreadable one keeps
// the hold, because a box that may be playing is the #961 interruption again.
func TestAStoppedNativeStationIsNotSomebodyListening(t *testing.T) {
	const src = "LOCAL_INTERNET_RADIO"
	if forcedWriteBusy(src, false, true) {
		t.Error("a stopped native station held the write, so a dead key waits five minutes")
	}
	if got := forcedHoldReason(src, false, true); got != "free" {
		t.Errorf("reason = %q, want %q", got, "free")
	}
	if !forcedWriteBusy(src, true, true) {
		t.Error("a PLAYING native station must still hold the write")
	}
	if !forcedWriteBusy(src, false, false) {
		t.Error("an unreadable play state on this source must keep the hold")
	}
	// The insurance pass only ever sees the name, so it keeps holding here.
	if resyncSafeSource(src) {
		t.Error("resyncSafeSource must stay name-only and keep this source out")
	}
}

// resyncSafeSource keeps its job: the play test was ADDED beside it, not
// swapped in. A paused Bluetooth session reports no play state, so a play test
// alone would wave the insurance pass through and steal the source.
func TestResyncSafeSourceStillGuardsUserChosenSources(t *testing.T) {
	for _, src := range []string{"UPNP", "INVALID_SOURCE"} {
		if !resyncSafeSource(src) {
			t.Errorf("%s must stay writable when idle", src)
		}
	}
	for _, src := range []string{"BLUETOOTH", "AUX", "LOCAL", "SPOTIFY", "PRODUCT", "LOCAL_INTERNET_RADIO"} {
		if resyncSafeSource(src) {
			t.Errorf("%s is the user's choice and must never be written over", src)
		}
	}
}

// The other door into a preset write, found by re-reading the #961 fix rather
// than by a report: the routine pass heals a slot that fell out of the box's
// own list, and it did that with no source check at all. Only the FORCED pass
// was ever held. A speaker that loses a key while its owner is pairing a phone
// would have been yanked off Bluetooth by the routine pass instead.
//
// Both doors now ask the same question, so this pins that the two agree.
func TestBothWritePathsAskTheSameQuestion(t *testing.T) {
	cases := []struct {
		src     string
		playing bool
		known   bool
		busy    bool
	}{
		{"BLUETOOTH", false, true, true},
		{"AUX", false, true, true},
		{"SPOTIFY", false, true, true},
		{"UPNP", false, true, false},
		{"STANDBY", false, true, false},
		{"", false, false, false},
		{"INVALID_SOURCE", false, true, false},
		{"LOCAL_INTERNET_RADIO", false, true, false},
		{"LOCAL_INTERNET_RADIO", true, true, true},
		{"UPNP", true, true, true},
	}
	for _, c := range cases {
		got := forcedWriteBusy(c.src, c.playing, c.known)
		if got != c.busy {
			t.Errorf("forcedWriteBusy(%q, playing=%v, known=%v) = %v, want %v",
				c.src, c.playing, c.known, got, c.busy)
		}
		// The forced pass must reach the same verdict through its own entry
		// point, or the two paths would drift apart again.
		hold, _ := forcedWriteHold(c.src, c.playing, c.known, true, time.Time{}, time.Now())
		if hold != c.busy {
			t.Errorf("forcedWriteHold(%q, playing=%v) held=%v but busy=%v", c.src, c.playing, hold, c.busy)
		}
	}
}
