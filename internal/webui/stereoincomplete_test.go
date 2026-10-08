package webui

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

func newStereoTestServer() *Server {
	return &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func brokenPair() *boxapi.Group {
	return &boxapi.Group{
		ID: "895814", MasterDeviceID: "DEV#MASTER", Status: "GROUP_ERROR",
		Members: []boxapi.ZoneMember{
			{DeviceID: "DEV#SELF", IP: "192.0.2.26", Role: "LEFT"},
			{DeviceID: "DEV#MASTER", IP: "192.0.2.25", Role: "RIGHT"},
		},
	}
}

func TestIncompletePairRefusesOnTheFirmwaresGroupError(t *testing.T) {
	s := newStereoTestServer()
	s.noteStereoSeen(brokenPair())
	p, ok := s.incompletePairReason(time.Now())
	if !ok || p.Reason != "group-error" || p.Master != "DEV#MASTER" || len(p.Members) != 2 {
		t.Fatalf("incompletePairReason = %+v, %v; want group-error naming the master and both members", p, ok)
	}
}

func TestAHealthyPairIsNeverRefused(t *testing.T) {
	// The half that holds a healthy pair's document is normal; refusing it would
	// take a working stereo setup away. Only GROUP_ERROR counts.
	s := newStereoTestServer()
	g := brokenPair()
	g.Status = "GROUP_OK"
	s.noteStereoSeen(g)
	if p, ok := s.incompletePairReason(time.Now()); ok {
		t.Fatalf("a healthy pair was refused: %+v", p)
	}
	g.Status = ""
	s.noteStereoSeen(g)
	if p, ok := s.incompletePairReason(time.Now()); ok {
		t.Fatalf("a pair without a status was refused: %+v", p)
	}
}

func TestAnOldPairReadDoesNotRefuse(t *testing.T) {
	s := newStereoTestServer()
	s.noteStereoSeen(brokenPair())
	if _, ok := s.incompletePairReason(time.Now().Add(stereoSeenFresh + time.Minute)); ok {
		t.Fatal("a pair read older than stereoSeenFresh still refused a recall")
	}
}

func TestAnsweringWithNoPairClearsTheRecord(t *testing.T) {
	s := newStereoTestServer()
	s.noteStereoSeen(brokenPair())
	s.noteStereoSeen(nil)
	if _, ok := s.incompletePairReason(time.Now()); ok {
		t.Fatal("a speaker that answered with no pair was still treated as stuck in one")
	}
}

func TestPartnerGoneRefusesEvenWithoutAGroupRead(t *testing.T) {
	s := newStereoTestServer()
	s.pairPartnerGone = func() (string, string) { return "192.0.2.25", "DEV#MASTER" }
	p, ok := s.incompletePairReason(time.Now())
	if !ok || p.Reason != "partner-gone" || p.PartnerIP != "192.0.2.25" {
		t.Fatalf("incompletePairReason = %+v, %v; want partner-gone", p, ok)
	}
}

func TestRefuseAnswers409WithTheReason(t *testing.T) {
	s := newStereoTestServer()
	s.noteStereoSeen(brokenPair())
	rec := httptest.NewRecorder()
	if !s.refuseIfIncompletePair(rec) {
		t.Fatal("refuseIfIncompletePair did not refuse a GROUP_ERROR pair")
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "stereo-incomplete" || body["master"] != "DEV#MASTER" {
		t.Fatalf("body = %v; want error=stereo-incomplete and the master", body)
	}
}

// #1208: a partner the agent start missed (network not up yet after a reboot)
// must stop blocking plays once it answers. The play path re-asks before it
// refuses.
func TestPartnerThatAnswersAgainIsNoLongerRefused(t *testing.T) {
	s := newStereoTestServer()
	gone := true
	s.pairPartnerGone = func() (string, string) {
		if gone {
			return "192.0.2.25", "DEV#MASTER"
		}
		return "", ""
	}
	rechecks := 0
	s.pairPartnerRecheck = func() (string, string) {
		rechecks++
		gone = false // the partner answers
		return s.pairPartnerGone()
	}
	rec := httptest.NewRecorder()
	if s.refuseIfIncompletePair(rec) {
		t.Fatalf("play refused although the partner answered the recheck: %s", rec.Body.String())
	}
	if rechecks != 1 {
		t.Fatalf("recheck ran %d times, want 1", rechecks)
	}
}

func TestPartnerThatStaysDownIsStillRefused(t *testing.T) {
	s := newStereoTestServer()
	s.pairPartnerGone = func() (string, string) { return "192.0.2.25", "DEV#MASTER" }
	s.pairPartnerRecheck = s.pairPartnerGone
	rec := httptest.NewRecorder()
	if !s.refuseIfIncompletePair(rec) {
		t.Fatal("play not refused although the partner is still gone")
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

// The zone endpoint is polled every few seconds; a partner that is really gone
// can take the full recheck timeout to fail. The zone read reports the cached
// verdict at once and lets the recheck run in the background.
func TestZoneReadDoesNotWaitForASlowRecheck(t *testing.T) {
	s := newStereoTestServer()
	s.pairPartnerGone = func() (string, string) { return "192.0.2.25", "DEV#MASTER" }
	started := make(chan struct{})
	release := make(chan struct{})
	s.pairPartnerRecheck = func() (string, string) {
		close(started)
		<-release // a probe that hangs until the test lets it go
		return "", ""
	}
	defer close(release)
	start := time.Now()
	ip, id := s.zonePartnerGone()
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("zone read took %s; it must not wait for the recheck", d)
	}
	if ip != "192.0.2.25" || id != "DEV#MASTER" {
		t.Fatalf("zonePartnerGone = %q, %q; want the cached verdict", ip, id)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the background recheck was never kicked")
	}
}

func TestZoneReadKicksNoRecheckWithoutARecord(t *testing.T) {
	s := newStereoTestServer()
	s.pairPartnerGone = func() (string, string) { return "", "" }
	s.pairPartnerRecheck = func() (string, string) {
		t.Error("recheck kicked although no partner is recorded as gone")
		return "", ""
	}
	if ip, _ := s.zonePartnerGone(); ip != "" {
		t.Fatalf("zonePartnerGone = %q, want empty", ip)
	}
}

func TestPlaySlotRefusesBeforeWaking(t *testing.T) {
	// The whole point: no wake, no lock held for seconds, an immediate answer.
	s := newStereoTestServer()
	s.noteStereoSeen(brokenPair())
	rec := httptest.NewRecorder()
	start := time.Now()
	if !s.refuseIfIncompletePair(rec) {
		t.Fatal("not refused")
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("refusal took %s; it must not touch the speaker", d)
	}
}
