package main

import (
	"strings"
	"testing"
)

// A SoundTouch 300 does not come back from an install on its own: it blinks and
// stays unreachable until the owner interrupts power. The app has had that
// sentence since the blink was confirmed, but only on the update path, so a
// first install told the owner the opposite.
//
// Mail, 2026-09-15: an ST20 and an ST300 installed back to back. The ST20 was
// done in three minutes. The ST300 went silent at 22:03 and was still silent
// when the owner saved the report at 22:10, having been told ST Reborn would
// keep looking and say so when the speaker answered.
func TestAnST300IsToldToPullThePlug(t *testing.T) {
	msg := speakerNotBackMessage("SoundTouch 300")
	for _, want := range []string{"Unplug", "ten seconds", "will not come back on its own"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the ST300 message is missing %q: %s", want, msg)
		}
	}
	// The generic reassurance is the thing that was wrong here, so it must be
	// gone, not merely joined by the instruction.
	if strings.Contains(msg, "keeps looking by itself") {
		t.Error("the ST300 still gets the wait-and-see line it cannot act on")
	}
}

func TestEveryOtherModelKeepsTheWaitMessage(t *testing.T) {
	for _, model := range []string{"SoundTouch 10", "SoundTouch 20", "SoundTouch 30", "SoundTouch Portable", "Wave SoundTouch", "CineMate 130", "Lifestyle 600", ""} {
		msg := speakerNotBackMessage(model)
		if strings.Contains(msg, "Unplug the soundbar") {
			t.Errorf("%q was told to unplug a soundbar it does not have", model)
		}
		if !strings.Contains(msg, "keeps looking by itself") {
			t.Errorf("%q lost the wait-and-see message: %s", model, msg)
		}
	}
}

// The substring test is only safe while no other model name contains 300.
// SoundTouch 30 is the one that would hurt, so it is pinned by name.
func TestTheModelMatchDoesNotCatchTheSoundTouch30(t *testing.T) {
	if isST300("SoundTouch 30") {
		t.Fatal("the SoundTouch 30 would be told to unplug a soundbar")
	}
	if !isST300("SoundTouch 300") {
		t.Fatal("the SoundTouch 300 is not recognised")
	}
}
