package webui

// The stereo flag on a group extend (zoneformextend.go). Helpers from
// zoneformpairs_test.go.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zones"
)

// The phone remote extends a group from a pair half and sends the half's own
// "stereo" back. That used to re-pair the speakers; it is a group form now,
// and as the half cannot lead, it is refused with the reason.
func TestAStereoFlagOnAGroupExtendDoesNotPair(t *testing.T) {
	t.Parallel()
	s := pgServer(t, pgLeftIP, map[string]boxapi.Group{pgLeftIP: pgPair(), pgRightIP: pgPair()})
	s.rememberMemberType(pgOtherIP, "SoundTouch 30")

	one := pgRun(pgLeft, true, pgOther)
	ans := s.zoneFormPairs(one)
	if one.req.Stereo {
		t.Fatal("the extend was still taken for a pairing")
	}
	if ans == nil || ans.JSON["code"] != pairCannotLeadCode {
		t.Fatalf("answer = %+v, want the pair-cannot-lead refusal", ans)
	}

	// Two members: a pair has one partner, so this is a group whatever the models.
	two := pgRun(pgLeft, true, pgRight, pgOther)
	_ = s.zoneFormPairs(two)
	if two.req.Stereo {
		t.Fatal("a stereo request with two members was taken for a pairing")
	}
}

// Re-forming or editing the pair itself is still a pairing.
func TestReformingThePairStaysAPairing(t *testing.T) {
	t.Parallel()
	s := pgServer(t, pgLeftIP, map[string]boxapi.Group{pgLeftIP: pgPair(), pgRightIP: pgPair()})
	run := pgRun(pgLeft, true, pgRight)
	if ans := s.zoneFormPairs(run); ans != nil {
		t.Fatalf("re-forming the pair was refused: %+v", ans)
	}
	if !run.req.Stereo {
		t.Fatal("re-forming the pair lost its stereo flag")
	}

	// Two speakers that are not paired: the pair flow decides, untouched.
	free := pgServer(t, pgOtherIP, nil)
	fresh := pgRun(pgOther, true, pgLeft)
	if ans := free.zoneFormPairs(fresh); ans != nil || !fresh.req.Stereo {
		t.Fatalf("a fresh pairing was changed: ans=%+v stereo=%v", ans, fresh.req.Stereo)
	}
}

// The extend arrives at the handler: it must answer the refusal and never
// reach the pairing (which would dial /addGroup).
func TestTheHandlerAnswersAnExtendFromAPairHalfWithTheRefusal(t *testing.T) {
	t.Parallel()
	zs, err := zones.Load(filepath.Join(t.TempDir(), "zone.json"))
	if err != nil {
		t.Fatalf("zones.Load: %v", err)
	}
	s := pgServer(t, deadBoxHost, map[string]boxapi.Group{deadBoxHost: pgPair()})
	s.zones = zs
	body := `{"master":{"deviceID":"` + pgLeftID + `","ip":"127.0.0.1"},"slaves":[` +
		`{"deviceID":"` + pgOtherID + `","ip":"127.0.0.1"},{"deviceID":"BBBBBBBBBBBB","ip":"127.0.0.2"}],` +
		`"mode":"native","stereo":true,"name":"Kitchen"}`
	rec := httptest.NewRecorder()
	s.handleZoneForm(rec, httptest.NewRequest(http.MethodPost, "/api/box/zone", strings.NewReader(body)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body is not JSON: %q", rec.Body.String())
	}
	if out["code"] != pairCannotLeadCode || out["stereo"] != nil {
		t.Fatalf("body = %v, want the pair-cannot-lead refusal and no pairing answer", out)
	}
	if _, ok := s.zones.Get(); ok {
		t.Fatal("a refused form stored a document")
	}
}
