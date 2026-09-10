// #907, reported with a bundle that proves it twice over: three SoundTouch 10s
// in a plain multiroom group, no stereo pair anywhere, and four presses of
// "Undo stereo pair" each took the group apart while the app painted "Stereo
// pair undone". The stereo arm's own log line appears in the same bundle exactly
// once, the day before, when she really did have a pair.
//
// The cause was that ?stereo=1 only ADDED a path. The escalation that consults
// the pair authority carried `master.DeviceID == ""`, so on any speaker holding
// group state it was unreachable and the request fell through to the multiroom
// teardown.
//
// These tests pin the contract in both directions: under stereo intent, act
// only on a POSITIVELY confirmed pair, and never on a group. The box host is
// unroutable, as everywhere else in this package, so the firmware reads fail and
// the assertions are about what the handler decides on its own.

package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JRpersonal/streborn/internal/zones"
)

// undoPair drives DELETE /api/box/zone?stereo=1 and returns the decoded answer.
func undoPair(t *testing.T, s *Server) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/api/box/zone?stereo=1", nil)
	w := httptest.NewRecorder()
	s.handleZoneDissolve(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad JSON: %v (%s)", err, w.Body.String())
	}
	return out
}

func multiroomDoc(permanent bool) zones.Zone {
	return zones.Zone{
		Master: "DEV-MASTER", MasterIP: "192.0.2.10", Permanent: permanent,
		Slaves: []zones.Member{
			{DeviceID: "DEV-A", IP: "192.0.2.20"},
			{DeviceID: "DEV-B", IP: "192.0.2.21"},
		},
	}
}

// #907 itself. A plain multiroom group and no confirmed pair: refuse, report it
// as a no-op rather than a success, and leave the document alone.
func TestUndoStereoPairRefusesAMultiroomGroup(t *testing.T) {
	s, st := staleDocServer(t, "192.0.2.10", multiroomDoc(false), true)

	out := undoPair(t, s)
	if out["noPair"] != true || out["nothing"] != true {
		t.Fatalf("undo-pair on a group must report no pair, got %v", out)
	}
	if out["ok"] == true {
		t.Fatalf("a refused undo must not report ok:true, which is what the app read as success: %v", out)
	}
	if out["inZone"] != true {
		t.Fatalf("the refusal must say the speaker is in a zone, got %v", out)
	}
	if !storedGroup(t, st) {
		t.Fatal("undo-pair deleted the stored group document")
	}
}

// The hard project rule, pinned against the STEREO path specifically. The guard
// in the multiroom fall-through protects a permanent document there; this asserts
// the stereo path never gets that far.
func TestUndoStereoPairKeepsAPermanentGroupDocument(t *testing.T) {
	s, st := staleDocServer(t, "192.0.2.10", multiroomDoc(true), true)

	out := undoPair(t, s)
	if out["noPair"] != true {
		t.Fatalf("undo-pair on a permanent group must refuse, got %v", out)
	}
	if !storedGroup(t, st) {
		t.Fatal("undo-pair deleted a saved permanent group; it can only be rebuilt by hand from there")
	}
}

// An unreadable pair authority must refuse as well. /getGroup hangs rather than
// refuses on scm/BCO chassis, and treating a hang as "carry on" is what put the
// multiroom teardown behind this button. The unroutable host in this package
// makes every read fail, so this is the shape every test here exercises, and it
// must never come out as a teardown.
func TestUndoStereoPairRefusesWhenThePairCannotBeConfirmed(t *testing.T) {
	s, st := staleDocServer(t, "192.0.2.10", multiroomDoc(false), true)

	out := undoPair(t, s)
	if out["pairReadable"] != false {
		t.Fatalf("the answer must say the pair read failed, got %v", out)
	}
	if out["noPair"] != true || out["ok"] == true {
		t.Fatalf("an unconfirmed pair must be refused, got %v", out)
	}
	if !storedGroup(t, st) {
		t.Fatal("an unconfirmed pair read cost the stored group document")
	}
}

// Regression, the direction that must keep working: a pair STR formed itself is
// persisted with Stereo=true and needs no read at all, so it still dissolves.
func TestUndoStereoPairStillDissolvesAPersistedPair(t *testing.T) {
	pair := zones.Zone{
		Master: "DEV-MASTER", MasterIP: "192.0.2.10", Stereo: true,
		Slaves: []zones.Member{{DeviceID: "DEV-SLAVE", IP: "192.0.2.20", Role: "RIGHT"}},
	}
	s, _ := staleDocServer(t, "192.0.2.10", pair, true)

	out := undoPair(t, s)
	if out["noPair"] == true {
		t.Fatalf("a persisted pair was refused: %v", out)
	}
	if out["stereo"] != true {
		t.Fatalf("a persisted pair must still be dissolved as a pair, got %v", out)
	}
}

// A speaker holding nothing at all must still reach the nothing-to-dissolve
// branch, because that is where a phantom marge pair record gets cleared. The
// refusal is scoped to group state for exactly this reason; refusing here too
// would have closed that escape hatch.
func TestUndoStereoPairOnAnEmptySpeakerStillReportsNothingToDissolve(t *testing.T) {
	s, _ := staleDocServer(t, "192.0.2.10", zones.Zone{}, false)

	out := undoPair(t, s)
	if out["nothing"] != true {
		t.Fatalf("an empty speaker must report nothing to dissolve, got %v", out)
	}
	if out["noPair"] == true {
		t.Fatalf("the empty speaker took the refusal path and skipped the marge cleanup: %v", out)
	}
}

// A plain Ungroup is unchanged by all of this: no ?stereo=1 means the multiroom
// path, and #843's guard still keeps a persisted pair intact.
func TestPlainUngroupIsUnaffectedByTheStereoGuard(t *testing.T) {
	pair := zones.Zone{
		Master: "DEV-MASTER", MasterIP: "192.0.2.10", Stereo: true,
		Slaves: []zones.Member{{DeviceID: "DEV-SLAVE", IP: "192.0.2.20", Role: "RIGHT"}},
	}
	s, st := staleDocServer(t, "192.0.2.10", pair, true)

	req := httptest.NewRequest(http.MethodDelete, "/api/box/zone", nil) // no ?stereo=1
	w := httptest.NewRecorder()
	s.handleZoneDissolve(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["stereoPairKept"] != true {
		t.Fatalf("a plain Ungroup on a pair must keep it, got %v", out)
	}
	if !storedGroup(t, st) {
		t.Fatal("a plain Ungroup deleted the stereo pair")
	}
}
