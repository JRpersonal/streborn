package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// MovePreset is what the app offers when a save is refused because the station
// is already on another key (discussions #709 and #925). The refusal stays, so
// the only way a key is ever cleared is the user asking for this move, and the
// station must be on exactly one key when it is done, whatever the speaker's
// agent version.

// moveBox is a speaker whose preset keys can be read, written, deleted and
// (when it is new enough) moved in one request.
type moveBox struct {
	mu       sync.Mutex
	srv      *httptest.Server
	keys     map[int]Preset
	moves    int
	deletes  int
	puts     int
	knowMove bool           // false models an agent older than POST /api/presets/move
	refuse   func(int) bool // a key the speaker will not accept a write on
}

func newMoveBox(t *testing.T, keys map[int]Preset) *moveBox {
	t.Helper()
	b := &moveBox{keys: keys, knowMove: true}
	if b.keys == nil {
		b.keys = map[int]Preset{}
	}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == presetAPIPath+"/move":
			b.moves++
			if !b.knowMove {
				http.NotFound(w, r)
				return
			}
			var req struct{ From, To int }
			_ = json.NewDecoder(r.Body).Decode(&req)
			p, ok := b.keys[req.From]
			if !ok {
				http.Error(w, "preset not set", http.StatusNotFound)
				return
			}
			delete(b.keys, req.From)
			p.Slot = req.To
			b.keys[req.To] = p
			_ = json.NewEncoder(w).Encode(p)
		case r.Method == http.MethodGet && r.URL.Path == presetAPIPath:
			out := []Preset{}
			for _, p := range b.keys {
				out = append(out, p)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, presetAPIPath+"/"):
			b.puts++
			var p Preset
			_ = json.NewDecoder(r.Body).Decode(&p)
			if b.refuse != nil && b.refuse(p.Slot) {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"code":"already-on-slot","slot":9,"name":"Something else"}`))
				return
			}
			b.keys[p.Slot] = p
			_ = json.NewEncoder(w).Encode(p)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, presetAPIPath+"/"):
			b.deletes++
			var slot int
			_, _ = fmt.Sscanf(strings.TrimPrefix(r.URL.Path, presetAPIPath+"/"), "%d", &slot)
			delete(b.keys, slot)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{}"))
		}
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *moveBox) held() map[int]string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[int]string{}
	for slot, p := range b.keys {
		out[slot] = p.Name
	}
	return out
}

func moveApp(t *testing.T) *App {
	t.Helper()
	a := newTestApp()
	a.logger = slog.Default()
	return a
}

// A current agent does the whole move itself, so the app must not take any key
// apart on its own.
func TestMovePresetAsksTheSpeakerToDoItInOneRequest(t *testing.T) {
	box := newMoveBox(t, map[int]Preset{
		1: {Slot: 1, Name: "Jens Chill", Type: "spotify", URI: "spotify:playlist:37i9dQZF1DWX7rdRjOECPW"},
		3: {Slot: 3, Name: "Overwritten", Type: "radio", StreamURL: "http://stream.example/old"},
	})
	a := moveApp(t)
	if err := a.MovePreset("127.0.0.1", listenPort(t, box.srv), 1, 3); err != nil {
		t.Fatalf("move: %v", err)
	}
	if got := box.held(); got[3] != "Jens Chill" || len(got) != 1 {
		t.Errorf("keys after the move = %v, want only key 3 holding Jens Chill", got)
	}
	if box.deletes != 0 || box.puts != 0 {
		t.Errorf("the app took the keys apart itself: %d deletes, %d writes", box.deletes, box.puts)
	}
}

// The desktop app updates independently of the on-box agent, so the button can
// land in front of a speaker that has no move endpoint. The move must still
// happen there.
func TestMovePresetFallsBackOnAnOlderAgent(t *testing.T) {
	box := newMoveBox(t, map[int]Preset{
		2: {Slot: 2, Name: "1LIVE", Type: "radio", StreamURL: "http://stream.example/1live"},
	})
	box.knowMove = false
	a := moveApp(t)
	if err := a.MovePreset("127.0.0.1", listenPort(t, box.srv), 2, 5); err != nil {
		t.Fatalf("move against an older agent: %v", err)
	}
	got := box.held()
	if got[5] != "1LIVE" {
		t.Errorf("key 5 = %q, want 1LIVE", got[5])
	}
	if _, still := got[2]; still {
		t.Errorf("the old key was not cleared: %v", got)
	}
}

// The one way the fallback can lose a preset is a refused write after the old
// key is already gone, which is the #836 loss all over again. The station has to
// come back on the key it started on.
func TestMovePresetPutsTheStationBackWhenTheNewKeyIsRefused(t *testing.T) {
	box := newMoveBox(t, map[int]Preset{
		2: {Slot: 2, Name: "1LIVE", Type: "radio", StreamURL: "http://stream.example/1live"},
	})
	box.knowMove = false
	box.refuse = func(slot int) bool { return slot == 5 }
	a := moveApp(t)
	err := a.MovePreset("127.0.0.1", listenPort(t, box.srv), 2, 5)
	if err == nil {
		t.Fatal("a refused move was reported as a success")
	}
	got := box.held()
	if got[2] != "1LIVE" {
		t.Fatalf("the station is gone after a failed move: keys = %v", got)
	}
	if _, landed := got[5]; landed {
		t.Errorf("the refused key holds something: %v", got)
	}
}

// An empty source key is a caller mistake, and the answer must say so without
// touching anything.
func TestMovePresetRefusesAnEmptySourceKeyOnAnOlderAgent(t *testing.T) {
	box := newMoveBox(t, map[int]Preset{
		1: {Slot: 1, Name: "1LIVE", Type: "radio", StreamURL: "http://stream.example/1live"},
	})
	box.knowMove = false
	a := moveApp(t)
	if err := a.MovePreset("127.0.0.1", listenPort(t, box.srv), 4, 5); err == nil {
		t.Fatal("moving an empty key was reported as a success")
	}
	if box.deletes != 0 {
		t.Errorf("a key was deleted for a move that could not happen (%d deletes)", box.deletes)
	}
	if got := box.held(); got[1] != "1LIVE" || len(got) != 1 {
		t.Errorf("keys = %v, want key 1 untouched", got)
	}
}
