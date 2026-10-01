package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Finding a newer copy of STR sitting next to the one that is running.
//
// There is no Windows installer. The release publishes a bare
// STR-Windows-vX.Y.Z.exe, so "updating" means downloading a second file into
// the same folder. Nothing overwrites anything, because the two files have
// different names, and the old one keeps working, keeps its shortcut and keeps
// its place on the taskbar.
//
// A reporter spent three rounds of mail on this (2026-10-01). He downloaded the
// new version repeatedly, kept starting the old one, and every speaker update he
// tried failed with an error the old build had already had fixed. His own words
// afterwards: the existing version did not go away by itself and was not
// overwritten. He only got there by deleting the old file by hand.
//
// The update check already knew a newer version existed. What it could not say
// was that the newer version was ALREADY on his disk, four centimetres away in
// the same folder, and that all he had to do was start it.

// strExeName matches the published Windows file name and captures its version.
// Deliberately anchored on the published name: a user's own rename to
// "STR.exe" carries no version to compare, and guessing one would be worse than
// staying quiet.
var strExeName = regexp.MustCompile(`(?i)^STR-Windows-v(\d+)\.(\d+)\.(\d+)[^/\\]*\.exe$`)

// parseExeVersion returns the three version numbers in a published file name.
func parseExeVersion(name string) (maj, min, patch int, ok bool) {
	m := strExeName.FindStringSubmatch(name)
	if m == nil {
		return 0, 0, 0, false
	}
	maj, _ = strconv.Atoi(m[1])
	min, _ = strconv.Atoi(m[2])
	patch, _ = strconv.Atoi(m[3])
	return maj, min, patch, true
}

// parseAppVersion reads the running build's version, which carries a leading v
// and may carry a git describe suffix (v0.9.93-18-gb3e1bd65-dirty).
func parseAppVersion(v string) (maj, min, patch int, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	var err error
	if maj, err = strconv.Atoi(parts[0]); err != nil {
		return 0, 0, 0, false
	}
	if min, err = strconv.Atoi(parts[1]); err != nil {
		return 0, 0, 0, false
	}
	if patch, err = strconv.Atoi(parts[2]); err != nil {
		return 0, 0, 0, false
	}
	// v0.0.0 is this repo's sentinel for an unstamped build (run.sh falls back
	// to it too). It parses as a valid version and would make every release in
	// the folder look newer, which points a developer at somebody else's exe.
	if maj == 0 && min == 0 && patch == 0 {
		return 0, 0, 0, false
	}
	return maj, min, patch, true
}

func newer(aMaj, aMin, aPatch, bMaj, bMin, bPatch int) bool {
	if aMaj != bMaj {
		return aMaj > bMaj
	}
	if aMin != bMin {
		return aMin > bMin
	}
	return aPatch > bPatch
}

// newestNewerSibling picks the highest-versioned published file name in names
// that is newer than running. Returns "" when there is none, which is the
// normal case and must stay silent.
//
// Pure, so the decision can be tested without a disk.
func newestNewerSibling(names []string, running string) string {
	rMaj, rMin, rPatch, ok := parseAppVersion(running)
	if !ok {
		// A dev build with no usable version cannot be compared against
		// anything. Saying nothing beats telling a developer to start a file.
		return ""
	}
	best := ""
	bMaj, bMin, bPatch := rMaj, rMin, rPatch
	for _, n := range names {
		maj, min, patch, ok := parseExeVersion(filepath.Base(n))
		if !ok || !newer(maj, min, patch, bMaj, bMin, bPatch) {
			continue
		}
		best, bMaj, bMin, bPatch = filepath.Base(n), maj, min, patch
	}
	return best
}

// NewerCopyNextToThisOne reports the file name of a newer STR sitting in the
// same folder as the running one, or "" when there is none. Bound so the update
// banner can say "you already have it, start that file" instead of sending
// somebody to download what they downloaded yesterday.
func (a *App) NewerCopyNextToThisOne() string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return ""
	}
	entries, err := os.ReadDir(filepath.Dir(self))
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.EqualFold(e.Name(), filepath.Base(self)) {
			continue // never point at the file that is already running
		}
		names = append(names, e.Name())
	}
	return newestNewerSibling(names, appVersion)
}
