package spotify

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// The engine config STR writes for go-librespot. Two of its lines are load-
// bearing in ways that are easy to undo by accident, so they are pinned here.
func TestEngineConfigTurnsNormalisationOff(t *testing.T) {
	m := New("", filepath.Join(t.TempDir(), "cfg"), "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	got := m.configYAML("Kitchen", 20)

	// Passthrough hands the speaker the raw Ogg, so the normalisation factor is
	// computed and then thrown away. That would be merely wasteful if the
	// computation were safe: it dereferences the track's normalisation metadata
	// without checking it, and a track that carries none takes the whole engine
	// down with a SIGSEGV mid-playlist (upstream fixed it on 2026-08-29; the
	// build STR ships predates that). Four such crashes sat in one household's
	// diagnostic on 2026-09-26.
	if !strings.Contains(got, "audio_output_pipe_passthrough: true") {
		t.Fatal("passthrough is the whole audio path and it is gone")
	}
	if !strings.Contains(got, "normalisation_disabled: true") {
		t.Error("normalisation must stay off: in passthrough its result is discarded, and computing it can crash the engine")
	}
}

// Everything the engine can be told to keep in memory or on flash is pinned
// here, explicitly, whatever its own default happens to be. A default is not a
// decision, and this speaker has about 35 MB of RAM and a few MB of flash to
// spend, against an upstream that reasonably sizes its caches for a desktop.
func TestEngineConfigPinsTheCachesOff(t *testing.T) {
	m := New("", filepath.Join(t.TempDir(), "cfg"), "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	got := m.configYAML("Kitchen", 20)

	for _, want := range []string{
		"cache:",
		"  enabled: false",
		"metadata:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the engine config no longer carries %q", want)
		}
	}
	// Both blocks, not one of them twice.
	if strings.Count(got, "  enabled: false") < 2 {
		t.Error("the audio cache and the metadata cache must each be pinned off")
	}
}
