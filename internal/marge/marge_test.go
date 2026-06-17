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

// xmlWellFormed prueft ob der String als XML parsebar ist.
func xmlWellFormed(t *testing.T, in string) {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(in))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("xml nicht wohlgeformt: %v\n--\n%s\n--", err, in)
		}
	}
}

func newTestServer() *Server {
	return New(slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithDeviceID("DEVICEID_PLACEHOLDER"),
		WithSpyLogSize(50),
	)
}

func TestEmptyPresetsXMLWohlgeformt(t *testing.T) {
	xmlWellFormed(t, EmptyPresetsXML)
}

func TestEmptyRecentsXMLWohlgeformt(t *testing.T) {
	xmlWellFormed(t, EmptyRecentsXML)
}

func TestSoundTouchStatusXMLWohlgeformt(t *testing.T) {
	xmlWellFormed(t, SoundTouchConfiguredXML)
	xmlWellFormed(t, SoundTouchNotConfiguredXML)
}

func TestPresetsHandlerLeer(t *testing.T) {
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
		t.Fatalf("erwartete <presets im Body, bekam: %s", body)
	}
}

func TestPresetsHandlerMitInhalt(t *testing.T) {
	s := newTestServer()
	s.SetPresets([]Preset{
		{ID: 1, Source: "SPOTIFY", Type: "uri", Location: "spotify:playlist:xyz",
			SourceAccount: "user@example.com", ItemName: "Morgens",
			ContainerArt: "https://example.com/art.png",
			CreatedOn:    1700000000, UpdatedOn: 1700000000},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/preset/list", nil)
	s.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	xmlWellFormed(t, body)
	if !strings.Contains(body, "Morgens") {
		t.Fatalf("Preset Name nicht gefunden: %s", body)
	}
	if !strings.Contains(body, "spotify:playlist:xyz") {
		t.Fatalf("Preset Location nicht gefunden: %s", body)
	}
}

func TestServiceAvailabilityHandler(t *testing.T) {
	s := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/services/availability", nil)
	s.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	xmlWellFormed(t, body)
	// Sicher gehen dass die wichtigsten Provider drin sind.
	for _, want := range []string{"SPOTIFY", "DEEZER", "AMAZON", "AIRPLAY"} {
		if !strings.Contains(body, want) {
			t.Fatalf("provider %s fehlt: %s", want, body)
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

func TestSourcesHandlerOhneItems(t *testing.T) {
	s := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sources", nil)
	s.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	xmlWellFormed(t, body)
	if !strings.Contains(body, "DEVICEID_PLACEHOLDER") {
		t.Fatalf("deviceID nicht im Body: %s", body)
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
		t.Fatalf("erwartete UNCONFIGURED Status: %s", body)
	}
}

func TestAccountHandlerKonfiguriert(t *testing.T) {
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
		t.Fatalf("AccountUUID fehlt: %s", body)
	}
}

func TestSpyLogfaengtRequestsAuf(t *testing.T) {
	s := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/some/unbekannter/pfad",
		strings.NewReader("<?xml version=\"1.0\"?><test/>"))
	req.Header.Set("Content-Type", "application/xml")
	s.Handler().ServeHTTP(rec, req)

	entries := s.RecentRequests()
	if len(entries) != 1 {
		t.Fatalf("erwartete 1 Spy Eintrag, bekam %d", len(entries))
	}
	if entries[0].Method != "POST" {
		t.Fatalf("methode falsch: %s", entries[0].Method)
	}
	if entries[0].Path != "/some/unbekannter/pfad" {
		t.Fatalf("pfad falsch: %s", entries[0].Path)
	}
	if !strings.Contains(entries[0].Body, "<test") {
		t.Fatalf("body fehlt: %s", entries[0].Body)
	}
}

func TestSpyLogEndpunkt(t *testing.T) {
	s := newTestServer()
	// Erst einen Request um was zu loggen
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/preset/foo", nil)
	s.Handler().ServeHTTP(rec, req)

	// Dann den Spy Log abrufen
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/__spy/log", nil)
	s.Handler().ServeHTTP(rec2, req2)
	body := rec2.Body.String()
	if !strings.Contains(body, "GET /preset/foo") {
		t.Fatalf("spy log enthaelt den Request nicht: %s", body)
	}
}

func TestCatchallGenericAck(t *testing.T) {
	s := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/voellig/unbekannt", nil)
	s.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	xmlWellFormed(t, body)
	if !strings.Contains(body, "<ack") {
		t.Fatalf("generic ack fehlt: %s", body)
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
