package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// What this pins, measured on a live speaker 2026-09-15:
//
//	$ curl -H "Origin: https://example.org" http://<speaker>:17008/spotify/credential
//	HTTP/1.1 200 OK
//	Access-Control-Allow-Origin: *
//	Content-Length: 292          <- the reusable Spotify Connect login
//
// The star is the agent telling the browser that any page may read the body.
// The LAN gate is no defence: the request comes from the victim's own browser,
// which is on the LAN. So a website the user happens to open could read their
// speaker's Spotify login.
//
// The rule now: no Origin is not a browser cross-origin read and is served as
// before; a known origin gets permission; any other origin is still served and
// simply gets no permission, so the browser refuses to hand the body over.

func corsProbe(t *testing.T, origin, method string) *httptest.ResponseRecorder {
	t.Helper()
	h := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("body"))
	}))
	r := httptest.NewRequest(method, "/spotify/credential", nil)
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestAForeignSiteGetsNoPermissionToReadTheAnswer(t *testing.T) {
	for _, origin := range []string{
		"https://example.org",
		"http://evil.test",
		"null",
		"https://wails.localhost.evil.test", // the allowlist must not match by prefix
		"http://wails.localhost:8080",       // nor ignore a port
	} {
		rec := corsProbe(t, origin, http.MethodGet)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("origin %q was granted read permission (%q)", origin, got)
		}
	}
}

func TestTheDesktopAppCanStillRead(t *testing.T) {
	for _, origin := range []string{"wails://wails", "http://wails.localhost", "https://wails.localhost"} {
		rec := corsProbe(t, origin, http.MethodGet)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != origin {
			t.Errorf("origin %q: allow-origin = %q, want the origin echoed back", origin, got)
		}
		if rec.Header().Get("Vary") != "Origin" {
			t.Errorf("origin %q: Vary missing, a cache could hand this permission to another origin", origin)
		}
	}
}

// A Go client, curl, or the speaker's own phone page fetching a relative path
// sends no Origin at all. None of those is a browser cross-origin read, and all
// of them must keep working exactly as before.
func TestARequestWithoutAnOriginIsUntouched(t *testing.T) {
	rec := corsProbe(t, "", http.MethodGet)
	if rec.Code != http.StatusOK || rec.Body.String() != "body" {
		t.Fatalf("status %d body %q, want the answer served as before", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("allow-origin = %q on a request that never asked for it", got)
	}
}

// The body is still SERVED to a foreign origin; only the reading is withheld.
// Blocking the request instead would break nothing here but would be a
// different, larger change, and the browser already does the right thing.
func TestTheAnswerIsStillServedToAForeignOrigin(t *testing.T) {
	rec := corsProbe(t, "https://example.org", http.MethodGet)
	if rec.Code != http.StatusOK || rec.Body.String() != "body" {
		t.Fatalf("status %d body %q, want the handler to have run normally", rec.Code, rec.Body.String())
	}
}

func TestAForeignPreflightIsRefused(t *testing.T) {
	if got := corsProbe(t, "https://example.org", http.MethodOptions).Code; got != http.StatusForbidden {
		t.Errorf("preflight status = %d, want 403", got)
	}
	if got := corsProbe(t, "wails://wails", http.MethodOptions).Code; got != http.StatusNoContent {
		t.Errorf("our own preflight status = %d, want 204", got)
	}
	if got := corsProbe(t, "", http.MethodOptions).Code; got != http.StatusNoContent {
		t.Errorf("originless preflight status = %d, want 204", got)
	}
}
