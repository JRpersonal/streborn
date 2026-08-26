package spotify

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func qualityTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "cfg")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	return New("", cfg, "", nil, slog.New(slog.NewTextHandler(io.Discard, nil))), cfg
}

// A set bitrate must land in config.yml immediately and survive into a fresh
// Manager on the same directory (agent restart, OTA binary swap).
func TestSetBitratePersistsAndRewritesConfig(t *testing.T) {
	m, cfg := qualityTestManager(t)
	applied, err := m.SetBitrate(320)
	if err != nil {
		t.Fatalf("SetBitrate: %v", err)
	}
	if !applied {
		t.Error("idle manager: want applied=true")
	}
	raw, err := os.ReadFile(filepath.Join(cfg, "config.yml"))
	if err != nil {
		t.Fatalf("config.yml not written: %v", err)
	}
	if !strings.Contains(string(raw), "bitrate: 320") {
		t.Errorf("config.yml carries no bitrate 320:\n%s", raw)
	}

	m2 := New("", cfg, "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m2.mu.Lock()
	got := m2.bitr
	m2.mu.Unlock()
	if got != 320 {
		t.Errorf("fresh manager bitr = %d, want 320", got)
	}
}

func TestSetBitrateRejectsInvalid(t *testing.T) {
	m, _ := qualityTestManager(t)
	if _, err := m.SetBitrate(200); err == nil {
		t.Error("bitrate 200: want error, got nil")
	}
	m.mu.Lock()
	got := m.bitr
	m.mu.Unlock()
	if got != defaultBitrate {
		t.Errorf("rejected value must not stick, bitr = %d", got)
	}
}

// A change during playback stores everything but defers the engine restart:
// playback is never cut (same rule as the device-name watcher).
func TestSetBitrateWhileStreamingDefers(t *testing.T) {
	m, _ := qualityTestManager(t)
	m.mu.Lock()
	m.sink = io.Discard
	m.mu.Unlock()
	applied, err := m.SetBitrate(320)
	if err != nil {
		t.Fatalf("SetBitrate: %v", err)
	}
	if applied {
		t.Error("streaming manager: want applied=false")
	}
	m.mu.Lock()
	pending := m.bitrPending
	m.mu.Unlock()
	if !pending {
		t.Error("want bitrPending=true so the idle watcher restarts the engine")
	}
}

// A corrupt or absent preference file must mean the default, never a refusal
// to start.
func TestLoadBitrateFallsBackToDefault(t *testing.T) {
	dir := t.TempDir()
	if got := loadBitrate(filepath.Join(dir, "absent.txt")); got != defaultBitrate {
		t.Errorf("absent file: %d, want %d", got, defaultBitrate)
	}
	p := filepath.Join(dir, "bad.txt")
	if err := os.WriteFile(p, []byte("kaputt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := loadBitrate(p); got != defaultBitrate {
		t.Errorf("corrupt file: %d, want %d", got, defaultBitrate)
	}
	if err := os.WriteFile(p, []byte("192\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := loadBitrate(p); got != defaultBitrate {
		t.Errorf("invalid value: %d, want %d", got, defaultBitrate)
	}
}

func TestServeQualityRoundTrip(t *testing.T) {
	m, _ := qualityTestManager(t)

	w := httptest.NewRecorder()
	m.ServeQuality(w, httptest.NewRequest("GET", "/spotify/quality", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"bitrate":160`) {
		t.Fatalf("GET = %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	m.ServeQuality(w, httptest.NewRequest("POST", "/spotify/quality", strings.NewReader(`{"bitrate":320}`)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"applied":true`) {
		t.Fatalf("POST = %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	m.ServeQuality(w, httptest.NewRequest("GET", "/spotify/quality", nil))
	if !strings.Contains(w.Body.String(), `"bitrate":320`) {
		t.Fatalf("GET after POST = %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	m.ServeQuality(w, httptest.NewRequest("POST", "/spotify/quality", strings.NewReader(`{"bitrate":123}`)))
	if w.Code != 400 {
		t.Errorf("invalid POST = %d, want 400", w.Code)
	}
}
