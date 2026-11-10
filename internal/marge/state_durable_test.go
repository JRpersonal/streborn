package marge

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// wantMode0644 asserts the mode of a written state file where the host has
// POSIX permission bits, and that no temp file is left beside it.
func wantMode0644(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o644 {
			t.Fatalf("%s mode = %v, want 0644", filepath.Base(path), fi.Mode().Perm())
		}
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}
}

// The confirmed device id is one upper-case line at 0644 in a directory the
// store creates. A write that fails is only logged (the id is relearned next
// boot) and must leave the previously stored id in place.
func TestPersistDeviceIDContentModeAndFailedWriteKeepsPrevious(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "deviceid")
	s := New(slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithDeviceID("0000000001B2"), WithDeviceIDPath(path))
	s.SetDeviceID("0000000001a1")
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "0000000001A1\n" {
		t.Fatalf("stored id = %q, %v", b, err)
	}
	wantMode0644(t, path)

	// Occupy the temp sibling with a directory so the write cannot start.
	if err := os.Mkdir(path+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	s.SetDeviceID("0000000001C3")
	if got := s.DeviceID(); got != "0000000001C3" {
		t.Fatalf("in-memory id = %q, want the new one", got)
	}
	b, err = os.ReadFile(path)
	if err != nil || string(b) != "0000000001A1\n" {
		t.Fatalf("failed write changed the stored id: %q, %v", b, err)
	}
}

// The stereo pair record is the only place the pair lives. A persist that
// fails is logged and swallowed, and the file keeps the record it had, so a
// restart restores the pair as it was last stored instead of losing it.
func TestPersistGroupContentModeAndFailedWriteKeepsPrevious(t *testing.T) {
	path := filepath.Join(t.TempDir(), "group.json")
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), groupPath: path}
	if err := s.SetCanonicalGroup(CanonicalGroupXML("", "AAA", "192.0.2.1", "BBB", "192.0.2.2")); err != nil {
		t.Fatalf("seed pair: %v", err)
	}
	if err := s.RenameGroup("Kitchen"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pg persistedGroup
	if err := json.Unmarshal(before, &pg); err != nil || !pg.Canonical || !strings.Contains(pg.XML, "<name>Kitchen</name>") {
		t.Fatalf("persisted record = %+v, %v", pg, err)
	}
	wantMode0644(t, path)

	// Occupy the temp sibling with a directory so the write cannot start.
	if err := os.Mkdir(path+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameGroup("Bedroom"); err != nil {
		t.Fatalf("rename with a failing persist must still succeed in memory: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("failed persist changed the record: %q, %v", after, err)
	}
}
