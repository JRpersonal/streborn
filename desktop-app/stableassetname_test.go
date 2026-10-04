package main

import (
	"path/filepath"
	"testing"
)

// fakeResource stands in for the Windows version resource: it answers by file
// name, so the sibling decision can be tested without real executables.
func fakeResource(versions map[string][3]int) func(string) (int, int, int, bool) {
	return func(path string) (int, int, int, bool) {
		v, ok := versions[filepath.Base(path)]
		if !ok {
			return 0, 0, 0, false
		}
		return v[0], v[1], v[2], true
	}
}

// Since 2026-10-04 the file is STR-Windows.exe, and a second browser download
// arrives as "STR-Windows (1).exe". The version comes from the file itself.
func TestFindsANewerStableNamedSiblingByItsResource(t *testing.T) {
	read := siblingVersionReader("C:/Downloads", fakeResource(map[string][3]int{
		"STR-Windows (1).exe": {1, 0, 2},
	}))
	got := newestNewerSiblingBy([]string{"STR-Windows (1).exe", "notes.txt"}, "v1.0.1", read)
	if got != "STR-Windows (1).exe" {
		t.Errorf("got %q, want the newer stable-named copy", got)
	}
}

// The running build is the same version as the copy next to it: nothing to say.
func TestStableNamedSiblingOfTheSameVersionStaysSilent(t *testing.T) {
	read := siblingVersionReader("C:/Downloads", fakeResource(map[string][3]int{
		"STR-Windows (1).exe": {1, 0, 2},
	}))
	if got := newestNewerSiblingBy([]string{"STR-Windows (1).exe"}, "v1.0.2", read); got != "" {
		t.Errorf("pointed at a copy of the running version: %q", got)
	}
}

// Old versioned files still sit in Downloads folders; without a readable
// resource the name keeps working as before.
func TestOldVersionedNameIsTheFallback(t *testing.T) {
	read := siblingVersionReader("C:/Downloads", fakeResource(nil))
	if got := newestNewerSiblingBy([]string{"STR-Windows-v0.9.95.exe"}, "v0.9.80", read); got != "STR-Windows-v0.9.95.exe" {
		t.Errorf("got %q, want the old versioned file", got)
	}
}

// A stable-named file whose resource cannot be read (a dev build, a damaged
// file) carries no version at all, and a guess would be worse than silence.
func TestUnreadableStableNamedSiblingStaysSilent(t *testing.T) {
	read := siblingVersionReader("C:/Downloads", fakeResource(nil))
	if got := newestNewerSiblingBy([]string{"STR-Windows.exe", "STR-Windows (1).exe"}, "v1.0.1", read); got != "" {
		t.Errorf("invented a version for an unreadable file: %q", got)
	}
}

// The resource reader is only ever asked about published names. A user's own
// "STR.exe" or some other program next to it is not ours to point at, whatever
// version it carries.
func TestResourceIsOnlyAskedForPublishedNames(t *testing.T) {
	read := siblingVersionReader("C:/Downloads", fakeResource(map[string][3]int{
		"STR.exe":   {9, 9, 9},
		"setup.exe": {9, 9, 9},
	}))
	if got := newestNewerSiblingBy([]string{"STR.exe", "setup.exe"}, "v1.0.1", read); got != "" {
		t.Errorf("trusted a name that is not ours: %q", got)
	}
}

// The resource wins over the name: an old versioned name on a file that was
// swapped to a newer build in place says the wrong thing.
func TestResourceBeatsAStaleName(t *testing.T) {
	read := siblingVersionReader("C:/Downloads", fakeResource(map[string][3]int{
		"STR-Windows-v0.9.80.exe": {1, 0, 1},
	}))
	maj, min, patch, ok := read("STR-Windows-v0.9.80.exe")
	if !ok || maj != 1 || min != 0 || patch != 1 {
		t.Errorf("got %d.%d.%d ok=%v, want the resource's 1.0.1", maj, min, patch, ok)
	}
}

// The update cache keeps the version in the name, whatever the release calls
// the file, so the cleanup can still tell spent downloads from pending ones.
func TestStagedUpdateNameCarriesTheVersion(t *testing.T) {
	for _, c := range []struct{ file, version, want string }{
		{"STR-Windows.exe", "v1.0.2", "STR-Windows-v1.0.2.exe"},
		{"STR-macOS.zip", "v1.0.2", "STR-macOS-v1.0.2.zip"},
		{"STR-macOS.dmg", "1.0.2", "STR-macOS-v1.0.2.dmg"},
		{"STR-Linux-x64.tar.gz", "v1.0.2", "STR-Linux-x64-v1.0.2.tar.gz"},
		{"STR-Windows-v1.0.1.exe", "v1.0.1", "STR-Windows-v1.0.1.exe"},
	} {
		got := stagedUpdateName(c.file, c.version)
		if got != c.want {
			t.Errorf("stagedUpdateName(%q, %q) = %q, want %q", c.file, c.version, got, c.want)
		}
		if v := stagedVersionRe.FindString(got); v == "" {
			t.Errorf("%q: the cleanup could not read a version from it", got)
		}
	}
}
