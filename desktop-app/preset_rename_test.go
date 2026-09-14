package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Renaming a key from the app (#812). The app must reach the agent's rename-only
// verb, and it must still be able to rename a key on a speaker whose agent
// predates that verb.

// renameBox is a speaker that holds one preset on key 3. patch says whether its
// agent knows PATCH /api/presets/<slot>; an older one answers 405, the same
// answer handlePresetSlot has always given an unknown method.
type renameBox struct {
	mu        sync.Mutex
	srv       *httptest.Server
	patch     bool
	patched   int
	putBodies []Preset
	held      Preset
}

func newRenameBox(t *testing.T, patch bool) *renameBox {
	t.Helper()
	b := &renameBox{
		patch: patch,
		held: Preset{
			Slot: 3, Name: "RADIO PARADISE (MAIN MIX) 320K AAC", Type: "radio",
			StreamURL: "http://stream.example/rp-main", Codec: "AAC+", Bitrate: 320,
		},
	}
	b.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == presetAPIPath:
			_ = json.NewEncoder(w).Encode([]Preset{b.held})
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, presetAPIPath+"/"):
			if !b.patch {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			var in struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			b.patched++
			b.held.Name = in.Name
			_ = json.NewEncoder(w).Encode(b.held)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, presetAPIPath+"/"):
			var p Preset
			_ = json.NewDecoder(r.Body).Decode(&p)
			b.putBodies = append(b.putBodies, p)
			b.held = p
			_ = json.NewEncoder(w).Encode(p)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	b.srv.Listener.Close()
	b.srv.Listener = l
	b.srv.Start()
	t.Cleanup(b.srv.Close)
	return b
}

func (b *renameBox) state() (patched int, puts []Preset, held Preset) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.patched, append([]Preset(nil), b.putBodies...), b.held
}

func TestRenamePresetWritesOnlyTheName(t *testing.T) {
	box := newRenameBox(t, true)
	a := copyApp(t)
	if err := a.RenamePreset("127.0.0.1", listenPort(t, box.srv), 3, "Radio Paradise"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	patched, puts, held := box.state()
	if patched != 1 {
		t.Errorf("the speaker saw %d rename requests, want 1", patched)
	}
	if len(puts) != 0 {
		t.Errorf("a rename must not re-save the whole preset: %+v", puts)
	}
	if held.Name != "Radio Paradise" || held.StreamURL != "http://stream.example/rp-main" {
		t.Errorf("key 3 = %+v, want only the name changed", held)
	}
}

// A speaker whose agent has not been updated yet must still take a rename: the
// app re-saves the key it already holds with the new name, every other field
// exactly as the speaker reported it.
func TestRenamePresetFallsBackToAReSaveOnAnOlderAgent(t *testing.T) {
	box := newRenameBox(t, false)
	a := copyApp(t)
	if err := a.RenamePreset("127.0.0.1", listenPort(t, box.srv), 3, "Radio Paradise"); err != nil {
		t.Fatalf("rename against an older agent: %v", err)
	}
	_, puts, held := box.state()
	if len(puts) != 1 {
		t.Fatalf("the speaker saw %d whole-preset writes, want exactly 1", len(puts))
	}
	got := puts[0]
	if got.Name != "Radio Paradise" {
		t.Errorf("re-saved name = %q, want Radio Paradise", got.Name)
	}
	if got.StreamURL != "http://stream.example/rp-main" || got.Codec != "AAC+" ||
		got.Bitrate != 320 || got.Type != "radio" {
		t.Errorf("the re-save dropped or changed a field: %+v", got)
	}
	if held.Name != "Radio Paradise" {
		t.Errorf("key 3 ended up as %q", held.Name)
	}
}

// An empty name is refused before anything is sent: the key would be left with
// nothing to identify it by.
func TestRenamePresetRefusesAnEmptyName(t *testing.T) {
	box := newRenameBox(t, true)
	a := copyApp(t)
	if err := a.RenamePreset("127.0.0.1", listenPort(t, box.srv), 3, "   "); err == nil {
		t.Fatal("a blank name was accepted")
	}
	patched, puts, held := box.state()
	if patched != 0 || len(puts) != 0 {
		t.Errorf("a refused rename still talked to the speaker: %d patches, %d puts", patched, len(puts))
	}
	if held.Name != "RADIO PARADISE (MAIN MIX) 320K AAC" {
		t.Errorf("the stored name changed to %q", held.Name)
	}
}
