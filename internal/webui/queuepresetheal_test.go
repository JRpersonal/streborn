package webui

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/JRpersonal/streborn/internal/mediaservers"
	"github.com/JRpersonal/streborn/internal/presets"
)

func healServer(t *testing.T, regs ...mediaservers.Server) *Server {
	t.Helper()
	store, err := mediaservers.Load(filepath.Join(t.TempDir(), "mediaservers.json"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	for _, r := range regs {
		if err := store.Add(r); err != nil {
			t.Fatalf("seed store: %v", err)
		}
	}
	return &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), mediaServers: store}
}

func queueKey(source string, urls ...string) presets.Preset {
	p := presets.Preset{Slot: 1, Type: "queue", Name: "Jazz", Source: source}
	for _, u := range urls {
		p.Items = append(p.Items, presets.PresetItem{URL: u, Title: "t"})
	}
	return p
}

// The reported case: the router gives the NAS a new lease and every folder key
// pointing at it dies at once. The key records which server it came from, and the
// store knows where that server lives now, so nothing about it is unknowable.
func TestQueuePresetFollowsAServerThatMoved(t *testing.T) {
	s := healServer(t, mediaservers.Server{ID: "AAAA-BBBB", Name: "MinimServer",
		Location: "http://192.0.2.50:9790/desc.xml"})
	p := queueKey("MinimServer",
		"http://192.0.2.11:9790/music/1.flac",
		"http://192.0.2.11:9790/music/2.flac")
	p.Items[0].Art = "http://192.0.2.11:9790/art/1.jpg"

	changed, from, to := s.healQueuePresetHost(context.Background(), &p)
	if !changed || from != "192.0.2.11" || to != "192.0.2.50" {
		t.Fatalf("want a rewrite 192.0.2.11 -> 192.0.2.50, got changed=%v %q -> %q", changed, from, to)
	}
	// The PORT and the path are not ours to correct: a DLNA server's content port
	// is often not its description port.
	if p.Items[0].URL != "http://192.0.2.50:9790/music/1.flac" || p.Items[1].URL != "http://192.0.2.50:9790/music/2.flac" {
		t.Fatalf("tracks not rewritten as expected: %+v", p.Items)
	}
	if p.Items[0].Art != "http://192.0.2.50:9790/art/1.jpg" {
		t.Fatalf("cover left on the old address: %q", p.Items[0].Art)
	}
}

// Everything that is not certain must leave the preset alone. A wrong rewrite is
// worse than a dead key: the key at least fails honestly.
func TestQueuePresetHealLeavesUncertainCasesAlone(t *testing.T) {
	live := mediaservers.Server{ID: "AAAA-BBBB", Name: "MinimServer", Location: "http://192.0.2.50:9790/desc.xml"}

	t.Run("server still at that address", func(t *testing.T) {
		s := healServer(t, live)
		p := queueKey("MinimServer", "http://192.0.2.50:9790/music/1.flac")
		if changed, _, _ := s.healQueuePresetHost(context.Background(), &p); changed {
			t.Fatal("rewrote a key whose server never moved")
		}
	})

	t.Run("name matches nothing registered", func(t *testing.T) {
		// The server this key came from is no longer a music source. Guessing at
		// the one that is would silently point the key at a stranger's library.
		s := healServer(t, live)
		p := queueKey("Plex", "http://192.0.2.11:32469/music/1.flac")
		if changed, _, _ := s.healQueuePresetHost(context.Background(), &p); changed {
			t.Fatal("pointed a key at a server it never came from")
		}
	})

	t.Run("no name and several servers", func(t *testing.T) {
		s := healServer(t, live, mediaservers.Server{ID: "CCCC-DDDD", Name: "Synology",
			Location: "http://192.0.2.60:50001/desc.xml"})
		p := queueKey("", "http://192.0.2.11:9790/music/1.flac")
		if changed, _, _ := s.healQueuePresetHost(context.Background(), &p); changed {
			t.Fatal("picked a server for a key that does not say where it came from")
		}
	})

	t.Run("not a folder key", func(t *testing.T) {
		s := healServer(t, live)
		p := presets.Preset{Slot: 1, Type: "radio", StreamURL: "http://192.0.2.11:8000/stream"}
		if changed, _, _ := s.healQueuePresetHost(context.Background(), &p); changed {
			t.Fatal("touched a radio key")
		}
	})
}

// A folder can hold tracks from more than one server (a saved list built across
// two). Only the half that moved may be rewritten.
func TestQueuePresetHealMovesOnlyWhatMoved(t *testing.T) {
	s := healServer(t, mediaservers.Server{ID: "AAAA-BBBB", Name: "MinimServer",
		Location: "http://192.0.2.50:9790/desc.xml"})
	p := queueKey("MinimServer",
		"http://192.0.2.11:9790/music/1.flac",
		"http://192.0.2.77:8200/other/2.mp3")

	changed, _, _ := s.healQueuePresetHost(context.Background(), &p)
	if !changed {
		t.Fatal("no rewrite at all")
	}
	if p.Items[1].URL != "http://192.0.2.77:8200/other/2.mp3" {
		t.Fatalf("a track on a different server was moved too: %q", p.Items[1].URL)
	}
}

func TestRewriteURLHostKeepsEverythingElse(t *testing.T) {
	cases := []struct{ in, want string }{
		{"http://192.0.2.11:9790/a%20b/1.flac?x=1", "http://192.0.2.50:9790/a%20b/1.flac?x=1"},
		{"http://192.0.2.11/1.flac", "http://192.0.2.50/1.flac"},
	}
	for _, c := range cases {
		got, ok := rewriteURLHost(c.in, "192.0.2.11", "192.0.2.50")
		if !ok || got != c.want {
			t.Errorf("rewriteURLHost(%q) = %q (%v), want %q", c.in, got, ok, c.want)
		}
	}
	if _, ok := rewriteURLHost("http://192.0.2.99/1.flac", "192.0.2.11", "192.0.2.50"); ok {
		t.Error("rewrote a URL that was not on the old host")
	}
	if _, ok := rewriteURLHost("", "192.0.2.11", "192.0.2.50"); ok {
		t.Error("rewrote an empty URL")
	}
}
