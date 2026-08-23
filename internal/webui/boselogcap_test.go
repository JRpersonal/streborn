package webui

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// A speaker arrived with Bose's own log directory at tens of megabytes on a
// 31.58 MiB volume, leaving no room for the Spotify engine while four identical
// speakers beside it had an empty BoseLog (2026-08-23). Those logs are for a
// cloud that was switched off in February.

func withNANDRoot(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func TestCapBoseLogsTrimsARunawayLogAndKeepsItsTail(t *testing.T) {
	root := withNANDRoot(t)
	logDir := filepath.Join(root, "BoseLog")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(logDir, "bose.log")
	body := append(bytes.Repeat([]byte("old junk\n"), 400_000), []byte("THE-NEWEST-LINE\n")...)
	if err := os.WriteFile(big, body, 0o644); err != nil {
		t.Fatal(err)
	}

	capBoseLogs(logDir)

	after, err := os.ReadFile(big)
	if err != nil {
		t.Fatalf("the file must survive, only shrink: %v", err)
	}
	if int64(len(after)) != boseLogKeep {
		t.Errorf("kept %d bytes, want %d", len(after), boseLogKeep)
	}
	// The TAIL is what matters: a log trimmed to its beginning is useless.
	if !bytes.HasSuffix(after, []byte("THE-NEWEST-LINE\n")) {
		t.Error("the newest lines were thrown away instead of the oldest")
	}
}

func TestCapBoseLogsLeavesAnOrdinaryLogDirectoryAlone(t *testing.T) {
	root := withNANDRoot(t)
	logDir := filepath.Join(root, "BoseLog")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	small := filepath.Join(logDir, "bose.log")
	want := []byte("a perfectly normal log\n")
	if err := os.WriteFile(small, want, 0o644); err != nil {
		t.Fatal(err)
	}

	capBoseLogs(logDir)

	got, err := os.ReadFile(small)
	if err != nil || !bytes.Equal(got, want) {
		t.Errorf("a small log must not be touched at all, got %q err %v", got, err)
	}
}

func TestCapBoseLogsTouchesNothingElseOfBoses(t *testing.T) {
	root := withNANDRoot(t)
	logDir := filepath.Join(root, "BoseLog")
	sub := filepath.Join(logDir, "archive")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// The box's own persistence lives next door and must never be involved.
	persist := filepath.Join(root, "BoseApp-Persistence")
	if err := os.MkdirAll(persist, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(persist, "NetworkProfiles.xml")
	huge := bytes.Repeat([]byte("x"), boseLogCapAt+1)
	if err := os.WriteFile(keep, huge, 0o644); err != nil {
		t.Fatal(err)
	}

	capBoseLogs(logDir)

	if fi, err := os.Stat(sub); err != nil || !fi.IsDir() {
		t.Error("a subdirectory of BoseLog must be left in place")
	}
	if got, err := os.ReadFile(keep); err != nil || len(got) != len(huge) {
		t.Errorf("the box's own persistence was touched: %d bytes, err %v", len(got), err)
	}
}

func TestCapBoseLogsIsFineWithoutTheDirectory(t *testing.T) {
	root := withNANDRoot(t)
	// A stock box that has no BoseLog at all must not panic.
	capBoseLogs(filepath.Join(root, "BoseLog"))
}
