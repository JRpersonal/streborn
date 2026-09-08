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

// PUT /api/presets writes a WHOLE preset set in one request. The box-to-box
// transfer used to PUT the slots one at a time, so the target judged every
// write against a half-written store: two speakers holding the same six
// stations in a DIFFERENT ORDER collided with themselves and the transfer died
// with a raw 409 in the user's face (#882).

// newBulkServer returns a Server with a real (temp-dir) preset store, plus the
// store path so a test can read back exactly what landed on "NAND".
func newBulkServer(t *testing.T) (*Server, *presets.Store) {
	t.Helper()
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatal(err)
	}
	return &Server{
		presets: store,
		logger:  slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	}, store
}

// bulkPut sends a set to PUT /api/presets through the collection handler, so
// the method dispatch is covered too.
func bulkPut(t *testing.T, s *Server, set []presets.Preset) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/presets", bytes.NewReader(body))
	req.RemoteAddr = "192.0.2.10:51234"
	s.handlePresets(rec, req)
	return rec
}

// radioPreset is the shape a transfer carries for an internet-radio key.
func radioPreset(slot int, name, url string) presets.Preset {
	return presets.Preset{Slot: slot, Name: name, StreamURL: url, Type: "radio"}
}

// slotNames maps the store to slot -> name for readable assertions.
func slotNames(store *presets.Store) map[int]string {
	out := map[int]string{}
	for _, p := range store.All() {
		out[p.Slot] = p.Name
	}
	return out
}

// TestBulkPresetPutAcceptsTheSameStationsInADifferentOrder is the reported
// case: both speakers already hold the same six stations, the source has them
// in another order, and the transfer is a pure re-ordering. Every slot must
// land and the request must answer 200.
func TestBulkPresetPutAcceptsTheSameStationsInADifferentOrder(t *testing.T) {
	s, store := newBulkServer(t)
	names := []string{"101 SMOOTH JAZZ", "B", "C", "D", "E", "Exclusively Rush"}
	for i, n := range names {
		if err := store.SetSlot(radioPreset(i+1, n, "http://example.com/"+n)); err != nil {
			t.Fatal(err)
		}
	}
	// The source's order: slots 1 and 6 are swapped against the target's.
	var set []presets.Preset
	for i, n := range names {
		slot := i + 1
		switch slot {
		case 1:
			n = "Exclusively Rush"
		case 6:
			n = "101 SMOOTH JAZZ"
		}
		set = append(set, radioPreset(slot, n, "http://example.com/"+n))
	}

	rec := bulkPut(t, s, set)
	if rec.Code != http.StatusOK {
		t.Fatalf("a pure re-ordering answered %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Written int   `json:"written"`
		Slots   []int `json:"slots"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("answer is not the documented JSON: %v (%s)", err, rec.Body.String())
	}
	if out.Written != 6 {
		t.Errorf("reported %d written, want 6", out.Written)
	}
	if len(out.Slots) != 6 {
		t.Errorf("reported slots %v, want all six", out.Slots)
	}
	got := slotNames(store)
	if got[1] != "Exclusively Rush" || got[6] != "101 SMOOTH JAZZ" {
		t.Errorf("the swap did not land: slot 1 = %q, slot 6 = %q", got[1], got[6])
	}
	if len(got) != 6 {
		t.Errorf("store holds %d slots, want 6", len(got))
	}
}

// TestBulkPresetPutRefusesTheSameStationTwiceAndChangesNothing: a set that puts
// one station on two keys is a genuine collision. It is refused, and because
// the write is all-or-nothing the store must be byte-for-byte what it was.
func TestBulkPresetPutRefusesTheSameStationTwiceAndChangesNothing(t *testing.T) {
	s, store := newBulkServer(t)
	if err := store.SetSlot(radioPreset(1, "Old One", "http://example.com/old1")); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSlot(radioPreset(2, "Old Two", "http://example.com/old2")); err != nil {
		t.Fatal(err)
	}
	before := slotNames(store)

	rec := bulkPut(t, s, []presets.Preset{
		radioPreset(1, "Jazz", "http://example.com/jazz"),
		radioPreset(2, "Jazz again", "http://example.com/jazz"),
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("one station on two keys answered %d, want 409: %s", rec.Code, rec.Body.String())
	}
	var c struct {
		Code string `json:"code"`
		Name string `json:"name"`
		Slot int    `json:"slot"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatalf("409 body is not the documented JSON: %v (%s)", err, rec.Body.String())
	}
	if c.Code != "already-on-slot" || c.Slot != 2 || c.Name != "Jazz again" {
		t.Errorf("409 body = %+v, want code already-on-slot naming the other key (slot 2)", c)
	}
	if got := slotNames(store); len(got) != len(before) || got[1] != before[1] || got[2] != before[2] {
		t.Errorf("a refused set changed the store: before %v, after %v", before, got)
	}
}

// TestBulkPresetPutRefusesAStationThatSitsOnASlotOutsideThePayload: the
// duplicate rule is applied to the RESULT, so a key the set does not rewrite
// still counts. Nothing may be deleted to make room (the loss-free promise of
// #836) and nothing may be written.
func TestBulkPresetPutRefusesAStationThatSitsOnASlotOutsideThePayload(t *testing.T) {
	s, store := newBulkServer(t)
	if err := store.SetSlot(radioPreset(5, "Keeper", "http://example.com/keeper")); err != nil {
		t.Fatal(err)
	}
	before := slotNames(store)

	rec := bulkPut(t, s, []presets.Preset{
		radioPreset(1, "New One", "http://example.com/new1"),
		radioPreset(2, "Keeper (copy)", "http://example.com/keeper"),
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("answered %d, want 409: %s", rec.Code, rec.Body.String())
	}
	var c struct {
		Code string `json:"code"`
		Name string `json:"name"`
		Slot int    `json:"slot"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatalf("409 body is not the documented JSON: %v (%s)", err, rec.Body.String())
	}
	if c.Code != "already-on-slot" || c.Slot != 5 || c.Name != "Keeper" {
		t.Errorf("409 body = %+v, want it to name the untouched key 5 and its station", c)
	}
	got := slotNames(store)
	if len(got) != len(before) || got[5] != "Keeper" {
		t.Errorf("a refused set changed the store: before %v, after %v", before, got)
	}
	if _, ok := got[1]; ok {
		t.Error("slot 1 was written even though the set was refused")
	}
}

// TestBulkPresetPutLeavesSlotsTheSetDoesNotName: a partial set rewrites only
// what it carries.
func TestBulkPresetPutLeavesSlotsTheSetDoesNotName(t *testing.T) {
	s, store := newBulkServer(t)
	if err := store.SetSlot(radioPreset(4, "Untouched", "http://example.com/untouched")); err != nil {
		t.Fatal(err)
	}
	rec := bulkPut(t, s, []presets.Preset{radioPreset(1, "New", "http://example.com/new")})
	if rec.Code != http.StatusOK {
		t.Fatalf("answered %d: %s", rec.Code, rec.Body.String())
	}
	got := slotNames(store)
	if got[4] != "Untouched" || got[1] != "New" {
		t.Errorf("store = %v, want slot 4 kept and slot 1 written", got)
	}
}

// TestBulkPresetPutRejectsAMalformedSet covers the structural gates: too many
// entries, an impossible slot, the same slot twice, and a preset with nothing
// playable in it (which would store a key that can never play, #252).
func TestBulkPresetPutRejectsAMalformedSet(t *testing.T) {
	cases := []struct {
		name string
		set  []presets.Preset
		want int
	}{
		{"empty set", nil, http.StatusBadRequest},
		{"seven presets", []presets.Preset{
			radioPreset(1, "a", "http://example.com/1"), radioPreset(2, "b", "http://example.com/2"),
			radioPreset(3, "c", "http://example.com/3"), radioPreset(4, "d", "http://example.com/4"),
			radioPreset(5, "e", "http://example.com/5"), radioPreset(6, "f", "http://example.com/6"),
			radioPreset(6, "g", "http://example.com/7"),
		}, http.StatusBadRequest},
		{"slot out of range", []presets.Preset{radioPreset(7, "a", "http://example.com/1")}, http.StatusBadRequest},
		{"same slot twice", []presets.Preset{
			radioPreset(1, "a", "http://example.com/1"), radioPreset(1, "b", "http://example.com/2"),
		}, http.StatusBadRequest},
		{"nothing playable", []presets.Preset{radioPreset(1, "a", "")}, http.StatusUnprocessableEntity},
		{"self-proxy stream", []presets.Preset{radioPreset(1, "a", "http://127.0.0.1:8888/stream/3")}, http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, store := newBulkServer(t)
			rec := bulkPut(t, s, tc.set)
			if rec.Code != tc.want {
				t.Fatalf("answered %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if len(store.All()) != 0 {
				t.Errorf("a refused set wrote %d slots", len(store.All()))
			}
		})
	}
}

// TestBulkPresetPutAcceptsSpotifyAndQueuePresets: a transfer carries whatever
// the source holds. Spotify keys collide on their context URI; folder keys
// carry no station at all and must never collide with each other.
func TestBulkPresetPutAcceptsSpotifyAndQueuePresets(t *testing.T) {
	s, store := newBulkServer(t)
	rec := bulkPut(t, s, []presets.Preset{
		{Slot: 1, Name: "Chill", Type: "spotify", URI: "spotify:playlist:abc"},
		{Slot: 2, Name: "Folder A", Type: "queue", Items: []presets.PresetItem{{URL: "http://example.com/a.mp3"}}},
		{Slot: 3, Name: "Folder B", Type: "queue", Items: []presets.PresetItem{{URL: "http://example.com/b.mp3"}}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("answered %d: %s", rec.Code, rec.Body.String())
	}
	if len(store.All()) != 3 {
		t.Fatalf("store holds %d slots, want 3", len(store.All()))
	}
	// The same playlist on two keys is still a collision.
	s2, _ := newBulkServer(t)
	rec = bulkPut(t, s2, []presets.Preset{
		{Slot: 1, Name: "Chill", Type: "spotify", URI: "spotify:playlist:abc"},
		{Slot: 2, Name: "Chill copy", Type: "spotify", URI: "spotify:playlist:abc"},
	})
	if rec.Code != http.StatusConflict {
		t.Errorf("the same playlist on two keys answered %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

// TestPresetCollectionStillRefusesOtherMethods keeps the older-agent contract
// the desktop fallback relies on: anything that is not GET or PUT is a 405.
func TestPresetCollectionStillRefusesOtherMethods(t *testing.T) {
	s, _ := newBulkServer(t)
	rec := httptest.NewRecorder()
	s.handlePresets(rec, httptest.NewRequest(http.MethodPost, "/api/presets", strings.NewReader("[]")))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST answered %d, want 405", rec.Code)
	}
}
