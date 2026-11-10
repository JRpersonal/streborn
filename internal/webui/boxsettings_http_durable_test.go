package webui

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// blockTempSibling makes the next durable write to path fail: the writer's
// temp sibling ("<path>.tmp") is occupied by a directory, so it cannot be
// opened as a file. It stands in for a NAND that refuses the write.
func blockTempSibling(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
}

// wantFile asserts content and, where the host has POSIX permission bits, the
// mode of a written state file.
func wantFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Base(path), err)
	}
	if string(b) != content {
		t.Fatalf("%s = %q, want %q", filepath.Base(path), b, content)
	}
	if runtime.GOOS == "windows" {
		return // no POSIX permission bits to compare
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != mode {
		t.Fatalf("%s mode = %v, want %v", filepath.Base(path), fi.Mode().Perm(), mode)
	}
}

// writeFileAtomic is the helper behind the flag files, the AirPlay XML edit
// and the play mode. It must write the exact bytes at 0644 without leaving its
// temp file behind, and a write that fails must leave the previous file alone.
func TestWriteFileAtomicContentModeAndFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setting")
	if err := writeFileAtomic(path, []byte("<xml a=\"1\"/>")); err != nil {
		t.Fatal(err)
	}
	wantFile(t, path, "<xml a=\"1\"/>", 0o644)
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}

	blockTempSibling(t, path)
	if err := writeFileAtomic(path, []byte("new")); err == nil {
		t.Fatal("blocked write reported success")
	}
	wantFile(t, path, "<xml a=\"1\"/>", 0o644)
}

// writeFlagFile appends exactly one newline and keeps the old value when the
// write fails.
func TestWriteFlagFileContentModeAndFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flag")
	if err := writeFlagFile(path, "1"); err != nil {
		t.Fatal(err)
	}
	wantFile(t, path, "1\n", 0o644)

	blockTempSibling(t, path)
	if err := writeFlagFile(path, "0"); err == nil {
		t.Fatal("blocked write reported success")
	}
	wantFile(t, path, "1\n", 0o644)
	if !readBoolFlag(path, false) {
		t.Fatal("flag lost its value after a failed write")
	}
}

// savePlayMode never followed its write with a sync, so it relies on the
// helper alone. A failed save is swallowed (best-effort) and the stored mode
// stays what it was.
func TestSavePlayModeContentModeAndFailure(t *testing.T) {
	s := &Server{
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		playModePath: filepath.Join(t.TempDir(), "play-mode"),
	}
	s.savePlayMode(true, repeatAll)
	wantFile(t, s.playModePath, "shuffle=1 repeat="+repeatAll.String()+"\n", 0o644)

	blockTempSibling(t, s.playModePath)
	s.savePlayMode(false, repeatOff)
	sh, rep, ok := s.loadPlayMode()
	if !ok || !sh || rep != repeatAll {
		t.Fatalf("failed save changed the stored mode: shuffle=%v repeat=%v ok=%v", sh, rep, ok)
	}
}
