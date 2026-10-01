package main

import "testing"

// The reporter's folder on 2026-10-01: he had downloaded the new one several
// times and kept starting the old one, because nothing overwrites anything.
func TestFindsTheNewerFileHeAlreadyDownloaded(t *testing.T) {
	names := []string{
		"STR-Windows-v0.8.6.exe",
		"STR-Windows-v0.9.93.exe",
		"readme.txt",
	}
	if got := newestNewerSibling(names, "v0.8.6"); got != "STR-Windows-v0.9.93.exe" {
		t.Errorf("got %q, want the newer file", got)
	}
}

func TestPicksTheHighestOfSeveral(t *testing.T) {
	names := []string{
		"STR-Windows-v0.9.90.exe", "STR-Windows-v0.9.93.exe", "STR-Windows-v0.9.91.exe",
	}
	if got := newestNewerSibling(names, "v0.8.6"); got != "STR-Windows-v0.9.93.exe" {
		t.Errorf("got %q, want the highest", got)
	}
	// Ordering must not decide it.
	rev := []string{names[1], names[0], names[2]}
	if got := newestNewerSibling(rev, "v0.8.6"); got != "STR-Windows-v0.9.93.exe" {
		t.Errorf("order changed the answer: %q", got)
	}
}

// The normal case by far: nothing newer is lying around, and the banner must
// not start inventing files.
func TestSilentWhenNothingIsNewer(t *testing.T) {
	for name, names := range map[string][]string{
		"only older":   {"STR-Windows-v0.8.6.exe", "STR-Windows-v0.9.0.exe"},
		"only equal":   {"STR-Windows-v0.9.93.exe"},
		"empty folder": {},
		"other files":  {"notes.txt", "ST Reborn.exe", "setup.exe"},
	} {
		if got := newestNewerSibling(names, "v0.9.93"); got != "" {
			t.Errorf("%s: invented %q", name, got)
		}
	}
}

// Version ordering has to be numeric, or v0.9.9 outranks v0.9.93 as a string.
func TestVersionsCompareNumerically(t *testing.T) {
	if got := newestNewerSibling([]string{"STR-Windows-v0.9.9.exe"}, "v0.9.93"); got != "" {
		t.Errorf("v0.9.9 was taken as newer than v0.9.93: %q", got)
	}
	if got := newestNewerSibling([]string{"STR-Windows-v0.10.0.exe"}, "v0.9.93"); got != "STR-Windows-v0.10.0.exe" {
		t.Errorf("v0.10.0 was not taken as newer than v0.9.93: %q", got)
	}
	if got := newestNewerSibling([]string{"STR-Windows-v1.0.0.exe"}, "v0.9.93"); got == "" {
		t.Error("a major bump was missed")
	}
}

// A browser that downloaded the same file twice names the second one
// "STR-Windows-v0.9.93 (1).exe". It is still the right file to point at.
func TestAcceptsABrowsersDuplicateSuffix(t *testing.T) {
	if got := newestNewerSibling([]string{"STR-Windows-v0.9.93 (1).exe"}, "v0.8.6"); got != "STR-Windows-v0.9.93 (1).exe" {
		t.Errorf("got %q", got)
	}
}

// A dev build has no comparable version. Telling a developer to start some
// other file would be noise, and worse, could be wrong.
func TestSilentOnADevBuild(t *testing.T) {
	for _, running := range []string{"", "dev", "v0.0.0-dev", "nonsense"} {
		if got := newestNewerSibling([]string{"STR-Windows-v0.9.93.exe"}, running); got != "" {
			t.Errorf("running=%q produced %q", running, got)
		}
	}
}

// The running build carries a git describe suffix, which must not stop the
// comparison: that is exactly the build a tester runs.
func TestHandlesAGitDescribeSuffix(t *testing.T) {
	if got := newestNewerSibling([]string{"STR-Windows-v0.9.95.exe"}, "v0.9.93-18-gb3e1bd65-dirty"); got != "STR-Windows-v0.9.95.exe" {
		t.Errorf("got %q", got)
	}
	if got := newestNewerSibling([]string{"STR-Windows-v0.9.93.exe"}, "v0.9.93-18-gb3e1bd65-dirty"); got != "" {
		t.Errorf("pointed at the same version it is already running: %q", got)
	}
}

// Only the published name is trusted. A rename carries no version, and a guess
// there would send somebody to start the wrong thing.
func TestIgnoresFilesThatCarryNoVersion(t *testing.T) {
	names := []string{"STR.exe", "ST Reborn.exe", "STR-Windows.exe", "str-windows-v.exe"}
	if got := newestNewerSibling(names, "v0.8.6"); got != "" {
		t.Errorf("trusted an unversioned name: %q", got)
	}
}

func TestNameMatchIsCaseInsensitive(t *testing.T) {
	if got := newestNewerSibling([]string{"str-windows-v0.9.93.EXE"}, "v0.8.6"); got == "" {
		t.Error("a differently-cased name was missed; Windows does not care about case")
	}
}
