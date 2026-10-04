package main

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func versionsFrom(m map[string][3]int) func(string) (int, int, int, bool) {
	return func(p string) (int, int, int, bool) {
		v, ok := m[filepath.Base(p)]
		return v[0], v[1], v[2], ok
	}
}

func existsIn(names ...string) func(string) bool {
	return func(p string) bool {
		for _, n := range names {
			if strings.EqualFold(filepath.Base(p), n) {
				return true
			}
		}
		return false
	}
}

func TestPlanWindowsInstall(t *testing.T) {
	dir := filepath.Join("C:", "Users", "x", "Downloads")
	oldExe := filepath.Join(dir, "STR-Windows-v0.9.95.exe")
	stable := filepath.Join(dir, "STR-Windows.exe")

	cases := []struct {
		name        string
		exe         string
		exists      []string
		versions    map[string][3]int
		update      string
		wantTarget  string
		wantRenamed bool
		wantReplace bool
		wantHandOff string
	}{
		{name: "old versioned name, no stable file yet", exe: oldExe, update: "v1.0.2",
			wantTarget: stable, wantRenamed: true},
		{name: "old name in other case", exe: filepath.Join(dir, "str-windows-V0.9.95.EXE"), update: "v1.0.2",
			wantTarget: stable, wantRenamed: true},
		{name: "browser duplicate of an old name", exe: filepath.Join(dir, "STR-Windows-v0.9.95 (1).exe"), update: "v1.0.2",
			wantTarget: stable, wantRenamed: true},
		{name: "already the stable name", exe: stable, update: "v1.0.2", wantTarget: stable},
		{name: "user's own name stays", exe: filepath.Join(dir, "STR.exe"), update: "v1.0.2",
			wantTarget: filepath.Join(dir, "STR.exe")},
		{name: "browser duplicate of the stable name stays", exe: filepath.Join(dir, "STR-Windows (1).exe"), update: "v1.0.2",
			wantTarget: filepath.Join(dir, "STR-Windows (1).exe")},
		{name: "older stable file is replaced", exe: oldExe, exists: []string{"STR-Windows.exe"},
			versions: map[string][3]int{"STR-Windows.exe": {1, 0, 1}}, update: "v1.0.2",
			wantTarget: stable, wantRenamed: true, wantReplace: true},
		{name: "equal stable file is replaced", exe: oldExe, exists: []string{"STR-Windows.exe"},
			versions: map[string][3]int{"STR-Windows.exe": {1, 0, 2}}, update: "v1.0.2",
			wantTarget: stable, wantRenamed: true, wantReplace: true},
		{name: "newer stable file wins, hand off", exe: oldExe, exists: []string{"STR-Windows.exe"},
			versions: map[string][3]int{"STR-Windows.exe": {1, 0, 3}}, update: "v1.0.2",
			wantTarget: oldExe, wantHandOff: stable},
		{name: "stable file without a version is left alone", exe: oldExe, exists: []string{"STR-Windows.exe"},
			update: "v1.0.2", wantTarget: oldExe},
		{name: "unknown update version is left alone when a stable file exists", exe: oldExe,
			exists: []string{"STR-Windows.exe"}, versions: map[string][3]int{"STR-Windows.exe": {1, 0, 1}},
			update: "", wantTarget: oldExe},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := planWindowsInstall(c.exe, c.update, existsIn(c.exists...), versionsFrom(c.versions))
			if !strings.EqualFold(p.Target, c.wantTarget) || p.Renamed != c.wantRenamed ||
				p.ReplaceExisting != c.wantReplace || p.HandOff != c.wantHandOff {
				t.Fatalf("got %+v, want target=%s renamed=%v replace=%v handoff=%q",
					p, c.wantTarget, c.wantRenamed, c.wantReplace, c.wantHandOff)
			}
		})
	}
}

type fakeRetargeter struct {
	from, to string
	changed  []string
	err      error
}

func (f *fakeRetargeter) Retarget(oldPath, newPath string) ([]string, error) {
	f.from, f.to = oldPath, newPath
	return f.changed, f.err
}

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func TestInstallUnderStableNameMovesOldFileAndShortcuts(t *testing.T) {
	dir := t.TempDir()
	oldExe := filepath.Join(dir, "STR-Windows-v0.9.95.exe")
	newExe := filepath.Join(t.TempDir(), "STR-Windows-v1.0.2.exe")
	writeFile(t, oldExe, "old build")
	writeFile(t, newExe, "new build")
	plan := planWindowsInstall(oldExe, "v1.0.2", fileExists, func(string) (int, int, int, bool) { return 0, 0, 0, false })
	rt := &fakeRetargeter{changed: []string{"Desktop\\ST Reborn.lnk"}}

	launch, err := installUnderStableName(oldExe, newExe, plan, rt, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(launch) != "STR-Windows.exe" || readFile(t, launch) != "new build" {
		t.Fatalf("launch %s holds %q", launch, readFile(t, launch))
	}
	if fileExists(oldExe) {
		t.Fatal("the old versioned file should have been moved aside")
	}
	if readFile(t, oldExe+".old") != "old build" {
		t.Fatal("the old file must survive as .old until the next start")
	}
	if rt.from != oldExe || rt.to != launch {
		t.Fatalf("shortcuts retargeted %q -> %q", rt.from, rt.to)
	}
}

func TestInstallUnderStableNameReplacesOlderStableFileAndKeepsIt(t *testing.T) {
	dir := t.TempDir()
	oldExe := filepath.Join(dir, "STR-Windows-v0.9.95.exe")
	stable := filepath.Join(dir, "STR-Windows.exe")
	newExe := filepath.Join(t.TempDir(), "new.exe")
	writeFile(t, oldExe, "0.9.95")
	writeFile(t, stable, "1.0.1")
	writeFile(t, newExe, "1.0.2")
	plan := windowsInstallPlan{Target: stable, Renamed: true, ReplaceExisting: true}

	if _, err := installUnderStableName(oldExe, newExe, plan, nil, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if readFile(t, stable) != "1.0.2" || readFile(t, stable+".old") != "1.0.1" {
		t.Fatal("the older stable file must be kept as .old and replaced by the update")
	}
}

func TestInstallUnderStableNameFailureLeavesEverythingAsItWas(t *testing.T) {
	dir := t.TempDir()
	oldExe := filepath.Join(dir, "STR-Windows-v0.9.95.exe")
	stable := filepath.Join(dir, "STR-Windows.exe")
	writeFile(t, oldExe, "0.9.95")
	writeFile(t, stable, "1.0.1")
	plan := windowsInstallPlan{Target: stable, Renamed: true, ReplaceExisting: true}
	rt := &fakeRetargeter{}

	// A download that vanished: copyFile fails.
	_, err := installUnderStableName(oldExe, filepath.Join(dir, "missing.exe"), plan, rt, func(string, ...any) {})
	if err == nil {
		t.Fatal("expected an error")
	}
	if readFile(t, oldExe) != "0.9.95" || readFile(t, stable) != "1.0.1" {
		t.Fatal("a failed install must leave both files as they were")
	}
	if rt.from != "" {
		t.Fatal("no shortcut may be touched when the install failed")
	}
}

func TestInstallUnderStableNameShortcutErrorIsNotFatal(t *testing.T) {
	dir := t.TempDir()
	oldExe := filepath.Join(dir, "STR-Windows-v0.9.95.exe")
	newExe := filepath.Join(t.TempDir(), "new.exe")
	writeFile(t, oldExe, "old")
	writeFile(t, newExe, "new")
	plan := windowsInstallPlan{Target: filepath.Join(dir, "STR-Windows.exe"), Renamed: true}
	if _, err := installUnderStableName(oldExe, newExe, plan, &fakeRetargeter{err: errors.New("powershell missing")}, func(string, ...any) {}); err != nil {
		t.Fatalf("a shortcut failure must not fail the update: %v", err)
	}
}

func TestRemoveStaleOldBinaries(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"STR-Windows-v0.9.95.exe.old", "STR-Windows.exe.old", "str-windows (1).exe.OLD",
		"STR-Windows.exe", "notes.old", "other.exe.old"} {
		writeFile(t, filepath.Join(dir, n), "x")
	}
	removed, locked := removeStaleOldBinaries(dir)
	sort.Strings(removed)
	want := []string{"STR-Windows-v0.9.95.exe.old", "STR-Windows.exe.old", "str-windows (1).exe.OLD"}
	sort.Strings(want)
	if strings.Join(removed, "|") != strings.Join(want, "|") || len(locked) != 0 {
		t.Fatalf("removed %v locked %v", removed, locked)
	}
	for _, keep := range []string{"STR-Windows.exe", "notes.old", "other.exe.old"} {
		if !fileExists(filepath.Join(dir, keep)) {
			t.Fatalf("%s must not be touched", keep)
		}
	}
}

func TestRenamedMarkerShowsOnce(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ST Reborn", renamedMarkerName)
	if consumeRenamedMarker(p) {
		t.Fatal("no marker, no note")
	}
	if err := writeRenamedMarker(p, "STR-Windows-v0.9.95.exe"); err != nil {
		t.Fatal(err)
	}
	if !consumeRenamedMarker(p) {
		t.Fatal("the first start after a rename must show the note")
	}
	if consumeRenamedMarker(p) {
		t.Fatal("the note must show only once")
	}
}

func TestShortcutRetargetScriptQuotesPaths(t *testing.T) {
	s := shortcutRetargetScript(`C:\Users\O'Brien\STR-Windows-v0.9.95.exe`, `C:\Users\O'Brien\STR-Windows.exe`)
	if !strings.Contains(s, `$old='C:\Users\O''Brien\STR-Windows-v0.9.95.exe'`) ||
		!strings.Contains(s, `$new='C:\Users\O''Brien\STR-Windows.exe'`) {
		t.Fatalf("paths not quoted for PowerShell: %s", s)
	}
	for _, place := range []string{"GetFolderPath('Desktop')", "GetFolderPath('Programs')", `User Pinned\TaskBar`} {
		if !strings.Contains(s, place) {
			t.Fatalf("script misses %s", place)
		}
	}
	if !strings.Contains(s, "-ieq $old") {
		t.Fatal("only shortcuts that point at the old file may be changed")
	}
}
