package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JRpersonal/streborn/internal/zones"
)

// #843: pressing "Ungroup" (a plain DELETE /api/box/zone, no ?stereo=1) on a
// household whose only group is a STR-formed stereo pair used to tear the pair
// apart. A STR pair persists Stereo=true, and the dissolve handler copied that
// flag straight out of the store, so it reached the RemoveGroup teardown without
// the ?stereo=1 intent gate ever being consulted. Ungroup must leave the pair
// intact (that is what "Undo stereo pair" is for) and report nothing to do.
func TestPlainUngroupLeavesAPersistedStereoPairIntact(t *testing.T) {
	pair := zones.Zone{
		Master: "DEV-MASTER", MasterIP: "192.0.2.10", Stereo: true,
		Slaves: []zones.Member{{DeviceID: "DEV-SLAVE", IP: "192.0.2.20", Role: "RIGHT"}},
	}
	s, st := staleDocServer(t, "192.0.2.10", pair, true)

	req := httptest.NewRequest(http.MethodDelete, "/api/box/zone", nil) // no ?stereo=1
	w := httptest.NewRecorder()
	s.handleZoneDissolve(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON: %v (%s)", err, w.Body.String())
	}
	if body["stereoPairKept"] != true || body["nothing"] != true {
		t.Fatalf("a plain Ungroup on a pair must report it was kept, got %v", body)
	}
	if !storedGroup(t, st) {
		t.Fatal("a plain Ungroup deleted the stereo pair; only ?stereo=1 (undo pair) may do that")
	}
}
