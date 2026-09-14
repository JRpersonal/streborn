package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
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

// The durable half of #948, after two releases had tried to STOP the station
// the firmware had already started.
//
// A Spotify recall that cannot succeed is now refused BEFORE anything touches
// the speaker, so `sys power` is never sent, the firmware never resumes its
// last station, and there is no auto-resume left to undo. These three
// preconditions are all answerable with the box asleep.
func TestASpotifyRecallThatCannotSucceedNeverReachesTheSpeaker(t *testing.T) {
	spotifyPreset := presets.Preset{Slot: 1, Name: "Morning mix", Type: "spotify",
		URI: "spotify:playlist:0000000000000000000000"}

	cases := []struct {
		name     string
		setup    func(*Server)
		wantCode int
		wantBody string
	}{
		{
			name:     "no engine on this box",
			setup:    func(s *Server) {},
			wantCode: http.StatusServiceUnavailable,
			wantBody: "Spotify not configured",
		},
		{
			name: "speaker never picked in Spotify",
			setup: func(s *Server) {
				s.spotifyPlay = func(context.Context, string, string, bool) error { return nil }
				s.spotifyCanRecall = func(context.Context) bool { return false }
			},
			wantCode: http.StatusUnprocessableEntity,
			wantBody: "spotify-not-logged-in",
		},
		{
			name: "free account",
			setup: func(s *Server) {
				s.spotifyPlay = func(context.Context, string, string, bool) error { return nil }
				s.spotifyCanRecall = func(context.Context) bool { return true }
				s.spotifyPremiumRequired = func() bool { return true }
			},
			wantCode: http.StatusUnprocessableEntity,
			wantBody: "spotify-premium-required",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, rec := newPlayTestServer(t)
			c.setup(s)
			if err := s.presets.SetSlot(spotifyPreset); err != nil {
				t.Fatalf("SetSlot: %v", err)
			}
			w := httptest.NewRecorder()
			s.handlePlaySlot(w, httptest.NewRequest(http.MethodPost, "/api/play/1", nil))

			if w.Code != c.wantCode {
				t.Errorf("status = %d, want %d (body %s)", w.Code, c.wantCode, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), c.wantBody) {
				t.Errorf("body = %s, want it to carry %q", w.Body.String(), c.wantBody)
			}
			// Nothing may have been sent to the speaker. A UPnP action here
			// would mean the handler had already driven the box.
			if n := rec.count(); n != 0 {
				t.Errorf("%d commands reached the speaker, want none: %v", n, rec.list())
			}
			// The refusal ran before the recall was recorded as a user play,
			// which is the same point in the handler as the standby wake.
			s.standbyStopMu.Lock()
			started := s.lastUserPlayStart
			s.standbyStopMu.Unlock()
			if !started.IsZero() {
				t.Error("the recall was recorded as a user play, so the handler had already gone past the wake")
			}
		})
	}
}

// The gate must not touch anything else. A radio preset, and a Spotify preset
// on a speaker that CAN recall, both fall through to the normal path.
func TestTheSpotifyGateOnlyRefusesSpotifyRecallsThatCannotWork(t *testing.T) {
	s, _ := newPlayTestServer(t)
	s.spotifyPlay = func(context.Context, string, string, bool) error { return nil }
	s.spotifyCanRecall = func(context.Context) bool { return true }
	s.spotifyPremiumRequired = func() bool { return false }

	radio := presets.Preset{Slot: 2, Name: "Test Station", Type: "radio", StreamURL: "http://stream.example/x"}
	spot := presets.Preset{Slot: 1, Name: "Mix", Type: "spotify", URI: "spotify:playlist:x"}
	// A "spotify" preset with no URI is the broken-save shape and takes the
	// radio path, so the gate must let it through rather than refusing it.
	noURI := presets.Preset{Slot: 3, Name: "Half saved", Type: "spotify", StreamURL: "http://stream.example/y"}

	for _, p := range []presets.Preset{radio, spot, noURI} {
		w := httptest.NewRecorder()
		if s.spotifyRecallRefused(w, p.Slot, p) {
			t.Errorf("slot %d (%s) was refused: %s", p.Slot, p.Type, w.Body.String())
		}
	}
}
