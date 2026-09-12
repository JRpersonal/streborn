package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JRpersonal/streborn/internal/zones"
)

// A permanent group is a SAVED arrangement, not the live zone. Taking the live
// zone apart with Ungroup must leave the saved one alone: it forms itself again
// when its main speaker next plays, and a user who presses Ungroup for the
// evening has not asked to throw the group away.
//
// The guard existed on every other path that clears the store, the peer purge
// and the standby doubt clear, and not on the one behind the button. It took a
// stored group off the maintainer's own fleet during a test on 2026-09-03, and
// it is the first thing a reporter suspects when a group is missing after an
// update, which is how it surfaced again on 2026-09-09.
func TestUngroupKeepsAPermanentGroupSaved(t *testing.T) {
	group := zones.Zone{
		Master: "DEV-MASTER", MasterIP: "192.0.2.10", Permanent: true,
		Slaves: []zones.Member{
			{DeviceID: "DEV-A", IP: "192.0.2.20"},
			{DeviceID: "DEV-B", IP: "192.0.2.21"},
		},
	}
	s, st := staleDocServer(t, "192.0.2.10", group, true)

	req := httptest.NewRequest(http.MethodDelete, "/api/box/zone", nil)
	w := httptest.NewRecorder()
	s.handleZoneDissolve(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !storedGroup(t, st) {
		t.Fatal("Ungroup deleted the saved permanent group; it can only be rebuilt by hand from there")
	}
}

// An ordinary group carries no such promise: it was live and now it is not, so
// the document goes with it. Without this the guard above would quietly turn
// every dissolve into a no-op on the store and leave stale documents behind.
func TestUngroupStillClearsAnOrdinaryGroup(t *testing.T) {
	group := zones.Zone{
		Master: "DEV-MASTER", MasterIP: "192.0.2.10",
		Slaves: []zones.Member{{DeviceID: "DEV-A", IP: "192.0.2.20"}},
	}
	s, st := staleDocServer(t, "192.0.2.10", group, true)

	req := httptest.NewRequest(http.MethodDelete, "/api/box/zone", nil)
	w := httptest.NewRecorder()
	s.handleZoneDissolve(w, req)

	if storedGroup(t, st) {
		t.Fatal("a plain group survived Ungroup in the store; only a permanent group is kept")
	}
}
