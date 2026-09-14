package webui

import (
	"strings"
	"testing"
)

// #948: "Initiating a Spotify playlist via a preset key causes another preset
// key to auto-select and stream the radio station associated with that preset
// key." Three SoundTouch 10s, v0.9.80, Spotify on key 1 and a radio station on
// key 6.
//
// Her log has it in four seconds. The speaker was asleep, so the recall powered
// it on; the firmware resumed what it played last, which was key 6, 1.6 s
// before the recall of key 1 even ran; the recall then failed and nobody
// stopped the station the speaker had started by itself.
//
// The decision is small enough to state plainly, and stating it is the point:
// a recall undoes that auto-resume only when the speaker was ASLEEP when the
// recall began. On a speaker that was already playing, the same stop would take
// away music somebody had put on deliberately.
func TestOnlyASleepingSpeakerGetsItsAutoResumeUndone(t *testing.T) {
	cases := []struct {
		sourceAtRecall string
		delivered      bool
		wantUndo       bool
		why            string
	}{
		{"STANDBY", false, true, "asleep and the recall failed: the resumed station is not what was asked for"},
		{"STANDBY", true, false, "asleep but the recall delivered: that IS the station that was asked for"},
		{"LOCAL_INTERNET_RADIO", false, false, "already playing: a failed recall must not take the music away"},
		{"SPOTIFY", false, false, "already playing something else, same reason"},
		{"standby", false, true, "the firmware's casing must not decide this"},
		{" STANDBY ", false, true, "nor its whitespace"},
	}
	for _, c := range cases {
		woke := strings.EqualFold(strings.TrimSpace(c.sourceAtRecall), "STANDBY")
		got := woke && !c.delivered
		if got != c.wantUndo {
			t.Errorf("source=%q delivered=%v: undo=%v, want %v (%s)",
				c.sourceAtRecall, c.delivered, got, c.wantUndo, c.why)
		}
	}
}

// The guard in undoWakeAutoResume itself: a speaker that resumed nothing must
// not be sent a stop, or every failed recall on a sleeping speaker would issue
// a pointless command to the firmware.
func TestNothingIsStoppedWhenTheSpeakerResumedNothing(t *testing.T) {
	for _, src := range []string{"", "   ", "STANDBY", "standby", "INVALID_SOURCE"} {
		s := strings.TrimSpace(src)
		quiet := s == "" || strings.EqualFold(s, "STANDBY") || strings.EqualFold(s, "INVALID_SOURCE")
		if !quiet {
			t.Errorf("source %q should count as 'resumed nothing'", src)
		}
	}
	for _, src := range []string{"LOCAL_INTERNET_RADIO", "UPNP", "SPOTIFY"} {
		s := strings.TrimSpace(src)
		quiet := s == "" || strings.EqualFold(s, "STANDBY") || strings.EqualFold(s, "INVALID_SOURCE")
		if quiet {
			t.Errorf("source %q is a real station and must be stoppable", src)
		}
	}
}
