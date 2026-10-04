package marge

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tuneInHost = "7f5055e9ff15f2a5035a488b81ec10f4.api.radiotime.com"

// A request to the TuneIn partner host is logged at INFO with its host and
// path, and the request list carries the host, so a bundle can show whether
// the firmware fetched a station title itself (#500).
func TestTuneInRequestIsLoggedWithItsHost(t *testing.T) {
	var buf bytes.Buffer
	s := New(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	req := httptest.NewRequest(http.MethodGet, "/profiles/s24896/nowPlaying?partnerId=Bose&serial=x", nil)
	req.Host = tuneInHost
	s.Handler().ServeHTTP(httptest.NewRecorder(), req)

	if !strings.Contains(buf.String(), "TuneIn request from the box") || !strings.Contains(buf.String(), tuneInHost) {
		t.Fatalf("no INFO line naming the TuneIn host, log was:\n%s", buf.String())
	}
	lines := s.RecentRequestLines(10)
	if len(lines) != 1 || !strings.Contains(lines[0], "host="+tuneInHost) || !strings.Contains(lines[0], "/profiles/s24896/nowPlaying") {
		t.Fatalf("request line lacks host or path: %v", lines)
	}
}

// The TuneIn BMX adapter path counts too, whatever host it arrives on.
func TestTuneInAdapterPathIsLogged(t *testing.T) {
	var buf bytes.Buffer
	s := New(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	req := httptest.NewRequest(http.MethodGet, "/bmx/tunein/v1/playback/station/s24896", nil)
	req.Host = "content.api.bose.io"
	s.Handler().ServeHTTP(httptest.NewRecorder(), req)
	if !strings.Contains(buf.String(), "TuneIn request from the box") {
		t.Fatalf("adapter request not logged, log was:\n%s", buf.String())
	}
}

// Ordinary cloud traffic stays at debug level: the INFO line must not fire
// for the requests the box makes all day.
func TestOrdinaryRequestIsNotLoggedAtInfo(t *testing.T) {
	var buf bytes.Buffer
	s := New(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	req := httptest.NewRequest(http.MethodGet, "/streaming/sourceproviders", nil)
	req.Host = "streaming.bose.com"
	s.Handler().ServeHTTP(httptest.NewRecorder(), req)
	if strings.Contains(buf.String(), "TuneIn request") {
		t.Fatalf("ordinary request logged as TuneIn:\n%s", buf.String())
	}
}

// A reflected TUNEIN (the #500 probe) must not appear in the catalogue a
// second time with its NAME as the id, next to the numbered id 25. Other
// reflected sources (Deezer) keep the shape they ship with. The account
// document carries the reflected TUNEIN with its numeric id.
func TestReflectedTuneInIsNotDuplicatedInCatalogue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reflect-sources.json")
	if err := os.WriteFile(path, []byte(`[{"source":"TUNEIN","account":"","name":"TuneIn"},{"source":"DEEZER","account":"1","name":"Deezer"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(slog.New(slog.NewTextHandler(io.Discard, nil)), WithReflectSourcesPath(path))

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/streaming/sourceproviders", nil))
	cat := rec.Body.String()
	if strings.Contains(cat, `id="TUNEIN"`) {
		t.Errorf("catalogue repeats TUNEIN by name")
	}
	if !strings.Contains(cat, `<sourceprovider id="25">`) {
		t.Errorf("catalogue lost the numbered TUNEIN entry")
	}
	if !strings.Contains(cat, `id="DEEZER"`) {
		t.Errorf("the Deezer reflection must keep its shipped shape")
	}

	full := s.reflectedFullSourcesXML()
	if !strings.Contains(full, `<sourceproviderid>25</sourceproviderid><sourcename>TUNEIN</sourcename>`) {
		t.Errorf("account document lacks the reflected TUNEIN source: %s", full)
	}
}
