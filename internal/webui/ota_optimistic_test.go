package webui

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestWriteBinaryAtomicOptimisticDespitePessimisticStatfs guards the core of
// the optimistic-write change: a statfs that predicts "no room" (UBIFS is
// deliberately pessimistic for compressible data) must no longer refuse the
// OTA up front. The write is attempted anyway and, on a filesystem that
// actually has room, succeeds.
func TestWriteBinaryAtomicOptimisticDespitePessimisticStatfs(t *testing.T) {
	orig := nandHasRoom
	nandHasRoom = func(string, int64) bool { return false }
	defer func() { nandHasRoom = orig }()

	dir := t.TempDir()
	dst := filepath.Join(dir, "streborn-armv7l")
	body := []byte("optimistic-write-body")
	if err := writeBinaryAtomic(dst, body); err != nil {
		t.Fatalf("optimistic write must succeed when the filesystem has room, got %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != string(body) {
		t.Fatalf("dst content mismatch: err=%v got=%q", err, got)
	}
	if _, err := os.Stat(dst + ".new"); !os.IsNotExist(err) {
		t.Errorf(".new temp must not survive a successful write, stat err = %v", err)
	}
}

// TestIsNoSpaceErr guards the discriminator between a real out-of-space write
// failure (escalates to errInsufficientNAND / tier 3) and every other error.
func TestIsNoSpaceErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"bare ENOSPC", syscall.ENOSPC, true},
		{"wrapped PathError ENOSPC", fmt.Errorf("write tmp: %w",
			&fs.PathError{Op: "write", Path: "/mnt/nv/x.new", Err: syscall.ENOSPC}), true},
		{"short write", fmt.Errorf("write tmp: %w", io.ErrShortWrite), true},
		{"message only", errors.New("write /mnt/nv/x.new: no space left on device"), true},
		{"EROFS is not no-space", &fs.PathError{Op: "write", Path: "/mnt/nv/x.new", Err: syscall.EROFS}, false},
		{"generic", errors.New("permission denied"), false},
	}
	for _, c := range cases {
		if got := isNoSpaceErr(c.err); got != c.want {
			t.Errorf("%s: isNoSpaceErr = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestClassifyNANDWriteErr checks the decision that maps a failed write
// attempt to the handler-visible error: real ENOSPC becomes
// errInsufficientNAND with an "actually attempted" inventory message, any
// other failure stays a plain step error (so tier 2 / a 500 handles it).
func TestClassifyNANDWriteErr(t *testing.T) {
	enospc := &fs.PathError{Op: "write", Path: "/mnt/nv/x.new", Err: syscall.ENOSPC}
	err := classifyNANDWriteErr("write tmp", enospc, t.TempDir(), 10*1024*1024, true, "engine dropped", true)
	if !errors.Is(err, errInsufficientNAND) {
		t.Fatalf("ENOSPC must map to errInsufficientNAND, got %v", err)
	}
	for _, want := range []string{"actually attempted", "engine dropped", "engine stop=true", "predicted full=true"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("507 message must contain %q, got %q", want, err.Error())
		}
	}

	// ENOSPC without a prior reclaim run (statfs had predicted room): the
	// message must say so instead of carrying an empty reclaim outcome.
	err = classifyNANDWriteErr("write tmp", enospc, t.TempDir(), 1024, false, "", false)
	if !errors.Is(err, errInsufficientNAND) {
		t.Fatalf("ENOSPC must map to errInsufficientNAND, got %v", err)
	}
	if !strings.Contains(err.Error(), "statfs predicted room") {
		t.Errorf("no-reclaim message must note the room prediction, got %q", err.Error())
	}

	generic := errors.New("input/output error")
	err = classifyNANDWriteErr("rename", generic, t.TempDir(), 1024, false, "", false)
	if errors.Is(err, errInsufficientNAND) {
		t.Fatalf("a non-space failure must not map to errInsufficientNAND, got %v", err)
	}
	if !strings.Contains(err.Error(), "rename:") {
		t.Errorf("generic failure must keep its step prefix, got %q", err.Error())
	}
}

// TestWriteBinaryAtomicCleansTruncatedTempOnFailure verifies a failed attempt
// cannot leave a (truncated) .new behind: the cleanup also covers the
// optimistic attempt that the pessimistic gate would previously have refused.
func TestWriteBinaryAtomicCleansTruncatedTempOnFailure(t *testing.T) {
	orig := nandHasRoom
	nandHasRoom = func(string, int64) bool { return false }
	defer func() { nandHasRoom = orig }()

	dir := t.TempDir()
	// Make dst's parent read-only so the tmp write fails after the gate said
	// "no room" (the closest portable stand-in for a real ENOSPC).
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0o755) }()
	if os.Geteuid() == 0 {
		t.Skip("running as root: read-only dir does not fail the write")
	}
	dst := filepath.Join(dir, "streborn-armv7l")
	err := writeBinaryAtomic(dst, []byte("body"))
	if err == nil {
		t.Fatal("write into a read-only dir must fail")
	}
	if errors.Is(err, errInsufficientNAND) {
		t.Fatalf("a permission failure must not be classified as insufficient NAND, got %v", err)
	}
	if _, serr := os.Stat(dst + ".new"); !os.IsNotExist(serr) {
		t.Errorf("failed attempt must not leave a .new behind, stat err = %v", serr)
	}
}
