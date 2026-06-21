package shepherd

import (
	"encoding/xml"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// skipIfNoSymlinkPrivilege skips tests that need real symlinks.
// On Windows without Developer Mode or admin rights, os.Symlink fails.
// These tests run on CI (Linux) and on the real box anyway,
// so skipping on Windows is fine.
func skipIfNoSymlinkPrivilege(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		return
	}
	// Try creating a test symlink as a probe
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(src, dst); err != nil {
		t.Skipf("symlinks not available on this platform: %v", err)
	}
}

// makeBoseConfigs creates simulated Bose standard configs in a tempdir.
func makeBoseConfigs(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range StandardSymlinks {
		path := filepath.Join(dir, name)
		content := `<ShepherdConfig><daemon name="dummy"/></ShepherdConfig>`
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	tmp := t.TempDir()
	boseDir := filepath.Join(tmp, "opt-bose-etc")
	shepherdDir := filepath.Join(tmp, "mnt-nv-shepherd")

	makeBoseConfigs(t, boseDir)

	cfg := Config{
		ShepherdDir:   shepherdDir,
		BoseConfigDir: boseDir,
		AgentBinary:   filepath.Join(tmp, "streborn-armv7l"),
		PresetsPath:   filepath.Join(tmp, "presets.json"),
	}
	return New(cfg, newTestLogger()), tmp
}

func TestRenderConfigWohlgeformt(t *testing.T) {
	xmlStr := RenderConfig("/media/sda1/streborn-armv7l",
		[]string{"--listen-marge", ":8080", "--presets", "/media/sda1/presets.json"})

	dec := xml.NewDecoder(strings.NewReader(xmlStr))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("XML not well-formed: %v\n%s", err, xmlStr)
		}
	}
	if !strings.Contains(xmlStr, `name="streborn"`) {
		t.Errorf("daemon name missing: %s", xmlStr)
	}
	if !strings.Contains(xmlStr, `exe="/media/sda1/streborn-armv7l"`) {
		t.Errorf("exe path missing: %s", xmlStr)
	}
	if !strings.Contains(xmlStr, `<arg>:8080</arg>`) {
		t.Errorf("arg port missing: %s", xmlStr)
	}
}

func TestRenderConfigEscapesXMLSpecials(t *testing.T) {
	xmlStr := RenderConfig("/bin/test", []string{`--name=<bad>`})
	if !strings.Contains(xmlStr, "&lt;b") || !strings.Contains(xmlStr, "&gt;") {
		t.Errorf("XML entities not escaped: %s", xmlStr)
	}
	dec := xml.NewDecoder(strings.NewReader(xmlStr))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("XML not well-formed after escape: %v\n%s", err, xmlStr)
		}
	}
}

func TestCheckLeeresVerzeichnisFehlt(t *testing.T) {
	m, _ := newTestManager(t)
	st, err := m.Check()
	if err != nil {
		t.Fatal(err)
	}
	if st.DirExists {
		t.Error("DirExists should be false")
	}
	if len(st.MissingSymlinks) != len(StandardSymlinks) {
		t.Errorf("expected %d missing, got %d", len(StandardSymlinks), len(st.MissingSymlinks))
	}
	if st.IsHealthy() {
		t.Error("expected not healthy for an empty directory")
	}
}

func TestInstallUndCheck(t *testing.T) {
	skipIfNoSymlinkPrivilege(t)
	m, _ := newTestManager(t)
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	st, err := m.Check()
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsHealthy() {
		t.Errorf("after Install should be healthy, is %+v", st)
	}
	if !st.DirExists {
		t.Error("DirExists nach Install")
	}
	if len(st.MissingSymlinks) != 0 {
		t.Errorf("expected no MissingSymlinks after Install, got %v", st.MissingSymlinks)
	}
	if !st.HasOwnConfig {
		t.Error("HasOwnConfig nach Install")
	}

	// Our own config file must exist and look correct
	ownPath := filepath.Join(m.cfg.ShepherdDir, OwnConfigName)
	data, err := os.ReadFile(ownPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `name="streborn"`) {
		t.Errorf("daemon name missing in %s", ownPath)
	}
}

func TestInstallIdempotent(t *testing.T) {
	skipIfNoSymlinkPrivilege(t)
	m, _ := newTestManager(t)
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	// Remember the modtime of the symlinks
	beforeStat, err := os.Lstat(filepath.Join(m.cfg.ShepherdDir, "Shepherd-core.xml"))
	if err != nil {
		t.Fatal(err)
	}

	// Install again, should make no change to the symlink
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	afterStat, err := os.Lstat(filepath.Join(m.cfg.ShepherdDir, "Shepherd-core.xml"))
	if err != nil {
		t.Fatal(err)
	}

	// Modtime should be the same (symlink not touched)
	if !beforeStat.ModTime().Equal(afterStat.ModTime()) {
		t.Error("symlink was touched again despite being in the correct state")
	}
}

func TestInstallFehlendeBoseConfigsUebersprungen(t *testing.T) {
	skipIfNoSymlinkPrivilege(t)
	m, _ := newTestManager(t)
	// Intentionally delete one Bose config
	os.Remove(filepath.Join(m.cfg.BoseConfigDir, "Shepherd-hsp.xml"))

	if err := m.Install(); err != nil {
		t.Fatalf("Install should succeed despite the missing Bose config: %v", err)
	}

	// The hsp symlink must not exist
	if _, err := os.Lstat(filepath.Join(m.cfg.ShepherdDir, "Shepherd-hsp.xml")); err == nil {
		t.Error("symlink Shepherd-hsp.xml should not exist")
	}
	// But the other symlinks should
	if _, err := os.Lstat(filepath.Join(m.cfg.ShepherdDir, "Shepherd-core.xml")); err != nil {
		t.Error("symlink Shepherd-core.xml should exist")
	}
	// Our own config must be present
	if _, err := os.Stat(filepath.Join(m.cfg.ShepherdDir, OwnConfigName)); err != nil {
		t.Error("own config should exist")
	}
}

func TestUninstall(t *testing.T) {
	skipIfNoSymlinkPrivilege(t)
	m, _ := newTestManager(t)
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	if err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.cfg.ShepherdDir); !os.IsNotExist(err) {
		t.Errorf("ShepherdDir should be gone, err=%v", err)
	}
}

func TestCheckBrokenSymlink(t *testing.T) {
	skipIfNoSymlinkPrivilege(t)
	m, _ := newTestManager(t)
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	// Remove one of the Bose configs AFTER the install. This makes the
	// symlink broken.
	os.Remove(filepath.Join(m.cfg.BoseConfigDir, "Shepherd-rhino.xml"))

	st, err := m.Check()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.BrokenSymlinks) != 1 || st.BrokenSymlinks[0] != "Shepherd-rhino.xml" {
		t.Errorf("expected exactly Shepherd-rhino.xml as broken, got %v", st.BrokenSymlinks)
	}
	if st.IsHealthy() {
		t.Error("not healthy with a broken symlink")
	}
}

func TestDefaultAgentArgs(t *testing.T) {
	args := DefaultAgentArgs("/media/sda1/presets.json")
	// Must contain at least these flags
	required := []string{"--presets", "--listen-webui", "--listen-marge",
		"--listen-bmx", "--hosts", "--log-level"}
	have := strings.Join(args, " ")
	for _, r := range required {
		if !strings.Contains(have, r) {
			t.Errorf("flag %s missing in %v", r, args)
		}
	}
}

func TestStatusIsHealthy(t *testing.T) {
	tests := []struct {
		name string
		st   Status
		want bool
	}{
		{"all empty", Status{}, false},
		{"only dir", Status{DirExists: true}, false},
		{"dir + config but no symlinks missing",
			Status{DirExists: true, HasOwnConfig: true}, true},
		{"missing symlink", Status{DirExists: true, HasOwnConfig: true,
			MissingSymlinks: []string{"x"}}, false},
		{"broken symlink", Status{DirExists: true, HasOwnConfig: true,
			BrokenSymlinks: []string{"x"}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.st.IsHealthy(); got != tc.want {
				t.Errorf("IsHealthy: got %v, want %v", got, tc.want)
			}
		})
	}
}
