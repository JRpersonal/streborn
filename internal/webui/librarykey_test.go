package webui

import (
	"context"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
)

// The #1065 file: a single ALAC track saved from the Library tab of a
// MinimServer, which is exactly what the reporter had on key 3.
const libraryTrackURL = "http://192.0.2.32:9790/minimserver/*/Music/Album/04*20Track.m4a"

func seedLibraryTrackPreset(t *testing.T, s *Server, slot int) {
	t.Helper()
	if err := s.presets.SetSlot(presets.Preset{
		Slot: slot, Name: "What I Deserve", Type: "radio",
		StreamURL: libraryTrackURL, Source: "MinimServer",
	}); err != nil {
		t.Fatalf("SetSlot: %v", err)
	}
}

func TestLibraryFileMime(t *testing.T) {
	cases := []struct {
		name string
		p    presets.Preset
		want string
	}{
		{"library ALAC track", presets.Preset{Type: "radio", Source: "MinimServer", StreamURL: libraryTrackURL}, "audio/mp4"},
		{"library FLAC, no type recorded", presets.Preset{Source: "Synology", StreamURL: "http://192.0.2.5:50002/a.flac"}, "audio/flac"},
		{"radio station (no Source)", presets.Preset{Type: "radio", StreamURL: "http://stream.example/relax.mp3"}, ""},
		{"library file over https stays on the proxy", presets.Preset{Type: "radio", Source: "Plex", StreamURL: "https://192.0.2.5/a.mp3"}, ""},
		{"unknown extension", presets.Preset{Type: "radio", Source: "MinimServer", StreamURL: "http://192.0.2.5/stream"}, ""},
		{"queue preset", presets.Preset{Type: "queue", Source: "MinimServer", StreamURL: libraryTrackURL}, ""},
		{"spotify preset", presets.Preset{Type: "spotify", Source: "x", StreamURL: libraryTrackURL}, ""},
		{"native service preset", presets.Preset{Type: presets.TypeNative, Source: "Pandora", StreamURL: libraryTrackURL}, ""},
	}
	for _, c := range cases {
		if got := libraryFileMime(c.p); got != c.want {
			t.Errorf("%s: libraryFileMime = %q, want %q", c.name, got, c.want)
		}
	}
}

// The speaker's own key on a single library track must play the file directly,
// the way the app's recall of the same key does. Before, the key fell through to
// the native radio path, the stream proxy failed the speaker's Range request on
// the file, and the speaker went amber on every press (#1065).
func TestHardwareKeyOnLibraryTrackPlaysTheFileDirectly(t *testing.T) {
	s, rec := newPlayTestServer(t)
	seedLibraryTrackPreset(t, s, 3)

	if !s.RecallSlot(context.Background(), 3) {
		t.Fatal("RecallSlot did not claim the library track key")
	}
	if !rec.has("SetAVTransportURI") || !rec.has("Play") {
		t.Fatalf("the file was not pushed to the speaker: recorded actions %v", rec.list())
	}
	s.lastPlayMu.Lock()
	lp := s.lastPlay
	s.lastPlayMu.Unlock()
	if lp == nil || lp.boxURL != libraryTrackURL || lp.mime != "audio/mp4" {
		t.Fatalf("last play = %+v, want the file itself with audio/mp4", lp)
	}
}

// A library queue still playing must not advance over the track the key just
// started.
func TestHardwareKeyOnLibraryTrackEndsAnActiveQueue(t *testing.T) {
	s, _ := newPlayTestServer(t)
	seedLibraryTrackPreset(t, s, 3)
	s.queue.load([]queueItem{
		{URL: "http://192.0.2.10:50002/a.mp3", Title: "A", Mime: "audio/mpeg"},
		{URL: "http://192.0.2.10:50002/b.mp3", Title: "B", Mime: "audio/mpeg"},
	}, 0, false, repeatOff)

	s.RecallSlot(context.Background(), 3)

	if s.queue.isActive() {
		t.Error("the library track key left the old queue active")
	}
}

// A radio station key is not ours to claim here: the native radio path and the
// UPnP recall in cmd/agent own it, unchanged.
func TestHardwareKeyOnRadioStationIsNotClaimed(t *testing.T) {
	s, rec := newPlayTestServer(t)
	seedRadioPreset(t, s, 1)

	if s.RecallSlot(context.Background(), 1) {
		t.Fatal("RecallSlot claimed a radio station key")
	}
	if rec.count() != 0 {
		t.Fatalf("a radio key reached the speaker through RecallSlot: %v", rec.list())
	}
}
