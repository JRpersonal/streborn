package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
)

// The Spotify view's save-to-key card offers the keys pressed twice too
// (#1587): SaveSpotifyPreset hands a slot of the second row (7..12) to the
// agent exactly like one of the first, as a live save on that slot. The agent
// keeps it in STR's store only (internal/webui presetrows_test.go).
func TestSaveSpotifyPresetTakesBothRows(t *testing.T) {
	type put struct {
		path, query string
		body        Preset
	}
	var (
		mu   sync.Mutex
		puts []put
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p Preset
		_ = json.NewDecoder(r.Body).Decode(&p)
		mu.Lock()
		puts = append(puts, put{r.URL.Path, r.URL.RawQuery, p})
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(p)
	}))
	t.Cleanup(srv.Close)
	a := copyApp(t)
	port := listenPort(t, srv)
	const uri = "spotify:playlist:37i9dQZF1DXcBWIGoYBM5M"
	for _, slot := range []int{1, 6, 7, 8, 12} {
		if err := a.SaveSpotifyPreset("127.0.0.1", port, slot, "Discover Weekly", uri, "user-id-here"); err != nil {
			t.Fatalf("slot %d: %v", slot, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(puts) != 5 {
		t.Fatalf("the speaker saw %d saves, want 5: %+v", len(puts), puts)
	}
	for i, slot := range []int{1, 6, 7, 8, 12} {
		got := puts[i]
		if want := presetAPIPath + "/" + strconv.Itoa(slot); got.path != want || got.query != "save=live" {
			t.Errorf("save %d went to %s?%s, want %s?save=live", i, got.path, got.query, want)
		}
		if got.body.Slot != slot || got.body.Type != "spotify" || got.body.URI != uri || got.body.Account != "user-id-here" {
			t.Errorf("save on slot %d sent %+v", slot, got.body)
		}
	}
}
