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
// There is no Windows installer. Up to v1.0.1 the release published a bare
// STR-Windows-vX.Y.Z.exe, so "updating" by hand meant downloading a second file
// into the same folder. Nothing overwrote anything, because the two files had
// different names, and the old one kept working, kept its shortcut and kept
// its place on the taskbar.
//
// Since 2026-10-04 the file is simply STR-Windows.exe. The in-app update swaps
// the running file in place and keeps its name, so a versioned name ended up
// describing an old build (STR-Windows-v0.9.80.exe running v1.0.1). A second
// download from the browser now arrives as "STR-Windows (1).exe", and the
// version comes from the file's own version resource instead of its name.
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

// strExeName matches the file names published up to v1.0.1 and captures their
// version. Kept as the fallback for those older files, which still sit in
// people's Downloads folders.
var strExeName = regexp.MustCompile(`(?i)^STR-Windows-v(\d+)\.(\d+)\.(\d+)[^/\\]*\.exe$`)

// strExeFamily matches every published Windows file name, old and new,
// including a browser's duplicate suffix ("STR-Windows (1).exe"). A user's own
// rename to "STR.exe" is deliberately left out: nothing says it is ours.
var strExeFamily = regexp.MustCompile(`(?i)^STR-Windows[^/\\]*\.exe$`)

// parseExeVersion returns the three version numbers in an old, versioned file
// name.
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

// versionOfFunc tells the version of one file in the folder, by name.
type versionOfFunc func(name string) (maj, min, patch int, ok bool)

// versionFromName is the name-only reader: old versioned file names only.
func versionFromName(name string) (maj, min, patch int, ok bool) {
	return parseExeVersion(filepath.Base(name))
}

// siblingVersionReader reads a sibling's version from its Windows version
// resource first (the only source a stable file name leaves) and falls back to
// the old versioned name. Only published file names are asked at all.
func siblingVersionReader(dir string, fromResource func(path string) (int, int, int, bool)) versionOfFunc {
	return func(name string) (int, int, int, bool) {
		base := filepath.Base(name)
		if !strExeFamily.MatchString(base) {
			return 0, 0, 0, false
		}
		if maj, min, patch, ok := fromResource(filepath.Join(dir, base)); ok {
			return maj, min, patch, true
		}
		return parseExeVersion(base)
	}
}

// newestNewerSibling picks the highest-versioned published file name in names
// that is newer than running, judging versions by name alone. Returns "" when
// there is none, which is the normal case and must stay silent.
func newestNewerSibling(names []string, running string) string {
	return newestNewerSiblingBy(names, running, versionFromName)
}

// newestNewerSiblingBy is the decision itself, with the version source
// injected so it can be tested without a disk.
func newestNewerSiblingBy(names []string, running string, versionOf versionOfFunc) string {
	rMaj, rMin, rPatch, ok := parseAppVersion(running)
	if !ok {
		// A dev build with no usable version cannot be compared against
		// anything. Saying nothing beats telling a developer to start a file.
		return ""
	}
	best := ""
	bMaj, bMin, bPatch := rMaj, rMin, rPatch
	for _, n := range names {
		maj, min, patch, ok := versionOf(n)
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
	return newestNewerSiblingBy(names, appVersion, siblingVersionReader(filepath.Dir(self), exeResourceVersion))
}

// exeVersionString returns the version of the STR executable at path as
// "vX.Y.Z", read from its version resource or, for files published up to
// v1.0.1, from its name. "" when neither says anything.
func exeVersionString(path string) string {
	maj, min, patch, ok := siblingVersionReader(filepath.Dir(path), exeResourceVersion)(filepath.Base(path))
	if !ok {
		return ""
	}
	return "v" + strconv.Itoa(maj) + "." + strconv.Itoa(min) + "." + strconv.Itoa(patch)
}
