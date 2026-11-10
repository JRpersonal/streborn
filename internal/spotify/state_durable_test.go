package spotify

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// wantStateFile asserts content and, where the host has POSIX permission bits,
// the mode of a written state file, and that no temp file is left beside it.
func wantStateFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Base(path), err)
	}
	if string(b) != content {
		t.Fatalf("%s = %q, want %q", filepath.Base(path), b, content)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != mode {
			t.Fatalf("%s mode = %v, want %v", filepath.Base(path), fi.Mode().Perm(), mode)
		}
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}
}

// occupyTemp makes the next durable write to path fail: its temp sibling is
// occupied by a directory. It stands in for a NAND that refuses the write.
func occupyTemp(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
}

// writeConfig is the one writer of config.yml. It writes exactly what
// configYAML renders, at 0644, and a write that fails reports the error and
// leaves the config the engine last started with.
func TestWriteConfigContentModeAndFailedWriteKeepsPrevious(t *testing.T) {
	m, cfg := qualityTestManager(t)
	path := filepath.Join(cfg, "config.yml")
	if err := m.writeConfig("Kitchen", 30); err != nil {
		t.Fatal(err)
	}
	want := m.configYAML("Kitchen", 30)
	if !strings.Contains(want, "Kitchen") {
		t.Fatalf("rendered config carries no device name:\n%s", want)
	}
	wantStateFile(t, path, want, 0o644)

	occupyTemp(t, path)
	if err := m.writeConfig("Bedroom", 40); err == nil {
		t.Fatal("blocked write reported success")
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != want {
		t.Fatalf("failed write changed config.yml: %q, %v", b, err)
	}
}

// Every caller goes through writeConfig, so a streaming quality change that
// cannot be written is reported to the caller and the old config stays.
func TestSetBitrateFailedConfigWriteKeepsPrevious(t *testing.T) {
	m, cfg := qualityTestManager(t)
	path := filepath.Join(cfg, "config.yml")
	if _, err := m.SetBitrate(320); err != nil {
		t.Fatalf("SetBitrate: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	occupyTemp(t, path)
	if _, err := m.SetBitrate(96); err == nil {
		t.Fatal("SetBitrate reported success although config.yml could not be written")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("failed rewrite changed config.yml: %q, %v", after, err)
	}
}

// The quality choice is one number line at 0644. Cut by a power loss, a plain
// write left it empty, which reads as automatic. A save that fails reports the
// error and keeps the previous choice.
func TestSaveBitrateChoiceContentModeAndFailedWriteKeepsPrevious(t *testing.T) {
	dir := t.TempDir()
	if err := saveBitrateChoice(dir, 320); err != nil {
		t.Fatal(err)
	}
	wantStateFile(t, bitratePath(dir), "320\n", 0o644)
	if got := loadBitrateChoice(dir); got != 320 {
		t.Fatalf("loaded choice = %d, want 320", got)
	}

	occupyTemp(t, bitratePath(dir))
	if err := saveBitrateChoice(dir, 96); err == nil {
		t.Fatal("blocked save reported success")
	}
	b, err := os.ReadFile(bitratePath(dir))
	if err != nil || string(b) != "320\n" {
		t.Fatalf("failed save changed the choice: %q, %v", b, err)
	}
	if got := loadBitrateChoice(dir); got != 320 {
		t.Fatalf("choice after a failed save = %d, want 320", got)
	}
}

// A credential blob the agent cannot parse is staged verbatim as
// credentials.json at 0600. An import that cannot write reports the error and
// leaves the sign-in that was there.
func TestImportCredentialLegacyContentModeAndFailedWriteKeepsPrevious(t *testing.T) {
	m, cfg := qualityTestManager(t)
	path := filepath.Join(cfg, "credentials.json")
	if err := m.ImportCredential(context.Background(), []byte("legacy-blob-1")); err != nil {
		t.Fatal(err)
	}
	wantStateFile(t, path, "legacy-blob-1", 0o600)

	occupyTemp(t, path)
	if err := m.ImportCredential(context.Background(), []byte("legacy-blob-2")); err == nil {
		t.Fatal("blocked import reported success")
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "legacy-blob-1" {
		t.Fatalf("failed import changed credentials.json: %q, %v", b, err)
	}
}
