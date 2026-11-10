package region

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The box country cache is one line at 0644 in a directory SetBox creates, and
// a cache write that fails keeps the previous file (the in-memory value still
// moves on, the failure is only logged).
func TestSetBoxCacheContentModeAndFailedWriteKeepsPrevious(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "box-country.txt")
	r := New(path, nil)
	r.SetBox("us")
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "US\n" {
		t.Fatalf("cache = %q, %v", b, err)
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
	r.SetBox("GB")
	if r.Country() != "GB" {
		t.Fatalf("in-memory country = %q, want GB", r.Country())
	}
	b, err = os.ReadFile(path)
	if err != nil || string(b) != "US\n" {
		t.Fatalf("failed write changed the cache: %q, %v", b, err)
	}
}
