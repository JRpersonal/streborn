package webui

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
)

// A save whose station already sits on another key is refused, and that refusal
// stays (#836). What was missing is the way out: discussion #709 read the
// refusal as a bug and discussion #925 read it as "so I can only keep one
// Spotify playlist". POST /api/box/preset-move is the third option, and these
// tests describe it from the user's side: the station ends up on the key he
// pressed, the old key is free, and nothing is ever lost on the way.

func newMoveServer(t *testing.T) *Server {
	t.Helper()
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatalf("presets.Load: %v", err)
	}
	return &Server{
		presets: store,
		logger:  slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	}
}

func movePreset(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/box/preset-move", strings.NewReader(body))
	req.RemoteAddr = "192.0.2.10:51234"
	s.handlePresetMove(rec, req)
	return rec
}

// The reported flow end to end: the playlist on key 1 is saved onto key 3, the
// agent refuses because it is already on key 1, the user accepts the move, and
// key 3 now holds it while key 1 is free.
func TestPresetMoveTakesTheStationToTheKeyTheSaveWasRefusedFor(t *testing.T) {
	s := newMoveServer(t)
	if err := s.presets.SetSlot(presets.Preset{
		Slot: 1, Name: "Jens Chill", Type: "spotify",
		URI: "spotify:playlist:37i9dQZF1DWX7rdRjOECPW", Account: "listener", Shuffle: true,
	}); err != nil {
		t.Fatal(err)
	}
	refused := putPreset(t, s, 3, `{"name":"Jens Chill","type":"spotify","uri":"spotify:playlist:37i9dQZF1DWX7rdRjOECPW"}`)
	if refused.Code != http.StatusConflict {
		t.Fatalf("save onto key 3: status = %d, want 409 (body %s)", refused.Code, refused.Body.String())
	}

	rec := movePreset(t, s, `{"from":1,"to":3}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("move 1 -> 3: status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if _, still := s.presets.Get(1); still {
		t.Error("key 1 still holds the station after the move")
	}
	p, ok := s.presets.Get(3)
	if !ok {
		t.Fatal("key 3 is empty after the move")
	}
	if p.URI != "spotify:playlist:37i9dQZF1DWX7rdRjOECPW" {
		t.Errorf("moved preset URI = %q", p.URI)
	}
	// A move must carry the whole preset across, not a name and a URI: the
	// account decides which household member's login recalls it (#27) and the
	// shuffle flag is what the user chose when he saved it.
	if p.Name != "Jens Chill" || p.Account != "listener" || !p.Shuffle {
		t.Errorf("moved preset lost fields: %+v", p)
	}
	if p.Slot != 3 {
		t.Errorf("moved preset slot = %d, want 3", p.Slot)
	}
}

// The move overwrites whatever the target key held, because that is what the
// refused save was going to do anyway, and it must leave the other four keys
// alone.
func TestPresetMoveReplacesOnlyTheTwoKeysInvolved(t *testing.T) {
	s := newMoveServer(t)
	for _, p := range []presets.Preset{
		{Slot: 1, Name: "Keeper", Type: "radio", StreamURL: "http://stream.example/keeper"},
		{Slot: 2, Name: "Mover", Type: "radio", StreamURL: "http://stream.example/mover"},
		{Slot: 5, Name: "Overwritten", Type: "radio", StreamURL: "http://stream.example/old"},
	} {
		if err := s.presets.SetSlot(p); err != nil {
			t.Fatal(err)
		}
	}
	if rec := movePreset(t, s, `{"from":2,"to":5}`); rec.Code != http.StatusOK {
		t.Fatalf("move 2 -> 5: status = %d (body %s)", rec.Code, rec.Body.String())
	}
	got := map[int]string{}
	for _, p := range s.presets.All() {
		got[p.Slot] = p.Name
	}
	want := map[int]string{1: "Keeper", 5: "Mover"}
	if len(got) != len(want) {
		t.Fatalf("store after the move = %v, want %v", got, want)
	}
	for slot, name := range want {
		if got[slot] != name {
			t.Errorf("key %d = %q, want %q", slot, got[slot], name)
		}
	}
	// One station, one key: the moved station must not be left on the old key as
	// a copy, or the next save of it is refused all over again.
	dup := 0
	for _, p := range s.presets.All() {
		if p.StreamURL == "http://stream.example/mover" {
			dup++
		}
	}
	if dup != 1 {
		t.Errorf("the moved station sits on %d keys, want 1", dup)
	}
}

// A move is persisted as one write, so the station is on exactly one key in the
// file the speaker reads back after a reboot too.
func TestPresetMoveSurvivesAReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "presets.json")
	store, err := presets.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{presets: store, logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}
	if err := store.SetSlot(presets.Preset{Slot: 4, Name: "Mover", Type: "radio", StreamURL: "http://stream.example/mover"}); err != nil {
		t.Fatal(err)
	}
	if rec := movePreset(t, s, `{"from":4,"to":6}`); rec.Code != http.StatusOK {
		t.Fatalf("move 4 -> 6: status = %d (body %s)", rec.Code, rec.Body.String())
	}
	reloaded, err := presets.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, still := reloaded.Get(4); still {
		t.Error("the old key came back after a reload")
	}
	if p, ok := reloaded.Get(6); !ok || p.Name != "Mover" {
		t.Errorf("reloaded key 6 = %+v, ok=%v", p, ok)
	}
}

// An empty source key must not invent a preset, and moving a key onto itself is
// a no-op the caller got wrong: both are refused before anything is written.
func TestPresetMoveRefusesWhatItCannotMove(t *testing.T) {
	s := newMoveServer(t)
	if err := s.presets.SetSlot(presets.Preset{Slot: 2, Name: "Mover", Type: "radio", StreamURL: "http://stream.example/mover"}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, body string
		want       int
	}{
		{"empty source key", `{"from":5,"to":1}`, http.StatusNotFound},
		{"same key", `{"from":2,"to":2}`, http.StatusBadRequest},
		{"slot out of range", `{"from":2,"to":9}`, http.StatusBadRequest},
		{"missing slots", `{}`, http.StatusBadRequest},
	}
	for _, c := range cases {
		if rec := movePreset(t, s, c.body); rec.Code != c.want {
			t.Errorf("%s: status = %d, want %d (body %s)", c.name, rec.Code, c.want, rec.Body.String())
		}
	}
	p, ok := s.presets.Get(2)
	if !ok || p.Name != "Mover" {
		t.Errorf("a refused move touched the store: %+v ok=%v", p, ok)
	}
}

// The answer carries the moved preset, so a client can paint the new key
// without a second read.
func TestPresetMoveAnswersWithTheMovedPreset(t *testing.T) {
	s := newMoveServer(t)
	if err := s.presets.SetSlot(presets.Preset{Slot: 1, Name: "Mover", Type: "radio", StreamURL: "http://stream.example/mover"}); err != nil {
		t.Fatal(err)
	}
	rec := movePreset(t, s, `{"from":1,"to":2}`)
	var got presets.Preset
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("answer is not a preset: %v (%s)", err, rec.Body.String())
	}
	if got.Slot != 2 || got.Name != "Mover" {
		t.Errorf("answer = %+v, want Mover on key 2", got)
	}
}

// A GET or PUT on the move route is not a move: only POST changes keys, so a
// stray reload of the URL cannot shuffle somebody's presets.
func TestPresetMoveIgnoresOtherMethods(t *testing.T) {
	s := newMoveServer(t)
	rec := httptest.NewRecorder()
	s.handlePresetMove(rec, httptest.NewRequest(http.MethodGet, "/api/box/preset-move", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/box/preset-move: status = %d, want 405", rec.Code)
	}
}

// The per-slot handler parses ANY word after /api/presets/ as a slot number and
// answers 400 "invalid slot, must be 1-6". That is why the move endpoint lives
// at /api/box/preset-move instead: an agent older than the endpoint would
// otherwise answer that same 400 to a move request, and the real endpoint
// produces that same sentence for a slot out of range, so the app could not
// tell an old agent from a real refusal. Guessing wrong runs a destructive
// two-step fallback, so the ambiguity was removed rather than guessed at.
// This test pins the behaviour the relocation exists for.
func TestPresetMoveIsNotASlotNumber(t *testing.T) {
	s := newMoveServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/box/preset-move", strings.NewReader(`{"from":1,"to":2}`))
	s.handlePresetSlot(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("the slot handler still parses a word as a slot: status = %d, want 400", rec.Code)
	}
}
