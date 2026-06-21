package marge

import (
	"encoding/xml"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// xmlWellFormed checks whether the string is parseable as XML.
func xmlWellFormed(t *testing.T, in string) {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(in))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("xml not well-formed: %v\n--\n%s\n--", err, in)
		}
	}
}

func newTestServer() *Server {
	return New(slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithDeviceID("DEVICEID_PLACEHOLDER"),
		WithSpyLogSize(50),
	)
}

func TestEmptyPresetsXMLWellFormed(t *testing.T) {
	xmlWellFormed(t, EmptyPresetsXML)
}

func TestEmptyRecentsXMLWellFormed(t *testing.T) {
	xmlWellFormed(t, EmptyRecentsXML)
}

func TestSoundTouchStatusXMLWellFormed(t *testing.T) {
	xmlWellFormed(t, SoundTouchConfiguredXML)
	xmlWellFormed(t, SoundTouchNotConfiguredXML)
}

func TestPresetsHandlerEmpty(t *testing.T) {
	s := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/preset/list", nil)
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
	body := rec.Body.String()
	xmlWellFormed(t, body)
	if !strings.Contains(body, "<presets") {
		t.Fatalf("expected <presets in body, got: %s", body)
	}
}

func TestPresetsHandlerMitInhalt(t *testing.T) {
	s := newTestServer()
	s.SetPresets([]Preset{
		{ID: 1, Source: "SPOTIFY", Type: "uri", Location: "spotify:playlist:xyz",
			SourceAccount: "user@example.com", ItemName: "Morning",
			ContainerArt: "https://example.com/art.png",
			CreatedOn:    1700000000, UpdatedOn: 1700000000},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/preset/list", nil)
	s.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	xmlWellFormed(t, body)
	if !strings.Contains(body, "Morning") {
		t.Fatalf("preset name not found: %s", body)
	}
	if !strings.Contains(body, "spotify:playlist:xyz") {
		t.Fatalf("preset location not found: %s", body)
	}
}

func TestServiceAvailabilityHandler(t *testing.T) {
	s := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/services/availability", nil)
	s.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	xmlWellFormed(t, body)
	// Make sure the most important providers are present.
	for _, want := range []string{"SPOTIFY", "DEEZER", "AMAZON", "AIRPLAY"} {
		if !strings.Contains(body, want) {
			t.Fatalf("provider %s missing: %s", want, body)
		}
	}
}

func TestReflectDeezerSource(t *testing.T) {
	// Path A: a reflect-sources file with a Deezer entry must make the marge stub
	// re-advertise Deezer both as a source provider and as an account-linked
	// source, so the box keeps it and plays via its cached ARL.
	dir := t.TempDir()
	rp := dir + "/reflect-sources.json"
	if err := os.WriteFile(rp, []byte(`[{"source":"DEEZER","account":"1456373802","name":"Deezer"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithDeviceID("DEVICEID_PLACEHOLDER"), WithReflectSourcesPath(rp))

	get := func(path string) string {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d", path, rec.Code)
		}
		body := rec.Body.String()
		xmlWellFormed(t, body)
		return body
	}

	sp := get("/streaming/sourceproviders")
	if !strings.Contains(sp, `id="DEEZER"`) {
		t.Fatalf("sourceproviders missing DEEZER: %s", sp)
	}
	full := get("/streaming/account/1456373802/full")
	if !strings.Contains(full, `type="DEEZER"`) || !strings.Contains(full, "1456373802") {
		t.Fatalf("account/full missing reflected Deezer source: %s", full)
	}

	// Without a reflect file: no Deezer is advertised (safe default).
	s2 := New(slog.New(slog.NewTextHandler(io.Discard, nil)), WithDeviceID("DEVICEID_PLACEHOLDER"))
	rec := httptest.NewRecorder()
	s2.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/streaming/sourceproviders", nil))
	if strings.Contains(rec.Body.String(), `id="DEEZER"`) {
		t.Fatalf("sourceproviders should not list DEEZER without a reflect file: %s", rec.Body.String())
	}
}

func TestSourcesHandlerWithoutItems(t *testing.T) {
	s := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sources", nil)
	s.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	xmlWellFormed(t, body)
	if !strings.Contains(body, "DEVICEID_PLACEHOLDER") {
		t.Fatalf("deviceID not in body: %s", body)
	}
}

func TestAccountHandlerUnkonfiguriert(t *testing.T) {
	s := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/account/info", nil)
	s.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	xmlWellFormed(t, body)
	if !strings.Contains(body, "UNCONFIGURED") {
		t.Fatalf("expected UNCONFIGURED status: %s", body)
	}
}

func TestAccountHandlerConfigured(t *testing.T) {
	s := newTestServer()
	s.SetAccount(&AccountInfo{
		AccountUUID:  "uuid-123",
		AccountEmail: "test@example.com",
		AuthToken:    "token-xyz",
		CreatedAt:    "2026-05-14T12:00:00Z",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/account/info", nil)
	s.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	xmlWellFormed(t, body)
	if !strings.Contains(body, "uuid-123") {
		t.Fatalf("AccountUUID missing: %s", body)
	}
}

func TestSpyLogfaengtRequestsAuf(t *testing.T) {
	s := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/some/unknown/path",
		strings.NewReader("<?xml version=\"1.0\"?><test/>"))
	req.Header.Set("Content-Type", "application/xml")
	s.Handler().ServeHTTP(rec, req)

	entries := s.RecentRequests()
	if len(entries) != 1 {
		t.Fatalf("expected 1 spy entry, got %d", len(entries))
	}
	if entries[0].Method != "POST" {
		t.Fatalf("method wrong: %s", entries[0].Method)
	}
	if entries[0].Path != "/some/unknown/path" {
		t.Fatalf("path wrong: %s", entries[0].Path)
	}
	if !strings.Contains(entries[0].Body, "<test") {
		t.Fatalf("body missing: %s", entries[0].Body)
	}
}

func TestSpyLogEndpoint(t *testing.T) {
	s := newTestServer()
	// First a request to log something
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/preset/foo", nil)
	s.Handler().ServeHTTP(rec, req)

	// Then fetch the spy log
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/__spy/log", nil)
	s.Handler().ServeHTTP(rec2, req2)
	body := rec2.Body.String()
	if !strings.Contains(body, "GET /preset/foo") {
		t.Fatalf("spy log does not contain the request: %s", body)
	}
}

func TestCatchallGenericAck(t *testing.T) {
	s := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/completely/unknown", nil)
	s.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	xmlWellFormed(t, body)
	if !strings.Contains(body, "<ack") {
		t.Fatalf("generic ack missing: %s", body)
	}
}

func TestHealthz(t *testing.T) {
	s := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("body=%q", rec.Body.String())
	}
}
