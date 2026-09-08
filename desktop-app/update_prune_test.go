package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// stageUpdateCache points os.UserCacheDir at a temp dir and returns the
// updates/ folder inside it, so the prune can be exercised without touching
// the real per-user cache.
func stageUpdateCache(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("LocalAppData", base)   // Windows
	t.Setenv("XDG_CACHE_HOME", base) // Linux
	t.Setenv("HOME", base)           // macOS and the Linux fallback
	dir, err := updateDir()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeStaged(t *testing.T, dir, name string, age time.Duration) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("installer"), 0o644); err != nil {
		t.Fatal(err)
	}
	if age > 0 {
		old := time.Now().Add(-age)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func newPruneApp() *App {
	return &App{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// Every downloaded installer used to stay in the cache for good: eight of them,
// 390 MB, on one machine. What is running and what is older than it is dead
// weight and must go; an update already downloaded but not applied yet is the
// one thing that must survive, or the user pays for the download twice.
func TestPruneStagedUpdatesKeepsOnlyThePendingOne(t *testing.T) {
	dir := stageUpdateCache(t)
	older := writeStaged(t, dir, "STR-Windows-v0.9.53.exe", 0)
	running := writeStaged(t, dir, "STR-Windows-v0.9.76.exe", 0)
	pending := writeStaged(t, dir, "STR-Windows-v0.9.77.exe", 0)
	foreign := writeStaged(t, dir, "notes.txt", 0)

	newPruneApp().pruneStagedUpdates("v0.9.76")

	for _, gone := range []string{older, running} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed, err=%v", filepath.Base(gone), err)
		}
	}
	for _, kept := range []string{pending, foreign} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s should have been kept: %v", filepath.Base(kept), err)
		}
	}
}

// A ".part" is a download in flight. Deleting a fresh one would pull the file
// out from under the transfer that is still writing it; an hour-old one is a
// leftover from a run that died and is pure waste.
func TestPruneStagedUpdatesSparesADownloadInFlight(t *testing.T) {
	dir := stageUpdateCache(t)
	live := writeStaged(t, dir, "STR-Windows-v0.9.78.exe.part", 0)
	stale := writeStaged(t, dir, "STR-Windows-v0.9.60.exe.part", 3*time.Hour)

	newPruneApp().pruneStagedUpdates("v0.9.77")

	if _, err := os.Stat(live); err != nil {
		t.Errorf("a fresh .part must survive: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("an hour-old .part should have been removed, err=%v", err)
	}
}

// The installers are named per platform, so the version has to be found in all
// three shapes, and a name without one must never be deleted.
func TestStagedVersionRe(t *testing.T) {
	cases := map[string]string{
		"STR-Windows-v0.9.77.exe":      "v0.9.77",
		"STR-macOS-v0.9.77.zip":        "v0.9.77",
		"STR-Linux-x64-v0.9.77.tar.gz": "v0.9.77",
		"STR-Windows-v0.10.0.exe":      "v0.10.0",
		"STR-Windows-v0.9.77.exe.part": "v0.9.77",
		"notes.txt":                    "",
		"streborn-armv7l":              "",
		"STR-Windows-vNext.exe":        "",
	}
	for name, want := range cases {
		if got := stagedVersionRe.FindString(name); got != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
}
