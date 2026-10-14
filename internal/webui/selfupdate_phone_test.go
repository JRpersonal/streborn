package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSelfUpdateOriginAllowedOnlyForLANPages(t *testing.T) {
	for origin, want := range map[string]bool{
		"http://192.168.1.20:8888":      true,
		"http://192.168.1.20":           true,
		"http://10.0.0.5:17008":         true,
		"http://172.16.4.2:8888":        true,
		"http://[fd00::12]:8888":        true,
		"http://kitchen:8888":           true,
		"http://kitchen.local:8888":     true,
		"":                              false,
		"null":                          false,
		"https://192.168.1.20:8888":     false, // the agent serves plain http only
		"http://192.168.1.20:8888/":     false,
		"http://192.168.1.20:8888/x":    false,
		"http://user@192.168.1.20:8888": false,
		"http://example.com":            false,
		"http://evil.example.local.com": false,
		"http://8.8.8.8:8888":           false,
		"http://127.0.0.1:8888":         false,
		"http://localhost:8888":         false,
		"http://[::1]:8888":             false,
		"file://":                       false,
		"wails://wails":                 false, // has its own grant, not this one
	} {
		if got := selfUpdateOriginAllowed(origin); got != want {
			t.Errorf("selfUpdateOriginAllowed(%q) = %v, want %v", origin, got, want)
		}
	}
}

// corsServe runs a request through the real middleware in front of two
// endpoints: the self-update one and an ordinary one.
func corsServe(t *testing.T, method, path, origin, remote string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"state": "idle"})
	}
	mux.HandleFunc(selfUpdateAPIPath, ok)
	mux.HandleFunc("/api/agent/version", ok)
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = remote
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	corsMiddleware(mux).ServeHTTP(rec, r)
	return rec
}

const lanCaller = "192.168.1.30:50000"

func TestSelfUpdateAnswersAnotherSpeakersPage(t *testing.T) {
	origin := "http://192.168.1.20:8888"
	rec := corsServe(t, http.MethodGet, selfUpdateAPIPath, origin, lanCaller, nil)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != origin {
		t.Fatalf("allow-origin %q, want the exact origin", got)
	}
	if rec.Header().Get("Vary") != "Origin" {
		t.Errorf("Vary %q", rec.Header().Get("Vary"))
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST, OPTIONS" {
		t.Errorf("methods %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type" {
		t.Errorf("headers %q", got)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status %d", rec.Code)
	}
}

func TestSelfUpdateGivesNoPermissionToOtherPages(t *testing.T) {
	for _, origin := range []string{"https://example.org", "http://evil.test", "null", "http://127.0.0.1:9999"} {
		rec := corsServe(t, http.MethodGet, selfUpdateAPIPath, origin, lanCaller, nil)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("origin %q got permission %q", origin, got)
		}
		pre := corsServe(t, http.MethodOptions, selfUpdateAPIPath, origin, lanCaller, map[string]string{
			"Access-Control-Request-Method": "POST",
		})
		if pre.Code != http.StatusForbidden || pre.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("preflight from %q: %d %q", origin, pre.Code, pre.Header().Get("Access-Control-Allow-Origin"))
		}
	}
	// A LAN page, but the caller itself is not on the LAN: no permission.
	rec := corsServe(t, http.MethodOptions, selfUpdateAPIPath, "http://192.168.1.20:8888", "203.0.113.9:4000", nil)
	if rec.Code != http.StatusForbidden || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("preflight from outside the LAN: %d", rec.Code)
	}
}

func TestOnlyTheSelfUpdateEndpointOpensToSpeakerPages(t *testing.T) {
	origin := "http://192.168.1.20:8888"
	rec := corsServe(t, http.MethodGet, "/api/agent/version", origin, lanCaller, nil)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("another endpoint answered a speaker page with %q", got)
	}
	pre := corsServe(t, http.MethodOptions, "/api/agent/version", origin, lanCaller, nil)
	if pre.Code != http.StatusForbidden || pre.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("preflight on another endpoint: %d", pre.Code)
	}
	// A path that merely starts like it is not it.
	rec = corsServe(t, http.MethodGet, selfUpdateAPIPath+"/x", origin, lanCaller, nil)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("a longer path got permission %q", got)
	}
}

func TestSelfUpdatePreflightAnswersPrivateNetworkAccess(t *testing.T) {
	origin := "http://192.168.1.20:8888"
	rec := corsServe(t, http.MethodOptions, selfUpdateAPIPath, origin, lanCaller, map[string]string{
		"Access-Control-Request-Method":          "POST",
		"Access-Control-Request-Headers":         "content-type",
		"Access-Control-Request-Private-Network": "true",
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != origin {
		t.Errorf("allow-origin %q", rec.Header().Get("Access-Control-Allow-Origin"))
	}
	if rec.Header().Get("Access-Control-Allow-Private-Network") != "true" {
		t.Error("Private Network Access preflight not answered")
	}
	// Without the question, no PNA answer.
	rec = corsServe(t, http.MethodOptions, selfUpdateAPIPath, origin, lanCaller, map[string]string{"Access-Control-Request-Method": "POST"})
	if rec.Header().Get("Access-Control-Allow-Private-Network") != "" {
		t.Error("PNA header sent unasked")
	}
}

// A missing CORS header only stops a page from reading. A simple POST from a
// foreign page would still act, so the handler refuses it outright.
func TestSelfUpdateRefusesAPostFromAForeignPage(t *testing.T) {
	h := newFleetHarness(t, "v1.0.13", nil)
	for origin, want := range map[string]int{
		"https://example.org":      http.StatusForbidden,
		"null":                     http.StatusForbidden,
		"http://192.168.1.20:8888": http.StatusOK, // another speaker's page
		"http://192.168.1.5:8888":  http.StatusOK, // this speaker's own page
		"":                         http.StatusOK, // no browser involved
	} {
		r := httptest.NewRequest(http.MethodPost, selfUpdateAPIPath, strings.NewReader(`{"action":"run-end","token":"aaaaaaaaaaaaaaaa"}`))
		r.RemoteAddr = lanCaller
		r.Host = "192.168.1.5:8888"
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.s.handleSelfUpdate(rec, r)
		if rec.Code != want {
			t.Errorf("origin %q: %d, want %d", origin, rec.Code, want)
		}
	}
}

func postSelfUpdate(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, selfUpdateAPIPath, strings.NewReader(body))
	r.RemoteAddr = lanCaller
	rec := httptest.NewRecorder()
	s.handleSelfUpdate(rec, r)
	return rec
}

const (
	tokA = "phoneAAAAAAAAAAAAAAAA"
	tokB = "phoneBBBBBBBBBBBBBBBB"
)

// One phone at a time: a second phone cannot claim a run that is going, and
// nobody without the token can install on the speaker that serves it.
func TestPhoneRunKeepsASecondDriverOut(t *testing.T) {
	h := newFleetHarness(t, "v1.0.12", nil)
	if rec := postSelfUpdate(t, h.s, `{"action":"run-claim","token":"`+tokA+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("claim %d", rec.Code)
	}
	if rec := postSelfUpdate(t, h.s, `{"action":"run-claim","token":"`+tokA+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("renewal %d", rec.Code)
	}
	if rec := postSelfUpdate(t, h.s, `{"action":"run-claim","token":"`+tokB+`"}`); rec.Code != http.StatusConflict {
		t.Fatalf("second phone claimed: %d", rec.Code)
	}
	for _, body := range []string{
		`{"action":"install"}`,
		`{"action":"install","token":"` + tokB + `"}`,
		`{"action":"engine"}`,
		`{"action":"install-all"}`,
	} {
		if rec := postSelfUpdate(t, h.s, body); rec.Code != http.StatusConflict {
			t.Errorf("%s during a phone run: %d", body, rec.Code)
		}
	}
	// The status says a run is going, and never shows the token.
	r := httptest.NewRequest(http.MethodGet, selfUpdateAPIPath, nil)
	r.RemoteAddr = lanCaller
	rec := httptest.NewRecorder()
	h.s.handleSelfUpdate(rec, r)
	if !strings.Contains(rec.Body.String(), `"phoneRun"`) || strings.Contains(rec.Body.String(), tokA) {
		t.Errorf("status %s", rec.Body.String())
	}
	if rec := postSelfUpdate(t, h.s, `{"action":"run-end","token":"`+tokB+`"}`); rec.Code != http.StatusOK || !h.s.phoneRun.heldByOther(tokB, time.Now()) {
		t.Error("another phone ended the run")
	}
	postSelfUpdate(t, h.s, `{"action":"run-end","token":"`+tokA+`"}`)
	if rec := postSelfUpdate(t, h.s, `{"action":"run-claim","token":"`+tokB+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("claim after the end: %d", rec.Code)
	}
	if rec := postSelfUpdate(t, h.s, `{"action":"run-claim","token":"short"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("a junk token: %d", rec.Code)
	}
}

func TestPhoneRunExpiresWhenThePhoneIsGone(t *testing.T) {
	var p phoneRun
	now := time.Now()
	if !p.claim(tokA, now) {
		t.Fatal("claim")
	}
	if p.claim(tokB, now.Add(phoneRunTTL-time.Second)) {
		t.Fatal("claimed over a live run")
	}
	if !p.claim(tokB, now.Add(phoneRunTTL+time.Second)) {
		t.Fatal("an abandoned run blocks for good")
	}
}

// While this speaker drives a run on its own, no phone can claim one.
func TestPhoneRunCannotStartDuringASpeakerRun(t *testing.T) {
	h := newFleetHarness(t, "v1.0.12", nil)
	h.s.fleet.run = &fleetRun{Mode: "running"}
	if rec := postSelfUpdate(t, h.s, `{"action":"run-claim","token":"`+tokA+`"}`); rec.Code != http.StatusConflict {
		t.Fatalf("claim during a speaker run: %d", rec.Code)
	}
}

// The hand-over: the phone could not reach one peer and asks this speaker to
// update just that one. The other peer and this speaker stay untouched.
func TestHandOverUpdatesOnlyTheGivenPeers(t *testing.T) {
	h := newFleetHarness(t, "v1.0.12", map[string]*fakePeer{
		"10.0.0.2": {name: "Kitchen", version: "v1.0.12", behaviour: "success"},
		"10.0.0.3": {name: "Bath", version: "v1.0.12", behaviour: "success"},
	})
	postSelfUpdate(t, h.s, `{"action":"run-claim","token":"`+tokA+`"}`)
	if rec := postSelfUpdate(t, h.s, `{"action":"install-all","token":"`+tokA+`","peers":["192.168.77.77"]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("a peer that is not on the list: %d", rec.Code)
	}
	if rec := postSelfUpdate(t, h.s, `{"action":"install-all","token":"`+tokA+`","peers":["10.0.0.3"]}`); rec.Code != http.StatusAccepted {
		t.Fatalf("hand-over %d %s", rec.Code, rec.Body.String())
	}
	// The phone keeps its claim while the speaker works.
	if rec := postSelfUpdate(t, h.s, `{"action":"run-claim","token":"`+tokA+`"}`); rec.Code != http.StatusOK {
		t.Errorf("renewal during the hand-over: %d", rec.Code)
	}
	if rec := postSelfUpdate(t, h.s, `{"action":"run-claim","token":"`+tokB+`"}`); rec.Code != http.StatusConflict {
		t.Errorf("another phone during the hand-over: %d", rec.Code)
	}
	deadline := time.Now().Add(10 * time.Second)
	for h.s.fleet.running() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	run := h.s.fleet.snapshot()
	if run == nil || run.Mode != "finished" || len(run.Speakers) != 1 {
		t.Fatalf("run %+v", run)
	}
	if r := run.Speakers[0]; r.Name != "Bath" || r.State != fleetDone || r.Self {
		t.Errorf("row %+v", r)
	}
	if got := strings.Join(h.order, ","); got != "Bath" {
		t.Errorf("installed %q", got)
	}
	if h.s.selfUpd.isBusy() {
		t.Error("the hand-over updated this speaker too")
	}
}
