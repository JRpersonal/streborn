package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeHeldRefusal stands in for the marge stub's refusal record.
type fakeHeldRefusal struct {
	mu   sync.Mutex
	slot int // 0 = none
}

func (f *fakeHeldRefusal) last() (time.Time, int, string, string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.slot == 0 {
		return time.Time{}, 0, "", "", false
	}
	return time.Now().Add(-time.Minute), f.slot, "LOCAL_INTERNET_RADIO", "1LIVE", true
}

func (f *fakeHeldRefusal) clear(slot int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.slot == slot {
		f.slot = 0
	}
}

func agentStatus(t *testing.T, s *Server) map[string]string {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleAgentVersion(w, httptest.NewRequest(http.MethodGet, "/api/agent/version", nil))
	var out map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// A refused long press on a key stops being reported as soon as the app
// stores or deletes that key: the key's state is settled, and the banner used
// to stay for an hour after a save that had succeeded (#1217). A save on
// another key leaves it standing.
func TestHeldRefusalClearedBySaveOrDeleteOnThatSlot(t *testing.T) {
	s := newMoveServer(t)
	f := &fakeHeldRefusal{slot: 6}
	s.SetHeldRefusalFn(f.last)
	s.SetHeldRefusalClearFn(f.clear)

	if got := agentStatus(t, s)["holdRefusedSlot"]; got != "6" {
		t.Fatalf("holdRefusedSlot = %q, want 6 before any save", got)
	}

	if w := putPreset(t, s, 2, `{"name":"Absolut Relax","type":"radio","stream_url":"http://stream.example/relax"}`); w.Code != http.StatusOK {
		t.Fatalf("save on key 2: status %d (%s)", w.Code, w.Body.String())
	}
	if got := agentStatus(t, s)["holdRefusedSlot"]; got != "6" {
		t.Fatalf("a save on key 2 cleared the refusal for key 6 (holdRefusedSlot = %q)", got)
	}

	if w := putPreset(t, s, 6, `{"name":"WDR 5","type":"radio","stream_url":"http://stream.example/wdr5"}`); w.Code != http.StatusOK {
		t.Fatalf("save on key 6: status %d (%s)", w.Code, w.Body.String())
	}
	if got, ok := agentStatus(t, s)["holdRefusedSlot"]; ok {
		t.Fatalf("refusal still reported after key 6 was saved (holdRefusedSlot = %q)", got)
	}

	f.mu.Lock()
	f.slot = 6
	f.mu.Unlock()
	w := httptest.NewRecorder()
	s.handlePresetSlot(w, httptest.NewRequest(http.MethodDelete, "/api/presets/6", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete key 6: status %d (%s)", w.Code, w.Body.String())
	}
	if got, ok := agentStatus(t, s)["holdRefusedSlot"]; ok {
		t.Fatalf("refusal still reported after key 6 was deleted (holdRefusedSlot = %q)", got)
	}
}
