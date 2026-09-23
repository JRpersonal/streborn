package spotify

import (
	"context"
	"strings"
	"testing"
)

// Patrick, mail 2026-09-09 and again on the 15th: he has to set repeat by hand
// in the Spotify app every evening. The preset stored only the playlist URI, so
// a recall started the context and left repeat wherever the previous session
// had put it.
//
// The half that matters is the WARM path. Pressing the key while that same
// playlist already plays takes the fast route, which exists to skip the cold
// staging and skipped this with it. That is the moment a listener most expects
// the preset's own setting to win.

func repeatBodies(calls []recordedCall, path string) []string {
	var out []string
	for _, c := range calls {
		if strings.Contains(c.path, path) {
			out = append(out, c.body)
		}
	}
	return out
}

func TestAWarmRecallSetsTheRepeatThePresetSaved(t *testing.T) {
	m, calls, cleanup := mockLibrespot(t)
	defer cleanup()
	const ctxURI = "spotify:playlist:abc"
	makeWarm(m, ctxURI)

	if err := m.Play(context.Background(), ctxURI, PlayOptions{Repeat: true}); err != nil {
		t.Fatalf("Play: %v", err)
	}
	got := repeatBodies(calls(), "/player/repeat_context")
	if len(got) != 1 || !strings.Contains(got[0], `"repeat_context":true`) {
		t.Fatalf("repeat_context on the fast path = %v, want one call turning it on", got)
	}
	// A session left on single-track repeat would loop one song all evening and
	// look exactly like the feature not working.
	if tr := repeatBodies(calls(), "/player/repeat_track"); len(tr) != 1 || !strings.Contains(tr[0], `"repeat_track":false`) {
		t.Errorf("repeat_track = %v, want it cleared when the playlist should loop", tr)
	}
}

// Set explicitly even when false, for the same reason shuffle is: the engine
// keeps repeat per session, so a preset saved WITHOUT repeat would keep looping
// because an earlier preset had it on.
func TestARecallWithoutRepeatClearsAStaleOne(t *testing.T) {
	m, calls, cleanup := mockLibrespot(t)
	defer cleanup()
	const ctxURI = "spotify:playlist:abc"
	makeWarm(m, ctxURI)

	if err := m.Play(context.Background(), ctxURI, PlayOptions{Repeat: false}); err != nil {
		t.Fatalf("Play: %v", err)
	}
	got := repeatBodies(calls(), "/player/repeat_context")
	if len(got) != 1 || !strings.Contains(got[0], `"repeat_context":false`) {
		t.Fatalf("repeat_context = %v, want one call turning it OFF", got)
	}
	if tr := repeatBodies(calls(), "/player/repeat_track"); len(tr) != 0 {
		t.Errorf("repeat_track was touched with repeat off: %v", tr)
	}
}

func TestAColdRecallSetsRepeatToo(t *testing.T) {
	m, calls, cleanup := mockLibrespot(t)
	defer cleanup()
	// No makeWarm: a cold context takes the full staging path.
	if err := m.Play(context.Background(), "spotify:playlist:cold", PlayOptions{Repeat: true}); err != nil {
		t.Fatalf("Play: %v", err)
	}
	got := repeatBodies(calls(), "/player/repeat_context")
	if len(got) != 1 || !strings.Contains(got[0], `"repeat_context":true`) {
		t.Fatalf("repeat_context on the cold path = %v, want one call turning it on", got)
	}
}
