//go:build !windows

package sticksetup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// macOS keeps a hidden /Volumes/.timemachine firmlink. With no stick inserted it
// was the only entry the wizard found, so it got auto-selected and the user was
// told "this stick cannot be written to, check the small lock switch, try a
// different USB stick" with nothing plugged in at all.
func TestHiddenMountsAreNotOfferedAsDrives(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{".timemachine", ".Spotlight-V100", "STR-STICK"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "notadir"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := scanMounts(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range got {
		if strings.HasPrefix(filepath.Base(d.Path), ".") {
			t.Errorf("a hidden entry is offered as a drive: %s", d.Path)
		}
	}
	if len(got) != 1 {
		t.Fatalf("drives = %d, want only the real volume: %+v", len(got), got)
	}
	if filepath.Base(got[0].Path) != "STR-STICK" {
		t.Errorf("the real volume is missing, got %s", got[0].Path)
	}
}
