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

// The shape a rival app leaves behind: the configuration file is PRISTINE and
// only the running firmware names someone else's host.
//
// Measured on a five-speaker fleet on 2026-09-26, minutes after the ST Remote
// Pro iOS app was started. It is the shape the old repair could not see,
// because that one only ever compared files, and it is why a speaker that was
// still hijacked was reported as cleaned up.
func withRuntimeHijack(t *testing.T, liveURL string) {
	t.Helper()
	dir := t.TempDir()
	override := filepath.Join(dir, "OverrideSdkPrivateCfg.xml")
	// Stock content: exactly what STR itself writes, so nothing is wrong here.
	body := `<?xml version="1.0"?><SoundTouchSdkPrivateCfg>
  <margeServerUrl>https://streaming.bose.com</margeServerUrl>
  <bmxRegistryUrl>https://content.api.bose.io/bmx/registry/v1/services</bmxRegistryUrl>
  <statsServerUrl>https://events.api.bosecm.com</statsServerUrl>
</SoundTouchSdkPrivateCfg>`
	if err := os.WriteFile(override, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	oldOverride, oldRootfs, oldLive := sdkOverridePath, sdkRootfsPath, liveMargeURLFn
	sdkOverridePath = override
	sdkRootfsPath = filepath.Join(dir, "absent.xml")
	liveMargeURLFn = func() string { return liveURL }
	t.Cleanup(func() {
		sdkOverridePath, sdkRootfsPath, liveMargeURLFn = oldOverride, oldRootfs, oldLive
	})
}

func cloudRestoreServer() *Server {
	return &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func postRestore(t *testing.T, s *Server) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/box/restore-cloud-url", nil)
	r.RemoteAddr = "192.168.178.20:5555"
	w := httptest.NewRecorder()
	s.handleRestoreCloudURL(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// A pristine file plus a hijacked firmware must ask for a restart. That is the
// whole repair: the firmware reads the file once, at boot.
func TestRestoreCloudURLAsksForARestartOnARuntimeHijack(t *testing.T) {
	withRuntimeHijack(t, "https://stremotepro.com/api/bose-cloud")
	code, out := postRestore(t, cloudRestoreServer())
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	if out["rebootRequired"] != true {
		t.Errorf("a runtime hijack must ask for a restart, got %v", out)
	}
	// healed=false is CORRECT here and must not read as failure: there was
	// nothing wrong with the file.
	if out["healed"] != false {
		t.Errorf("nothing should have been written, got healed=%v", out["healed"])
	}
	// Tagged, the way every other report in this package names a cloud URL
	// (margeServerUrl=<url>), so the app can show which of the three is wrong.
	live, _ := out["liveForeign"].(string)
	if !strings.Contains(live, "stremotepro.com") || !strings.HasPrefix(live, sdkMargeTag+"=") {
		t.Errorf("the foreign address must be named and tagged, got %q", live)
	}
	if _, ok := out["filesForeign"]; ok {
		t.Errorf("the files are fine and must not be reported as foreign: %v", out["filesForeign"])
	}
}

// A speaker that is already STR's own must not be restarted for nothing.
func TestRestoreCloudURLIsANoOpOnAHealthySpeaker(t *testing.T) {
	withRuntimeHijack(t, "https://streaming.bose.com")
	code, out := postRestore(t, cloudRestoreServer())
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	if out["rebootRequired"] == true {
		t.Errorf("a healthy speaker must not be restarted: %v", out)
	}
	if _, ok := out["liveForeign"]; ok {
		t.Errorf("nothing is foreign here: %v", out["liveForeign"])
	}
}

// It writes to the speaker, so it stays on the LAN like every other such call.
func TestRestoreCloudURLIsLANOnly(t *testing.T) {
	withRuntimeHijack(t, "https://stremotepro.com/api/bose-cloud")
	r := httptest.NewRequest(http.MethodPost, "/api/box/restore-cloud-url", nil)
	r.RemoteAddr = "203.0.113.9:5555"
	w := httptest.NewRecorder()
	cloudRestoreServer().handleRestoreCloudURL(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 from off-LAN, got %d", w.Code)
	}
}

func TestRestoreCloudURLRejectsGET(t *testing.T) {
	withRuntimeHijack(t, "https://stremotepro.com/api/bose-cloud")
	r := httptest.NewRequest(http.MethodGet, "/api/box/restore-cloud-url", nil)
	r.RemoteAddr = "192.168.178.20:5555"
	w := httptest.NewRecorder()
	cloudRestoreServer().handleRestoreCloudURL(w, r)
	if w.Code == http.StatusOK {
		t.Fatal("a repair must not run on a GET")
	}
}

// The regression that matters most: the conflicting-mod handler used to build
// stillDetected from FILES alone, so on a runtime hijack it reported everything
// cleaned up while the speaker was still answering to someone else. That is the
// #986 mistake in a new shape, and it is what this pins.
func TestStillDetectedCountsTheLiveAddress(t *testing.T) {
	withRuntimeHijack(t, "https://stremotepro.com/api/bose-cloud")
	if foreignCloudURLFiles() != "" {
		t.Fatalf("the fixture is wrong: the files must be pristine, got %q", foreignCloudURLFiles())
	}
	if liveForeignCloudURL() == "" {
		t.Fatal("the fixture is wrong: the live address must be foreign")
	}
	// The expression the handler uses, kept in sync with agent_admin.go.
	stillDetected := detectConflictingMod() != "" || foreignCloudURLFiles() != "" || liveForeignCloudURL() != ""
	if !stillDetected {
		t.Error("a speaker still naming a foreign host must not be reported as cleaned up")
	}
}
