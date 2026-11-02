package main

import (
	"net/http"
	"strings"
	"testing"
)

// One music-library folder, one key (#1695). The Library's "save folder to a
// key" is a save gesture, so it asks the agent to judge it as one (save=live),
// and the agent's already-on-slot refusal reaches the frontend whole: the move
// offer parses the other key and the name out of the error text.
func TestSaveFolderPresetIsASaveGestureAndCarriesTheRefusal(t *testing.T) {
	var gotQuery string
	port := savePlayingBox(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/presets/6" {
			http.NotFound(w, r)
			return
		}
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"already-on-slot","name":"Power Windows","slot":3}`))
	})
	err := moveApp(t).SaveFolderPreset("127.0.0.1", port, 6, `{"name":"Power Windows","type":"queue","items":[{"url":"http://192.0.2.10/a.flac"}]}`)
	if gotQuery != "save=live" {
		t.Errorf("query = %q, want save=live", gotQuery)
	}
	if err == nil {
		t.Fatal("a refused save came back as stored")
	}
	for _, want := range []string{"status 409:", `"code":"already-on-slot"`, `"slot":3`, `"name":"Power Windows"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to carry %s", err, want)
		}
	}
}

// The live-queue save (a hold on a key tile while a folder plays) hands the
// same refusal through, not as "no-queue", so the frontend shows the note and
// the move offer instead of falling back to another save path.
func TestSaveQueuePresetCarriesTheRefusal(t *testing.T) {
	port := savePlayingBox(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/queue/save-slot" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"already-on-slot","name":"Power Windows","slot":3}`))
	})
	_, err := moveApp(t).SaveQueuePreset("127.0.0.1", port, 6)
	if err == nil || !strings.Contains(err.Error(), "already-on-slot") || !strings.Contains(err.Error(), `"slot":3`) {
		t.Fatalf("err = %v, want the already-on-slot body", err)
	}
	if strings.Contains(err.Error(), "no-queue") {
		t.Fatalf("err = %v reads as no-queue, the frontend would fall through", err)
	}
}
