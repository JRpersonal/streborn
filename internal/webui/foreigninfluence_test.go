package webui

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resetForeignSeen clears the change-detection state so each test starts from
// "nothing seen yet".
func resetForeignSeen(t *testing.T) {
	t.Helper()
	foreignSeenMu.Lock()
	foreignSeen, foreignFirst = "", true
	foreignSeenMu.Unlock()
	t.Cleanup(func() {
		foreignSeenMu.Lock()
		foreignSeen, foreignFirst = "", true
		foreignSeenMu.Unlock()
	})
}

// isolateBox points every path this file reads at a temp tree, so a finding is
// whatever the test puts there and nothing leaks in from the host.
func isolateBox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// nandRoot is a const, so the mod-file detection reads the real /mnt/nv.
	// On a build machine that does not exist, which is exactly the "no rival
	// tool installed" baseline these tests want.
	old := []any{sdkOverridePath, sdkRootfsPath, hostsOriginalPath, hostsLivePath, liveMargeURLFn, octBackupGlob}
	sdkOverridePath = filepath.Join(dir, "OverrideSdkPrivateCfg.xml")
	sdkRootfsPath = filepath.Join(dir, "rootfs.xml")
	hostsOriginalPath = filepath.Join(dir, "hosts.original")
	hostsLivePath = filepath.Join(dir, "hosts")
	octBackupGlob = filepath.Join(dir, "*SdkPrivateCfg.xml*.oct-backup")
	liveMargeURLFn = func() string { return "" }
	stock := `<?xml version="1.0"?><SoundTouchSdkPrivateCfg>
  <margeServerUrl>https://streaming.bose.com</margeServerUrl>
  <bmxRegistryUrl>https://content.api.bose.io/bmx/registry/v1/services</bmxRegistryUrl>
  <statsServerUrl>https://events.api.bosecm.com</statsServerUrl>
</SoundTouchSdkPrivateCfg>`
	if err := os.WriteFile(sdkOverridePath, []byte(stock), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sdkOverridePath = old[0].(string)
		sdkRootfsPath = old[1].(string)
		hostsOriginalPath = old[2].(string)
		hostsLivePath = old[3].(string)
		liveMargeURLFn = old[4].(func() string)
		octBackupGlob = old[5].(string)
	})
	return dir
}

func findingByID(fs []ForeignFinding, id string) *ForeignFinding {
	for i := range fs {
		if fs[i].ID == id {
			return &fs[i]
		}
	}
	return nil
}

// A clean speaker reports nothing. The list must not invent rows, or the safe
// haven view becomes noise nobody reads.
func TestForeignInfluenceQuietOnACleanSpeaker(t *testing.T) {
	isolateBox(t)
	if got := collectForeignInfluence(); len(got) != 0 {
		t.Errorf("a clean speaker must report nothing, got %+v", got)
	}
	if foreignSummary(nil) != "" {
		t.Error("an empty summary must be empty")
	}
}

// The shape that started all this: live-only, no file touched.
func TestForeignInfluenceSeesARuntimeHijack(t *testing.T) {
	isolateBox(t)
	liveMargeURLFn = func() string { return "https://stremotepro.com/api/bose-cloud" }
	got := collectForeignInfluence()
	f := findingByID(got, "cloud-url-live")
	if f == nil {
		t.Fatalf("the live hijack must be reported, got %+v", got)
	}
	if f.Tool != "ST Remote Pro" {
		t.Errorf("the tool must be named, got %q", f.Tool)
	}
	if !f.Active {
		t.Error("a live hijack is active, not a leftover")
	}
	if f.Undo != undoReboot {
		t.Errorf("the cure is a restart, got %q", f.Undo)
	}
	// And the files must NOT be reported, because they are fine. Saying
	// otherwise would send the user through a rewrite that changes nothing.
	if findingByID(got, "cloud-url-file") != nil {
		t.Error("the files are pristine and must not be reported")
	}
}

// An address nobody recognises still has to show up, quoted. That is the case
// that teaches us about the next tool.
func TestForeignInfluenceReportsAnUnknownTool(t *testing.T) {
	isolateBox(t)
	liveMargeURLFn = func() string { return "https://some-new-thing.example/api" }
	f := findingByID(collectForeignInfluence(), "cloud-url-live")
	if f == nil {
		t.Fatal("an unknown foreign address must still be reported")
	}
	if f.Tool != "" {
		t.Errorf("an unknown tool must not be guessed, got %q", f.Tool)
	}
	if !strings.Contains(f.Detail, "some-new-thing.example") {
		t.Errorf("the address must be quoted, got %q", f.Detail)
	}
}

// A foreign redirect in the read-only hosts file is shown and explained, never
// offered as a button STR cannot honour.
func TestForeignInfluenceExplainsTheUntouchableHostsBlock(t *testing.T) {
	dir := isolateBox(t)
	body := "127.0.0.1 localhost\n" +
		"# >>> streborn begin >>>\n10.0.0.5 streaming.bose.com\n# <<< streborn end <<<\n" +
		"192.168.1.9 content.api.bose.io\n"
	if err := os.WriteFile(filepath.Join(dir, "hosts.original"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f := findingByID(collectForeignInfluence(), "hosts-block")
	if f == nil {
		t.Fatal("a foreign redirect must be reported")
	}
	if f.Undo != undoNone || f.Note == "" {
		t.Errorf("it must be explained rather than offered, got undo=%q note=%q", f.Undo, f.Note)
	}
	// STR's OWN block is the product working and must never be listed.
	if strings.Contains(f.Detail, "10.0.0.5") {
		t.Errorf("STR's own redirect must not be reported as foreign: %q", f.Detail)
	}
}

// The log cadence. This runs on polled paths, so a line per call would be
// exactly the log spam that wears the speaker's NAND.
func TestForeignInfluenceLogsOnChangeNotOnEveryLook(t *testing.T) {
	isolateBox(t)
	resetForeignSeen(t)
	var buf bytes.Buffer
	s := &Server{logger: slog.New(slog.NewTextHandler(&buf, nil))}
	liveMargeURLFn = func() string { return "https://stremotepro.com/api/bose-cloud" }

	for i := 0; i < 5; i++ {
		s.noteForeignInfluence(collectForeignInfluence())
	}
	first := strings.Count(buf.String(), foreignLogPrefix)
	if first != 1 {
		t.Errorf("an unchanged state must be logged once, got %d lines", first)
	}

	// The repair lands: that IS a change and must be recorded, or the log
	// shows the problem and never its end.
	liveMargeURLFn = func() string { return "" }
	s.noteForeignInfluence(collectForeignInfluence())
	if !strings.Contains(buf.String(), "none left") {
		t.Error("the moment the speaker came back must be in the log")
	}
}

// The prefix is the contract a later sweep greps for across bundles.
func TestForeignInfluenceLogLineIsGreppable(t *testing.T) {
	isolateBox(t)
	resetForeignSeen(t)
	var buf bytes.Buffer
	s := &Server{logger: slog.New(slog.NewTextHandler(&buf, nil))}
	liveMargeURLFn = func() string { return "https://stremotepro.com/api/bose-cloud" }
	s.noteForeignInfluence(collectForeignInfluence())
	out := buf.String()
	for _, want := range []string{foreignLogPrefix, "id=cloud-url-live", "ST Remote Pro", "undo=reboot"} {
		if !strings.Contains(out, want) {
			t.Errorf("the log line must carry %q, got: %s", want, out)
		}
	}
}

// Undoing a row always answers with what is STILL there. A repair that reports
// success while the speaker is unchanged is the failure this area keeps making.
func TestForeignInfluenceUndoAlwaysReportsWhatRemains(t *testing.T) {
	isolateBox(t)
	resetForeignSeen(t)
	liveMargeURLFn = func() string { return "https://stremotepro.com/api/bose-cloud" }
	s := &Server{logger: slog.New(slog.NewTextHandler(new(bytes.Buffer), nil))}
	r := httptest.NewRequest(http.MethodPost, "/api/box/foreign-influence",
		strings.NewReader(`{"id":"cloud-url-live"}`))
	r.RemoteAddr = "192.168.178.20:5555"
	w := httptest.NewRecorder()
	s.handleForeignInfluence(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["rebootRequired"] != true {
		t.Errorf("the live row is cured by a restart, got %v", out)
	}
	// Still hijacked, because nothing has rebooted yet. The answer must say so.
	if rem, _ := out["remaining"].(string); !strings.Contains(rem, "cloud-url-live") {
		t.Errorf("remaining must still list the row until the speaker restarts, got %q", rem)
	}
}

func TestForeignInfluenceUndoIsLANOnly(t *testing.T) {
	isolateBox(t)
	s := &Server{logger: slog.New(slog.NewTextHandler(new(bytes.Buffer), nil))}
	r := httptest.NewRequest(http.MethodPost, "/api/box/foreign-influence",
		strings.NewReader(`{"id":"cloud-url-live"}`))
	r.RemoteAddr = "203.0.113.9:5555"
	w := httptest.NewRecorder()
	s.handleForeignInfluence(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 off-LAN, got %d", w.Code)
	}
}
