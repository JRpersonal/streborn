package webui

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func startVolServer(t *testing.T) *Server {
	t.Helper()
	return &Server{
		logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		startVolumePath: filepath.Join(t.TempDir(), "start-volume"),
	}
}

func postStartVol(t *testing.T, s *Server, body string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/box/start-volume", strings.NewReader(body))
	r.RemoteAddr = "192.168.178.20:5555"
	w := httptest.NewRecorder()
	s.handleStartVolume(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// Off by default. A speaker that quietly changes its own volume is worse than
// one that forgets, so nothing happens until the owner asks for it.
func TestStartVolumeIsOffUntilItIsSet(t *testing.T) {
	s := startVolServer(t)
	if _, ok := s.startVolume(); ok {
		t.Error("no file must mean off")
	}
	r := httptest.NewRequest(http.MethodGet, "/api/box/start-volume", nil)
	r.RemoteAddr = "192.168.178.20:5555"
	w := httptest.NewRecorder()
	s.handleStartVolume(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["volume"] != float64(0) {
		t.Errorf("off must read as 0, got %v", out["volume"])
	}
}

func TestStartVolumeRoundTrips(t *testing.T) {
	s := startVolServer(t)
	if code, _ := postStartVol(t, s, `{"volume":35}`); code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	v, ok := s.startVolume()
	if !ok || v != 35 {
		t.Errorf("stored level = %d ok=%v, want 35 true", v, ok)
	}
}

// Zero is the honest way to store "off" and must turn it back off.
func TestStartVolumeZeroTurnsItOff(t *testing.T) {
	s := startVolServer(t)
	postStartVol(t, s, `{"volume":40}`)
	postStartVol(t, s, `{"volume":0}`)
	if v, ok := s.startVolume(); ok {
		t.Errorf("0 must mean off, got %d", v)
	}
}

func TestStartVolumeRejectsOutOfRange(t *testing.T) {
	s := startVolServer(t)
	for _, body := range []string{`{"volume":-1}`, `{"volume":101}`} {
		if code, _ := postStartVol(t, s, body); code != http.StatusBadRequest {
			t.Errorf("%s should be refused, got %d", body, code)
		}
	}
}

// A damaged file is not an instruction. Reading garbage must fall back to off
// rather than setting the speaker to something arbitrary.
func TestStartVolumeIgnoresADamagedFile(t *testing.T) {
	s := startVolServer(t)
	for _, junk := range []string{"", "  ", "loud", "999", "-5"} {
		if err := os.WriteFile(s.startVolumePath, []byte(junk), 0o644); err != nil {
			t.Fatal(err)
		}
		if v, ok := s.startVolume(); ok {
			t.Errorf("%q should read as off, got %d", junk, v)
		}
	}
}

func TestStartVolumeIsLANOnly(t *testing.T) {
	s := startVolServer(t)
	r := httptest.NewRequest(http.MethodPost, "/api/box/start-volume", strings.NewReader(`{"volume":30}`))
	r.RemoteAddr = "203.0.113.9:5555"
	w := httptest.NewRecorder()
	s.handleStartVolume(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 off-LAN, got %d", w.Code)
	}
}

// It must only ever run on the automatic resume. A level the user chose while
// the music plays is theirs, and a second caller here would start fighting
// their volume knob.
func TestStartVolumeIsOnlyAppliedOnTheWakeResume(t *testing.T) {
	src, err := os.ReadFile("resume_standby.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(src), "s.applyStartVolume("); n != 1 {
		t.Errorf("applyStartVolume should be called once from the wake resume, found %d calls", n)
	}
}
