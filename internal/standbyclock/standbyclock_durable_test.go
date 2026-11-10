package standbyclock

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Save writes the normalized choice as one JSON line at 0644, creates the
// directory, leaves no temp file, and a write that fails keeps the previous
// choice on disk.
func TestSaveContentModeAndFailedWriteKeepsPrevious(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "standby-clock.json")
	first := Config{Enabled: true, Timezone: "Europe/Berlin", Format24: true}
	if err := Save(path, first); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(before); n == 0 || before[n-1] != '\n' {
		t.Fatalf("saved file does not end in a newline: %q", before)
	}
	got, ok := Load(path)
	if !ok || got != first.normalized() {
		t.Fatalf("Load = %+v, %v; want %+v", got, ok, first.normalized())
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o644 {
			t.Fatalf("mode = %v, want 0644", fi.Mode().Perm())
		}
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}

	// Occupy the temp sibling with a directory so the write cannot start.
	if err := os.Mkdir(path+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, Config{Enabled: false}); err == nil {
		t.Fatal("blocked Save reported success")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("failed Save changed the file: %q, want %q", after, before)
	}
}
