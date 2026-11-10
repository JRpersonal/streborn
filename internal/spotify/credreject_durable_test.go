package spotify

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// writeFileAtomic keeps the caller's mode (0600 for credentials, 0644 for the
// account side files), leaves no temp file, and a write that fails leaves the
// previous file intact.
func TestWriteFileAtomicContentModeAndFailedWriteKeepsPrevious(t *testing.T) {
	for _, perm := range []os.FileMode{0o600, 0o644} {
		path := filepath.Join(t.TempDir(), "state.json")
		if err := writeFileAtomic(path, []byte(`{"a":1}`), perm); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(path)
		if err != nil || string(b) != `{"a":1}` {
			t.Fatalf("content = %q, %v", b, err)
		}
		if runtime.GOOS != "windows" {
			fi, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != perm {
				t.Fatalf("mode = %v, want %v", fi.Mode().Perm(), perm)
			}
		}
		if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
			t.Fatalf("temp file left behind: %v", err)
		}

		// Occupy the temp sibling with a directory so the write cannot start.
		if err := os.Mkdir(path+".tmp", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := writeFileAtomic(path, []byte(`{"a":2}`), perm); err == nil {
			t.Fatal("blocked write reported success")
		}
		b, err = os.ReadFile(path)
		if err != nil || string(b) != `{"a":1}` {
			t.Fatalf("failed write changed the file: %q, %v", b, err)
		}
	}
}
