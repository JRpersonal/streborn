package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
)

// The second preset row (#1110): slots 7..12 are stored and served exactly like
// 1..6 by every preset endpoint, and never reach the speaker's own list.

func TestSecondRowSlotsAreStoredLikeTheFirst(t *testing.T) {
	s := newSaveGateServer(t)
	for _, slot := range []int{7, 9, 12} {
		body := fmt.Sprintf(`{"name":"Row two %d","type":"radio","stream_url":"http://stream.example/r%d"}`, slot, slot)
		if w := putPreset(t, s, slot, body); w.Code != http.StatusOK {
			t.Fatalf("PUT /api/presets/%d = %d %s", slot, w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/presets/9", nil)
	w := httptest.NewRecorder()
	s.handlePresetSlot(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Row two 9") {
		t.Fatalf("GET /api/presets/9 = %d %s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest(http.MethodGet, "/api/presets", nil)
	w = httptest.NewRecorder()
	s.handlePresets(w, r)
	var all []presets.Preset
	if err := json.Unmarshal(w.Body.Bytes(), &all); err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("GET /api/presets listed %d presets, want 3: %+v", len(all), all)
	}
	r = httptest.NewRequest(http.MethodDelete, "/api/presets/12", nil)
	w = httptest.NewRecorder()
	s.handlePresetSlot(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("DELETE /api/presets/12 = %d", w.Code)
	}
	if _, ok := s.presets.Get(12); ok {
		t.Error("slot 12 still stored after the delete")
	}
}

// The duplicate guard spans both rows: a station on row 2 is refused on row 1
// and the answer names the row-2 slot.
func TestDuplicateGuardNamesASecondRowSlot(t *testing.T) {
	s := newSaveGateServer(t)
	const relax = `{"name":"Relax","type":"radio","stream_url":"http://stream.example/relax"}`
	if w := putPreset(t, s, 9, relax); w.Code != http.StatusOK {
		t.Fatalf("seed slot 9: %d %s", w.Code, w.Body.String())
	}
	w := putPreset(t, s, 2, relax)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"slot":9`) {
		t.Fatalf("PUT slot 2 = %d %s, want 409 naming slot 9", w.Code, w.Body.String())
	}
}

func TestBulkPutTakesBothRows(t *testing.T) {
	s, store := newBulkServer(t)
	var set []presets.Preset
	for slot := 1; slot <= presets.MaxSlot; slot++ {
		set = append(set, radioPreset(slot, fmt.Sprintf("s%d", slot), fmt.Sprintf("http://example.com/%d", slot)))
	}
	if rec := bulkPut(t, s, set); rec.Code != http.StatusOK {
		t.Fatalf("twelve presets: %d %s", rec.Code, rec.Body.String())
	}
	if got := slotNames(store); len(got) != 12 || got[12] != "s12" {
		t.Fatalf("store after the bulk put: %v", got)
	}
	set = append(set, radioPreset(13, "s13", "http://example.com/13"))
	if rec := bulkPut(t, s, set); rec.Code != http.StatusBadRequest {
		t.Fatalf("thirteen presets: %d, want 400", rec.Code)
	}
}

func TestPresetMoveCrossesRows(t *testing.T) {
	s := newSaveGateServer(t)
	if w := putPreset(t, s, 2, `{"name":"Mover","type":"radio","stream_url":"http://stream.example/mover"}`); w.Code != http.StatusOK {
		t.Fatalf("seed: %d", w.Code)
	}
	move := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/box/preset-move", strings.NewReader(body))
		w := httptest.NewRecorder()
		s.handlePresetMove(w, r)
		return w
	}
	if w := move(`{"from":2,"to":11}`); w.Code != http.StatusOK {
		t.Fatalf("move 2 -> 11 = %d %s", w.Code, w.Body.String())
	}
	if p, ok := s.presets.Get(11); !ok || p.Name != "Mover" {
		t.Fatalf("slot 11 = %+v %v", p, ok)
	}
	if w := move(`{"from":11,"to":4}`); w.Code != http.StatusOK {
		t.Fatalf("move 11 -> 4 = %d %s", w.Code, w.Body.String())
	}
	if _, ok := s.presets.Get(11); ok {
		t.Error("slot 11 still holds the station after moving it back to row 1")
	}
}

// writeBoxPreset and writeStorePresetToBox are the two doors every save, move
// and swap uses to put a key on the speaker. A row-2 slot goes through neither:
// they answer nil at once, without dialling anything (the host here would
// time out).
func TestSecondRowNeverReachesTheSpeaker(t *testing.T) {
	s := newSaveGateServer(t)
	s.boxHost = "192.0.2.1"
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := s.writeBoxPreset(ctx, 8, "Row two", "http://127.0.0.1:8888/stream/8", "", false); err != nil {
		t.Errorf("writeBoxPreset(8) = %v, want a silent no-op", err)
	}
	if err := s.writeStorePresetToBox(ctx, presets.Preset{Slot: 12, Name: "Row two", Type: "spotify", URI: "spotify:playlist:x"}); err != nil {
		t.Errorf("writeStorePresetToBox(12) = %v, want a silent no-op", err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Error("a row-2 write tried to reach the speaker")
	}
}

func TestAgentVersionAdvertisesTwoPresetRows(t *testing.T) {
	s := quietServer("127.0.0.1")
	w := httptest.NewRecorder()
	s.handleAgentVersion(w, httptest.NewRequest(http.MethodGet, "/api/agent/version", nil))
	var out map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["presetRows"] != "2" {
		t.Fatalf(`presetRows = %q, want "2"`, out["presetRows"])
	}
}

// The now-playing key header names a row-2 Spotify slot as it is (7..12).
func TestPlayingKeyNamesASecondRowSlot(t *testing.T) {
	list := []presets.Preset{{Slot: 10, Type: "spotify", URI: "spotify:playlist:abc"}}
	if got := playingSpotifySlot("http://127.0.0.1:8888/spotify/stream-10.ogg", "", list); got != 10 {
		t.Fatalf("playingSpotifySlot = %d, want 10", got)
	}
}
