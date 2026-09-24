package webui

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
)

// A Spotify preset saved from the RUNNING playback must carry the live repeat
// state, the same way it carries shuffle. The engine keeps repeat per session,
// so a playlist the user had looping went back to stopping at the end on the
// next preset press and he set it again by hand in the Spotify app, every
// evening (Patrick, 2026-09-09 and again on the 15th).

func newRepeatStampServer(t *testing.T, liveCtx string, liveRepeat bool) *Server {
	t.Helper()
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatalf("presets.Load: %v", err)
	}
	return &Server{
		presets:        store,
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		spotifyContext: func() string { return liveCtx },
		spotifyRepeat:  func(context.Context) bool { return liveRepeat },
	}
}

func TestPresetSaveStampsLiveRepeatOntoLiveContext(t *testing.T) {
	s := newRepeatStampServer(t, "spotify:playlist:3HEceRN9WWMATLvxo16X0D", true)
	w := putPreset(t, s, 4, `{"name":"Rock","type":"spotify","uri":"spotify:playlist:3HEceRN9WWMATLvxo16X0D"}`)
	if w.Code != 200 {
		t.Fatalf("status = %d (body %s)", w.Code, w.Body.String())
	}
	p, ok := s.presets.Get(4)
	if !ok || !p.Repeat {
		t.Fatalf("stored preset = %+v, ok=%v; want Repeat=true from the live session", p, ok)
	}
}

// The engine may announce the ephemeral station wrapper for the playlist the
// save path stores unwrapped, exactly as it does for shuffle.
func TestPresetSaveStampsRepeatThroughStationWrapper(t *testing.T) {
	s := newRepeatStampServer(t, "spotify:station:playlist:3HEceRN9WWMATLvxo16X0D", true)
	w := putPreset(t, s, 4, `{"name":"Rock","type":"spotify","uri":"spotify:playlist:3HEceRN9WWMATLvxo16X0D"}`)
	if w.Code != 200 {
		t.Fatalf("status = %d (body %s)", w.Code, w.Body.String())
	}
	if p, _ := s.presets.Get(4); !p.Repeat {
		t.Fatal("repeat stamp must match the unwrapped station context")
	}
}

// A session that is not looping must not invent a repeat preset, or every
// preset saved while music plays would suddenly run all night.
func TestPresetSaveKeepsRepeatOffWhenLiveSessionDoesNotLoop(t *testing.T) {
	s := newRepeatStampServer(t, "spotify:playlist:3HEceRN9WWMATLvxo16X0D", false)
	w := putPreset(t, s, 4, `{"name":"Rock","type":"spotify","uri":"spotify:playlist:3HEceRN9WWMATLvxo16X0D"}`)
	if w.Code != 200 {
		t.Fatalf("status = %d (body %s)", w.Code, w.Body.String())
	}
	if p, _ := s.presets.Get(4); p.Repeat {
		t.Fatal("a non-looping live session must not invent a repeat preset")
	}
}

// Saving content that is NOT playing right now must not consult the live
// session: a bulk rename or a copied preset keeps whatever it carried.
func TestPresetSaveLeavesRepeatOfNonLiveContextAlone(t *testing.T) {
	s := newRepeatStampServer(t, "spotify:playlist:SOMETHING_ELSE", true)
	w := putPreset(t, s, 5, `{"name":"Focus","type":"spotify","uri":"spotify:playlist:37i9dQZF1E8LF0ybJT4A8r"}`)
	if w.Code != 200 {
		t.Fatalf("status = %d (body %s)", w.Code, w.Body.String())
	}
	if p, _ := s.presets.Get(5); p.Repeat {
		t.Fatal("a non-live save must not pick up the live session's repeat state")
	}
}

// An explicit repeat flag carried in by the client (box-to-box preset copy)
// survives even when the live session plays the same list without looping.
func TestPresetSaveKeepsExplicitRepeatFlag(t *testing.T) {
	s := newRepeatStampServer(t, "spotify:playlist:3HEceRN9WWMATLvxo16X0D", false)
	w := putPreset(t, s, 4, `{"name":"Rock","type":"spotify","uri":"spotify:playlist:3HEceRN9WWMATLvxo16X0D","repeat":true}`)
	if w.Code != 200 {
		t.Fatalf("status = %d (body %s)", w.Code, w.Body.String())
	}
	if p, _ := s.presets.Get(4); !p.Repeat {
		t.Fatal("an explicit repeat=true from the client must never be cleared")
	}
}

// A speaker with no Spotify configured has no resolver at all. The save must
// still succeed and simply leave repeat alone.
func TestPresetSaveWithoutARepeatResolverStillSaves(t *testing.T) {
	s := newRepeatStampServer(t, "spotify:playlist:3HEceRN9WWMATLvxo16X0D", false)
	s.spotifyRepeat = nil
	w := putPreset(t, s, 4, `{"name":"Rock","type":"spotify","uri":"spotify:playlist:3HEceRN9WWMATLvxo16X0D"}`)
	if w.Code != 200 {
		t.Fatalf("status = %d (body %s)", w.Code, w.Body.String())
	}
	if p, ok := s.presets.Get(4); !ok || p.Repeat {
		t.Fatalf("stored preset = %+v, ok=%v; want a plain saved preset with Repeat=false", p, ok)
	}
}
