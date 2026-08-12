package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The write probe has to answer for the directory the install will actually
// touch, so it writes rather than reading permission bits.
func TestDirWritable(t *testing.T) {
	dir := t.TempDir()
	if !dirWritable(dir) {
		t.Fatal("a fresh temp dir must be writable")
	}
	if dirWritable(filepath.Join(dir, "does-not-exist")) {
		t.Fatal("a missing directory must not report writable")
	}
	if runtime.GOOS != "windows" {
		// A read-only directory is the /opt and /Applications case: the update
		// must find out here, not after the download.
		ro := filepath.Join(dir, "readonly")
		if err := os.Mkdir(ro, 0o500); err != nil {
			t.Fatal(err)
		}
		if os.Geteuid() == 0 {
			t.Skip("running as root, where every directory is writable")
		}
		if dirWritable(ro) {
			t.Fatal("a directory without write permission must not report writable")
		}
	}
	// The probe must clean up after itself: an update check that litters the
	// user's Applications folder with probe files would be its own bug report.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if len(e.Name()) > 8 && e.Name()[:8] == ".streborn"[:8] {
			t.Fatalf("probe file left behind: %s", e.Name())
		}
	}
}

// The macOS asset preference must degrade to the .dmg rather than dead-ending
// when a release predates the zip.
func TestUpdateAssetKeysCoverTheHostOS(t *testing.T) {
	keys := updateAssetKeys()
	if runtime.GOOS == "windows" || runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		if len(keys) == 0 {
			t.Fatalf("no manifest keys for %s", runtime.GOOS)
		}
		if updateAssetKey() != keys[0] {
			t.Fatal("the single-key helper must return the preferred key")
		}
	}
	if runtime.GOOS == "darwin" {
		last := keys[len(keys)-1]
		if last != "desktop_macos" {
			t.Fatalf("the .dmg must stay the last resort on macOS, got %q", last)
		}
	}
}
