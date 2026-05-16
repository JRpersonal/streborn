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

// skipIfNoSymlinkPrivilege uebergeht Tests die echte Symlinks brauchen.
// Auf Windows ohne Developer Mode oder Admin Rechte schlaegt os.Symlink fehl.
// Diese Tests werden auf CI (Linux) und auf der echten Box ohnehin gefahren,
// daher ist Skip auf Windows ok.
func skipIfNoSymlinkPrivilege(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		return
	}
	// Probehalber einen Test Symlink anlegen
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(src, dst); err != nil {
		t.Skipf("symlinks nicht verfuegbar auf dieser Plattform: %v", err)
	}
}

// makeBoseConfigs legt simulierte Bose Standard Configs in einem Tempdir an.
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
			t.Fatalf("XML nicht wohlgeformt: %v\n%s", err, xmlStr)
		}
	}
	if !strings.Contains(xmlStr, `name="streborn"`) {
		t.Errorf("daemon name fehlt: %s", xmlStr)
	}
	if !strings.Contains(xmlStr, `exe="/media/sda1/streborn-armv7l"`) {
		t.Errorf("exe Pfad fehlt: %s", xmlStr)
	}
	if !strings.Contains(xmlStr, `<arg>:8080</arg>`) {
		t.Errorf("arg Port fehlt: %s", xmlStr)
	}
}

func TestRenderConfigEscapesXMLSpecials(t *testing.T) {
	xmlStr := RenderConfig("/bin/test", []string{`--name=<böse>`})
	if !strings.Contains(xmlStr, "&lt;b") || !strings.Contains(xmlStr, "&gt;") {
		t.Errorf("XML Entities nicht escaped: %s", xmlStr)
	}
	dec := xml.NewDecoder(strings.NewReader(xmlStr))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("XML nicht wohlgeformt nach Escape: %v\n%s", err, xmlStr)
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
		t.Error("DirExists sollte false sein")
	}
	if len(st.MissingSymlinks) != len(StandardSymlinks) {
		t.Errorf("erwartete %d missing, bekam %d", len(StandardSymlinks), len(st.MissingSymlinks))
	}
	if st.IsHealthy() {
		t.Error("nicht healthy bei leerem Verzeichnis erwartet")
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
		t.Errorf("nach Install sollte healthy sein, ist %+v", st)
	}
	if !st.DirExists {
		t.Error("DirExists nach Install")
	}
	if len(st.MissingSymlinks) != 0 {
		t.Errorf("keine MissingSymlinks nach Install erwartet, bekam %v", st.MissingSymlinks)
	}
	if !st.HasOwnConfig {
		t.Error("HasOwnConfig nach Install")
	}

	// Eigene Config Datei muss existieren und korrekt aussehen
	ownPath := filepath.Join(m.cfg.ShepherdDir, OwnConfigName)
	data, err := os.ReadFile(ownPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `name="streborn"`) {
		t.Errorf("daemon name fehlt in %s", ownPath)
	}
}

func TestInstallIdempotent(t *testing.T) {
	skipIfNoSymlinkPrivilege(t)
	m, _ := newTestManager(t)
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	// Modtime der Symlinks merken
	beforeStat, err := os.Lstat(filepath.Join(m.cfg.ShepherdDir, "Shepherd-core.xml"))
	if err != nil {
		t.Fatal(err)
	}

	// Nochmal Install, sollte keine Aenderung am Symlink machen
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	afterStat, err := os.Lstat(filepath.Join(m.cfg.ShepherdDir, "Shepherd-core.xml"))
	if err != nil {
		t.Fatal(err)
	}

	// Modtime sollte gleich sein (Symlink nicht angefasst)
	if !beforeStat.ModTime().Equal(afterStat.ModTime()) {
		t.Error("Symlink wurde erneut angefasst trotz korrektem Stand")
	}
}

func TestInstallFehlendeBoseConfigsUebersprungen(t *testing.T) {
	skipIfNoSymlinkPrivilege(t)
	m, _ := newTestManager(t)
	// Eine Bose Config absichtlich loeschen
	os.Remove(filepath.Join(m.cfg.BoseConfigDir, "Shepherd-hsp.xml"))

	if err := m.Install(); err != nil {
		t.Fatalf("Install sollte trotz fehlender Bose Config klappen: %v", err)
	}

	// hsp Symlink darf nicht existieren
	if _, err := os.Lstat(filepath.Join(m.cfg.ShepherdDir, "Shepherd-hsp.xml")); err == nil {
		t.Error("Symlink Shepherd-hsp.xml sollte nicht existieren")
	}
	// Andere Symlinks aber schon
	if _, err := os.Lstat(filepath.Join(m.cfg.ShepherdDir, "Shepherd-core.xml")); err != nil {
		t.Error("Symlink Shepherd-core.xml sollte existieren")
	}
	// Eigene Config muss da sein
	if _, err := os.Stat(filepath.Join(m.cfg.ShepherdDir, OwnConfigName)); err != nil {
		t.Error("eigene Config sollte existieren")
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
		t.Errorf("ShepherdDir sollte weg sein, err=%v", err)
	}
}

func TestCheckBrokenSymlink(t *testing.T) {
	skipIfNoSymlinkPrivilege(t)
	m, _ := newTestManager(t)
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	// Eine der Bose Configs entfernen NACH dem Install. Damit wird der
	// Symlink broken.
	os.Remove(filepath.Join(m.cfg.BoseConfigDir, "Shepherd-rhino.xml"))

	st, err := m.Check()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.BrokenSymlinks) != 1 || st.BrokenSymlinks[0] != "Shepherd-rhino.xml" {
		t.Errorf("erwartete genau Shepherd-rhino.xml als broken, bekam %v", st.BrokenSymlinks)
	}
	if st.IsHealthy() {
		t.Error("nicht healthy bei broken symlink")
	}
}

func TestDefaultAgentArgs(t *testing.T) {
	args := DefaultAgentArgs("/media/sda1/presets.json")
	// Muss mindestens diese Flags enthalten
	required := []string{"--presets", "--listen-webui", "--listen-marge",
		"--listen-bmx", "--hosts", "--log-level"}
	have := strings.Join(args, " ")
	for _, r := range required {
		if !strings.Contains(have, r) {
			t.Errorf("Flag %s fehlt in %v", r, args)
		}
	}
}

func TestStatusIsHealthy(t *testing.T) {
	tests := []struct {
		name  string
		st    Status
		want  bool
	}{
		{"alles leer", Status{}, false},
		{"nur dir", Status{DirExists: true}, false},
		{"dir + config aber kein Symlinks fehlen",
			Status{DirExists: true, HasOwnConfig: true}, true},
		{"missing Symlink", Status{DirExists: true, HasOwnConfig: true,
			MissingSymlinks: []string{"x"}}, false},
		{"broken Symlink", Status{DirExists: true, HasOwnConfig: true,
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
