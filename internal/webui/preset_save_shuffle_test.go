package webui

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
)

// A Spotify preset saved from the RUNNING playback must carry the live shuffle
// state: a playlist the user listens to shuffled recalls shuffled. Without the
// stamp a long-press save always produced a resume preset that replayed the
// identical track order on every press (live ST30 2026-08-19, slot 4 "Rock").

func newShuffleStampServer(t *testing.T, liveCtx string, liveShuffle bool) *Server {
	t.Helper()
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatalf("presets.Load: %v", err)
	}
	return &Server{
		presets:        store,
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		spotifyContext: func() string { return liveCtx },
		spotifyShuffle: func(context.Context) bool { return liveShuffle },
	}
}

func TestPresetSaveStampsLiveShuffleOntoLiveContext(t *testing.T) {
	s := newShuffleStampServer(t, "spotify:playlist:3HEceRN9WWMATLvxo16X0D", true)
	w := putPreset(t, s, 4, `{"name":"Rock","type":"spotify","uri":"spotify:playlist:3HEceRN9WWMATLvxo16X0D"}`)
	if w.Code != 200 {
		t.Fatalf("status = %d (body %s)", w.Code, w.Body.String())
	}
	p, ok := s.presets.Get(4)
	if !ok || !p.Shuffle {
		t.Fatalf("stored preset = %+v, ok=%v; want Shuffle=true from the live session", p, ok)
	}
}

// The engine may announce the ephemeral station wrapper for the playlist the
// save path stores unwrapped; the live-context match must see through it.
func TestPresetSaveStampsShuffleThroughStationWrapper(t *testing.T) {
	s := newShuffleStampServer(t, "spotify:station:playlist:3HEceRN9WWMATLvxo16X0D", true)
	w := putPreset(t, s, 4, `{"name":"Rock","type":"spotify","uri":"spotify:playlist:3HEceRN9WWMATLvxo16X0D"}`)
	if w.Code != 200 {
		t.Fatalf("status = %d (body %s)", w.Code, w.Body.String())
	}
	if p, _ := s.presets.Get(4); !p.Shuffle {
		t.Fatal("shuffle stamp must match the unwrapped station context")
	}
}

// An unshuffled live session stays a faithful resume preset.
func TestPresetSaveKeepsResumeWhenLiveSessionUnshuffled(t *testing.T) {
	s := newShuffleStampServer(t, "spotify:playlist:3HEceRN9WWMATLvxo16X0D", false)
	w := putPreset(t, s, 4, `{"name":"Rock","type":"spotify","uri":"spotify:playlist:3HEceRN9WWMATLvxo16X0D"}`)
	if w.Code != 200 {
		t.Fatalf("status = %d (body %s)", w.Code, w.Body.String())
	}
	if p, _ := s.presets.Get(4); p.Shuffle {
		t.Fatal("an unshuffled live session must not invent a shuffle preset")
	}
}

// Saving content that is NOT playing right now must not consult the live
// session at all: a bulk rename or a copied preset keeps whatever it carried.
func TestPresetSaveLeavesNonLiveContextAlone(t *testing.T) {
	s := newShuffleStampServer(t, "spotify:playlist:SOMETHING_ELSE", true)
	w := putPreset(t, s, 5, `{"name":"Focus","type":"spotify","uri":"spotify:playlist:37i9dQZF1E8LF0ybJT4A8r"}`)
	if w.Code != 200 {
		t.Fatalf("status = %d (body %s)", w.Code, w.Body.String())
	}
	if p, _ := s.presets.Get(5); p.Shuffle {
		t.Fatal("a non-live save must not pick up the live session's shuffle state")
	}
}

// An explicit shuffle flag carried in by the client (box-to-box preset copy)
// survives even when the live session happens to play the same list unshuffled.
func TestPresetSaveKeepsExplicitShuffleFlag(t *testing.T) {
	s := newShuffleStampServer(t, "spotify:playlist:3HEceRN9WWMATLvxo16X0D", false)
	w := putPreset(t, s, 4, `{"name":"Rock","type":"spotify","uri":"spotify:playlist:3HEceRN9WWMATLvxo16X0D","shuffle":true}`)
	if w.Code != 200 {
		t.Fatalf("status = %d (body %s)", w.Code, w.Body.String())
	}
	if p, _ := s.presets.Get(4); !p.Shuffle {
		t.Fatal("an explicit shuffle=true from the client must never be cleared")
	}
}
