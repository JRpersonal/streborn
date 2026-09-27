package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Keeping the session that went wrong.
//
// These logs exist to answer "what happened during that update". The person who
// needs that answer is, by definition, somebody whose update looked wrong, and
// the reasonable first thing to do about that is restart the app. With one slot
// kept, the restart that follows noticing a problem overwrites the run that
// caused it, and the second restart overwrites the first.
//
// A reporter whose speakers lost their Spotify engine across four re-pushes sent
// a bundle in which not one of those four runs survived (2026-09-27). The engine
// deletions were reconstructed from the speakers' own logs instead, which only
// worked because the speakers keep more history than the app did.

func withTempLogDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("LOCALAPPDATA", dir)
	t.Setenv("XDG_CACHE_HOME", dir)
	t.Setenv("HOME", dir)
	p := LogFilePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeLog(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readOrEmpty(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func TestTheRunThatWentWrongSurvivesSeveralRestarts(t *testing.T) {
	p := withTempLogDir(t)

	// Session 1 is the one that matters: the update that removed the engine.
	writeLog(t, p, "session-1 the update that went wrong")
	rotateLogOnStartup()

	// The user restarts a few times trying to make it work.
	for i := 2; i <= 4; i++ {
		writeLog(t, p, fmt.Sprintf("session-%d", i))
		rotateLogOnStartup()
	}

	// Now they save a diagnostic. Session 1 has to still be in there.
	var found string
	for i := 1; i <= logGenerations; i++ {
		if body := readOrEmpty(fmt.Sprintf("%s.%d", p, i)); strings.Contains(body, "session-1") {
			found = fmt.Sprintf("%s.%d", p, i)
		}
	}
	if found == "" {
		t.Fatal("the session that caused the problem was overwritten by the restarts that followed it")
	}

	// And in the order a reader expects: newest first.
	paths := PreviousLogPaths()
	if len(paths) != 4 {
		t.Fatalf("PreviousLogPaths returned %d sessions, want 4", len(paths))
	}
	if !strings.Contains(readOrEmpty(paths[0]), "session-4") {
		t.Errorf("first path is not the newest session: %s", readOrEmpty(paths[0]))
	}
	if !strings.Contains(readOrEmpty(paths[3]), "session-1") {
		t.Errorf("last path is not the oldest session: %s", readOrEmpty(paths[3]))
	}
}

func TestTheRingIsBounded(t *testing.T) {
	p := withTempLogDir(t)
	for i := 1; i <= logGenerations+4; i++ {
		writeLog(t, p, fmt.Sprintf("session-%d", i))
		rotateLogOnStartup()
	}
	if got := len(PreviousLogPaths()); got != logGenerations {
		t.Fatalf("kept %d sessions, want the ring size %d", got, logGenerations)
	}
	// Nothing beyond the ring is left lying around.
	if _, err := os.Stat(fmt.Sprintf("%s.%d", p, logGenerations+1)); err == nil {
		t.Error("a generation past the ring survived, so the log grows without bound")
	}
}

func TestAnEmptyLogIsNotRotated(t *testing.T) {
	// Rotating nothing would push a real session out of the ring for free.
	p := withTempLogDir(t)
	writeLog(t, p, "the session worth keeping")
	rotateLogOnStartup()
	writeLog(t, p, "")
	rotateLogOnStartup()
	if !strings.Contains(readOrEmpty(p+".1"), "the session worth keeping") {
		t.Error("an empty log displaced the previous session")
	}
}

func TestNoPreviousSessionsIsNotAnError(t *testing.T) {
	withTempLogDir(t)
	if got := PreviousLogPaths(); len(got) != 0 {
		t.Errorf("PreviousLogPaths = %v on a fresh install, want none", got)
	}
	rotateLogOnStartup() // must not panic with no log at all
}
