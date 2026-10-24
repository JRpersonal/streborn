package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/mediaservers"
	"github.com/JRpersonal/streborn/internal/presets"
)

// libKeySweepServer is healServer with a preset store and a hook that reports each
// finished background sweep.
func libKeySweepServer(t *testing.T, keys ...presets.Preset) (*Server, chan struct{}) {
	t.Helper()
	s := healServer(t, mediaservers.Server{ID: "AAAA-BBBB", Name: "MinimServer[mac.local]",
		Location: "http://192.0.2.47:9791/AAAA-BBBB/Upnp/device.xml"})
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if err := store.SetSlot(k); err != nil {
			t.Fatal(err)
		}
	}
	s.presets = store
	done := make(chan struct{}, 4)
	s.libSweepDone = func() { done <- struct{}{} }
	return s, done
}

func libKeyGetPresets(t *testing.T, s *Server) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handlePresets(rec, httptest.NewRequest(http.MethodGet, "/api/presets", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/presets: %d", rec.Code)
	}
}

func waitSweep(t *testing.T, done chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the library-key sweep never finished")
	}
}

// #1366, the reporter's bundle: the music server moved, the v1.0.21 agent was
// in place, and the album key on 6 and the playlist key on 2 kept the dead
// address and the letter placeholder for a cover until each was pressed. The
// app only listed the keys. Listing them is now enough: the tracks and the
// covers of both move, and a radio key is left alone.
func TestPresetReadMovesLibraryKeysAndCoversToTheServer(t *testing.T) {
	album := presets.Preset{Slot: 6, Type: "queue", Name: "Moving Pictures", Source: "MinimServer[mac.local]",
		Items: []presets.PresetItem{
			{URL: "http://192.0.2.11:9790/m/Rush/Moving*20Pictures/01.m4a", Art: "http://192.0.2.11:9790/m/Rush/Moving*20Pictures/cover.jpg", Title: "Tom Sawyer"},
			{URL: "http://192.0.2.11:9790/m/Rush/Moving*20Pictures/02.m4a", Art: "http://192.0.2.11:9790/m/Rush/Moving*20Pictures/cover.jpg", Title: "Red Barchetta"},
		}}
	playlist := presets.Preset{Slot: 2, Type: "queue", Name: "Rush favorites", Source: "MinimServer[mac.local]",
		Art: "http://192.0.2.11:9790/m/playlist.jpg",
		Items: []presets.PresetItem{
			{URL: "http://192.0.2.11:9790/m/Rush/Power*20Windows/01.m4a", Art: "http://192.0.2.11:9790/m/Rush/Power*20Windows/cover.jpg", Title: "Manhattan Project"},
		}}
	radio := presets.Preset{Slot: 5, Type: "radio", Name: "Exclusively Rush", StreamURL: "https://radio.example.com/rush"}
	s, done := libKeySweepServer(t, album, playlist, radio)

	libKeyGetPresets(t, s)
	waitSweep(t, done)

	got6, _ := s.presets.Get(6)
	for i, it := range got6.Items {
		if urlHost(it.URL) != "192.0.2.47" || urlHost(it.Art) != "192.0.2.47" {
			t.Fatalf("slot 6 item %d not on the server's address: %+v", i, it)
		}
	}
	got2, _ := s.presets.Get(2)
	if urlHost(got2.Items[0].URL) != "192.0.2.47" || urlHost(got2.Items[0].Art) != "192.0.2.47" || urlHost(got2.Art) != "192.0.2.47" {
		t.Fatalf("slot 2 not moved with its covers: %+v", got2)
	}
	if got5, _ := s.presets.Get(5); got5.StreamURL != radio.StreamURL {
		t.Fatalf("radio key touched: %+v", got5)
	}

	// A second read inside the window starts no second sweep: the app polls
	// every 15 s and must not cost a probe per key each time.
	libKeyGetPresets(t, s)
	select {
	case <-done:
		t.Fatal("a second sweep ran inside libraryKeySweepEvery")
	case <-time.After(200 * time.Millisecond):
	}
}

// The sweep spends network time between reading a key and writing it back. A
// key the owner re-saved meanwhile must win over the healed copy of the old one.
func TestLibraryKeySweepLeavesAKeyChangedMeanwhile(t *testing.T) {
	old := presets.Preset{Slot: 3, Type: "queue", Name: "Old", Source: "MinimServer[mac.local]",
		Items: []presets.PresetItem{{URL: "http://192.0.2.11:9790/m/1.m4a", Title: "t"}}}
	s, _ := libKeySweepServer(t, old)
	fresh := presets.Preset{Slot: 3, Type: "radio", Name: "New station", StreamURL: "https://radio.example.com/new"}
	s.queueHostAnswers = func(context.Context, string) bool {
		// The owner saves another station on the key while it is checked.
		if err := s.presets.SetSlot(fresh); err != nil {
			t.Fatal(err)
		}
		return false
	}

	s.sweepLibraryKeys(context.Background())

	got, _ := s.presets.Get(3)
	if got.Name != "New station" || got.StreamURL != fresh.StreamURL {
		t.Fatalf("the sweep overwrote a key saved meanwhile: %+v", got)
	}
}

// realignQueuePresetArt used to copy the track list only when no cover had
// moved yet, so a key whose own cover moved first had its track covers written
// into the store's backing array behind the store's lock.
func TestRealignArtNeverWritesIntoTheStoresTracks(t *testing.T) {
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSlot(presets.Preset{Slot: 1, Type: "queue", Source: "MinimServer",
		Art:   "http://192.0.2.11:9790/folder.jpg",
		Items: []presets.PresetItem{{URL: "http://192.0.2.47:9790/1.m4a", Art: "http://192.0.2.11:9790/1.jpg"}}}); err != nil {
		t.Fatal(err)
	}
	p, _ := store.Get(1)
	if !realignQueuePresetArt(&p, "192.0.2.47", func(string) bool { return false }) {
		t.Fatal("covers on the dead address not moved")
	}
	if urlHost(p.Items[0].Art) != "192.0.2.47" || urlHost(p.Art) != "192.0.2.47" {
		t.Fatalf("covers not realigned: %+v", p)
	}
	if kept, _ := store.Get(1); urlHost(kept.Items[0].Art) != "192.0.2.11" {
		t.Fatalf("the store's own track list was rewritten in place: %+v", kept.Items[0])
	}
}
