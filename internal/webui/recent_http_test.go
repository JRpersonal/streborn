package webui

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/recent"
)

func newRecentServer(t *testing.T) *Server {
	t.Helper()
	return &Server{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		recent: recent.New(),
	}
}

func recentRequest(t *testing.T, s *Server, method, query string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "/api/recent"+query, nil)
	w := httptest.NewRecorder()
	s.handleRecent(w, r)
	return w
}

func recentKeys(t *testing.T, s *Server) []string {
	t.Helper()
	w := recentRequest(t, s, http.MethodGet, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET status %d", w.Code)
	}
	var list []recent.Entry
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("json: %v", err)
	}
	out := []string{}
	for _, e := range list {
		out = append(out, e.CardKey+"/"+e.Track)
	}
	return out
}

// GET /api/recent lists a station once, at its newest listening, which is what
// both the desktop app and the phone page render (2026-10-19).
func TestRecentEndpointListsAStationOnce(t *testing.T) {
	s := newRecentServer(t)
	s.recent.Add(recent.Entry{Source: "radio", CardKey: "swr3", Track: "Epic", TS: "t1"})
	s.recent.Add(recent.Entry{Source: "radio", CardKey: "1live", Track: "Other", TS: "t2"})
	s.recent.Add(recent.Entry{Source: "radio", CardKey: "swr3", Track: "Reborn", TS: "t3"})

	got := strings.Join(recentKeys(t, s), ",")
	if got != "1live/Other,swr3/Reborn" {
		t.Fatalf("GET = %s, want 1live/Other,swr3/Reborn", got)
	}
}

// DELETE with cardKey+ts removes that station only; the explicit all=1 clears;
// a bare DELETE is refused rather than clearing everything.
func TestRecentEndpointDeleteAndClear(t *testing.T) {
	s := newRecentServer(t)
	s.recent.Add(recent.Entry{Source: "radio", CardKey: "swr3", Track: "Epic", TS: "t1"})
	s.recent.Add(recent.Entry{Source: "radio", CardKey: "swr3", Track: "Yellow", TS: "t2"})
	s.recent.Add(recent.Entry{Source: "radio", CardKey: "1live", Track: "Other", TS: "t3"})

	w := recentRequest(t, s, http.MethodDelete, "?cardKey=swr3&ts=t2")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"removed":2`) {
		t.Fatalf("delete card: %d %s", w.Code, w.Body.String())
	}
	if got := strings.Join(recentKeys(t, s), ","); got != "1live/Other" {
		t.Fatalf("after delete = %s", got)
	}

	if w := recentRequest(t, s, http.MethodDelete, "?cardKey=1live&ts=stale"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"removed":0`) {
		t.Fatalf("stale delete: %d %s", w.Code, w.Body.String())
	}
	if w := recentRequest(t, s, http.MethodDelete, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("bare DELETE = %d, want 400", w.Code)
	}
	if got := strings.Join(recentKeys(t, s), ","); got != "1live/Other" {
		t.Fatalf("a refused delete changed the list: %s", got)
	}

	if w := recentRequest(t, s, http.MethodDelete, "?all=1"); w.Code != http.StatusOK {
		t.Fatalf("clear: %d", w.Code)
	}
	if got := recentKeys(t, s); len(got) != 0 {
		t.Fatalf("after clear = %v", got)
	}
}

// The phone page's Recently played has the desktop app's delete-one and
// clear-all, through the same agent endpoints, with an inline confirmation.
func TestPhoneRemoteRecentCanDeleteAndClear(t *testing.T) {
	at := strings.Index(indexHTML, "async function loadRecent(")
	end := strings.Index(indexHTML, "async function playRecent(")
	if at < 0 || end < at {
		t.Fatal("could not find the phone page's recently-played code")
	}
	code := indexHTML[at:end]
	for _, want := range []string{
		"seenKeys[e.cardKey]", // one row per station, not per session
		"'/api/recent?cardKey=' + encodeURIComponent(e.cardKey) + '&ts=' + encodeURIComponent(e.ts), 'DELETE'",
		"'/api/recent?all=1', 'DELETE'",
		"function renderRecentFoot(confirming, err)",
		"btnRecentClearYes", "btnRecentClearNo",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("phone recently-played code is missing %q", want)
		}
	}
	for _, banned := range []string{"confirm(", "alert("} {
		if strings.Contains(code, banned) {
			t.Errorf("phone recently-played code uses %q; the confirmation must be inline", banned)
		}
	}
	if !strings.Contains(indexHTML, `<div id="recentFoot"`) {
		t.Error("phone page is missing the recently-played footer")
	}
}

// Every new Recently-played string exists in every locale bundle.
func TestPhoneRemoteLocalesCarryRecentDelete(t *testing.T) {
	bundles := strings.Count(indexHTML, "now:\"")
	if bundles == 0 {
		t.Fatal("could not find any locale bundle in indexHTML")
	}
	for _, key := range []string{"recDel", "recClear", "recClearQ", "recClearYes", "recClearNo", "recFail"} {
		if got := strings.Count(indexHTML, key+":\""); got != bundles {
			t.Errorf("%s: %d locale bundles but %d keys", key, bundles, got)
		}
	}
}
