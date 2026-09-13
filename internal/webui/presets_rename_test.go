package webui

import (
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
)

// newRenameServer builds a Server whose preset store persists into a real file,
// so a test can prove a rename survives the agent restarting (the store is
// re-loaded from that path). No box behind it: boxHost empty skips the hardware
// push.
func newRenameServer(t *testing.T) (*Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "presets.json")
	store, err := presets.Load(path)
	if err != nil {
		t.Fatalf("presets.Load: %v", err)
	}
	return &Server{
		presets: store,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, path
}

func renamePreset(t *testing.T, s *Server, slot int, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("PATCH", fmt.Sprintf("/api/presets/%d", slot), strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handlePresetSlot(w, r)
	return w
}

// The point of #812: the station keeps playing, only the label changes, and the
// new label is still there after the speaker reboots.
func TestPresetRenameKeepsTheStationAndSurvivesARestart(t *testing.T) {
	s, path := newRenameServer(t)
	if err := s.presets.SetSlot(presets.Preset{
		Slot: 3, Name: "RADIO PARADISE (MAIN MIX) 320K AAC", Type: "radio",
		StreamURL: "http://stream.example/rp-main", Codec: "AAC+", Bitrate: 320,
	}); err != nil {
		t.Fatalf("seed preset: %v", err)
	}
	w := renamePreset(t, s, 3, `{"name":"Radio Paradise"}`)
	if w.Code != 200 {
		t.Fatalf("rename: status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	p, ok := s.presets.Get(3)
	if !ok {
		t.Fatal("the preset is gone after a rename")
	}
	if p.Name != "Radio Paradise" {
		t.Errorf("name = %q, want Radio Paradise", p.Name)
	}
	if p.StreamURL != "http://stream.example/rp-main" || p.Codec != "AAC+" || p.Bitrate != 320 {
		t.Errorf("a rename changed more than the name: %+v", p)
	}
	// What the user notices: the speaker comes back up with the name they typed.
	reloaded, err := presets.Load(path)
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}
	rp, ok := reloaded.Get(3)
	if !ok || rp.Name != "Radio Paradise" {
		t.Errorf("after a restart the store holds %+v, ok=%v; want the new name", rp, ok)
	}
}

// The trap named in #836: the save path refuses a write whose station already
// sits on another key. A store written before that guard existed can legally
// hold the same station twice, and renaming one of those keys must still work:
// the rename is not a save and never asks that question.
func TestPresetRenameIsNotRefusedWhenTheStationIsOnTwoKeys(t *testing.T) {
	s, _ := newRenameServer(t)
	const url = "http://stream.example/absolut-relax"
	for _, slot := range []int{1, 5} {
		if err := s.presets.SetSlot(presets.Preset{
			Slot: slot, Name: "ABSOLUT RELAX", Type: "radio", StreamURL: url,
		}); err != nil {
			t.Fatalf("seed slot %d: %v", slot, err)
		}
	}
	w := renamePreset(t, s, 5, `{"name":"Relax"}`)
	if w.Code != 200 {
		t.Fatalf("rename of a doubly-held station: status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if p, _ := s.presets.Get(5); p.Name != "Relax" {
		t.Errorf("slot 5 name = %q, want Relax", p.Name)
	}
	// The other key is not touched, neither its name nor its station.
	other, _ := s.presets.Get(1)
	if other.Name != "ABSOLUT RELAX" || other.StreamURL != url {
		t.Errorf("renaming one key changed the other: %+v", other)
	}
}

// A Spotify preset renames like any other: its URI and account must survive.
func TestPresetRenameKeepsSpotifyIdentity(t *testing.T) {
	s, _ := newRenameServer(t)
	if err := s.presets.SetSlot(presets.Preset{
		Slot: 2, Name: "Spotify", Type: "spotify",
		URI: "spotify:playlist:37i9dQZF1DWX7rdRjOECPW", Account: "user@example.com",
	}); err != nil {
		t.Fatalf("seed preset: %v", err)
	}
	if w := renamePreset(t, s, 2, `{"name":"Sunday morning"}`); w.Code != 200 {
		t.Fatalf("rename: status = %d (body %s)", w.Code, w.Body.String())
	}
	p, _ := s.presets.Get(2)
	if p.Name != "Sunday morning" || p.Type != "spotify" ||
		p.URI != "spotify:playlist:37i9dQZF1DWX7rdRjOECPW" || p.Account != "user@example.com" {
		t.Errorf("stored preset = %+v, want only the name changed", p)
	}
}

// An empty (or blank) name would leave a key with nothing to identify it by, so
// it is refused and the stored name stays.
func TestPresetRenameRefusesAnEmptyName(t *testing.T) {
	s, _ := newRenameServer(t)
	if err := s.presets.SetSlot(presets.Preset{
		Slot: 4, Name: "Deutschlandfunk", Type: "radio", StreamURL: "http://stream.example/dlf",
	}); err != nil {
		t.Fatalf("seed preset: %v", err)
	}
	w := renamePreset(t, s, 4, `{"name":"   "}`)
	if w.Code != 422 {
		t.Fatalf("blank name: status = %d, want 422 (body %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "name-empty") {
		t.Errorf("want code name-empty, got %s", w.Body.String())
	}
	if p, _ := s.presets.Get(4); p.Name != "Deutschlandfunk" {
		t.Errorf("a refused rename changed the name to %q", p.Name)
	}
}

// Renaming a key that holds nothing is a 404, not a way to create a preset with
// no station on it.
func TestPresetRenameOnAnEmptyKeyIsNotFound(t *testing.T) {
	s, _ := newRenameServer(t)
	if w := renamePreset(t, s, 6, `{"name":"Nothing here"}`); w.Code != 404 {
		t.Fatalf("rename of an empty key: status = %d, want 404 (body %s)", w.Code, w.Body.String())
	}
	if _, ok := s.presets.Get(6); ok {
		t.Error("a rename created a preset on an empty key")
	}
}

// The cap counts CHARACTERS. A byte cap would cut a Japanese name after about
// 21 of them and could split a character in half.
func TestPresetRenameShortensAnOverlongNameByRunes(t *testing.T) {
	s, _ := newRenameServer(t)
	if err := s.presets.SetSlot(presets.Preset{
		Slot: 1, Name: "old", Type: "radio", StreamURL: "http://stream.example/one",
	}); err != nil {
		t.Fatalf("seed preset: %v", err)
	}
	long := strings.Repeat("ラジオ", 40) // 120 runes, 360 bytes
	if w := renamePreset(t, s, 1, `{"name":"`+long+`"}`); w.Code != 200 {
		t.Fatalf("long name: status = %d (body %s)", w.Code, w.Body.String())
	}
	p, _ := s.presets.Get(1)
	if got := len([]rune(p.Name)); got != maxPresetNameRunes {
		t.Errorf("stored name is %d runes, want %d", got, maxPresetNameRunes)
	}
	if !strings.HasPrefix(long, p.Name) {
		t.Errorf("the shortened name %q is not a prefix of what was sent", p.Name)
	}
}
