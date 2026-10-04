package webui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// patternReader yields n deterministic bytes without ever holding them, so a
// test can feed a large upload and measure what the RECEIVER allocates.
type patternReader struct{ n, off int64 }

func (r *patternReader) Read(p []byte) (int, error) {
	if r.off >= r.n {
		return 0, io.EOF
	}
	if rem := r.n - r.off; int64(len(p)) > rem {
		p = p[:rem]
	}
	for i := range p {
		p[i] = byte((r.off + int64(i)) % 251)
	}
	r.off += int64(len(p))
	return len(p), nil
}

func patternBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = io.ReadFull(&patternReader{n: int64(n)}, b)
	return b
}

func sumHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// #1083: receiving the agent update held the whole binary in memory, and the
// old agent's RSS went from 19 to 43 MB while v1.0.1 arrived. The streamed
// receive must cost a small window, not the size of the binary.
func TestReceiveAgentBinaryDoesNotBufferTheUpload(t *testing.T) {
	const size = 8 << 20
	dst := filepath.Join(t.TempDir(), "streborn-armv7l")

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got, err := receiveAgentBinary(dst, &patternReader{n: size}, size, 30<<20)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 1<<20 {
		t.Errorf("receiving 8 MB allocated %d bytes; the upload is being buffered again", alloc)
	}
	if got.size != size || got.ramStaged {
		t.Errorf("got size=%d ramStaged=%v, want %d on NAND", got.size, got.ramStaged, size)
	}
	want := sumHex(patternBytes(size))
	if got.sum != want {
		t.Error("the reported hash is not the hash of the upload")
	}
	if onDisk, err := fileSHA256(dst); err != nil || onDisk != want {
		t.Errorf("the binary on disk differs from the upload (err %v)", err)
	}
	if _, err := os.Stat(dst + ".new"); !os.IsNotExist(err) {
		t.Errorf("temp file left behind: %v", err)
	}
}

// A body over the limit must be refused before it replaces the running
// agent's binary: with the bytes streaming straight to flash, the size check
// can no longer wait until the whole body has been read into memory.
func TestReceiveAgentBinaryRefusesTooBigWithoutTouchingTheBinary(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "streborn-armv7l")
	if err := os.WriteFile(dst, []byte("OLD-AGENT"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := receiveAgentBinary(dst, &patternReader{n: 64 << 10}, 64<<10, 32<<10)
	if !errors.Is(err, errUploadTooBig) {
		t.Fatalf("err = %v, want errUploadTooBig", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "OLD-AGENT" {
		t.Error("an oversized upload replaced the running agent's binary")
	}
	if _, err := os.Stat(dst + ".new"); !os.IsNotExist(err) {
		t.Errorf("temp file left behind: %v", err)
	}
}

func TestReceiveAgentBinaryRefusesTooSmall(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "streborn-armv7l")
	_, err := receiveAgentBinary(dst, bytes.NewReader(make([]byte, 100)), 100, 30<<20)
	if !errors.Is(err, errUploadTooSmall) {
		t.Fatalf("err = %v, want errUploadTooSmall", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Error("a too-small upload was installed")
	}
}

// A connection that dies mid-upload is the caller's failure, not the box's:
// it must come back as an uploadReadError and leave nothing half-written.
func TestReceiveAgentBinaryReadFailureLeavesNothing(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "streborn-armv7l")
	src := io.MultiReader(&patternReader{n: 64 << 10}, iotestErrReader{})
	_, err := receiveAgentBinary(dst, src, 128<<10, 30<<20)
	var rerr uploadReadError
	if !errors.As(err, &rerr) {
		t.Fatalf("err = %v, want an uploadReadError", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Error("a broken upload was installed")
	}
	if _, err := os.Stat(dst + ".new"); !os.IsNotExist(err) {
		t.Errorf("temp file left behind: %v", err)
	}
}

type iotestErrReader struct{}

func (iotestErrReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

// Tier 3: when the NAND fills up mid-stream, what was already written, the
// part of the current read that did not fit, and the rest of the body must
// all end up in the RAM stage, in order, so the swap helper finds the
// complete binary. Without the old in-memory copy this is the only source.
func TestSpillToRAMStageReassemblesTheWholeUpload(t *testing.T) {
	dir := t.TempDir()
	old := ramStageTarget
	ramStageTarget = filepath.Join(dir, "streborn-ota.stage")
	t.Cleanup(func() { ramStageTarget = old })

	body := patternBytes(200 << 10)
	const written, pendingLen = 70 << 10, 5 << 10
	tmp := filepath.Join(dir, "streborn-armv7l.new")
	if err := os.WriteFile(tmp, body[:written], 0o755); err != nil {
		t.Fatal(err)
	}
	// The hasher has seen everything read so far: the written part plus the
	// read that failed to fit.
	h := sha256.New()
	h.Write(body[:written+pendingLen])
	pending := append([]byte(nil), body[written:written+pendingLen]...)
	rest := bytes.NewReader(body[written+pendingLen:])

	got, err := spillToRAMStage(tmp, written, pending, rest, h, make([]byte, 32<<10), int64(len(body)), 30<<20, errInsufficientNAND)
	if err != nil {
		t.Fatalf("spill: %v", err)
	}
	if !got.ramStaged || got.size != int64(len(body)) {
		t.Errorf("got ramStaged=%v size=%d, want true %d", got.ramStaged, got.size, len(body))
	}
	if got.sum != sumHex(body) {
		t.Error("the staged hash is not the hash of the upload")
	}
	staged, err := os.ReadFile(ramStageTarget)
	if err != nil || !bytes.Equal(staged, body) {
		t.Fatalf("the RAM stage does not hold the complete upload (err %v)", err)
	}
}

// The flash verify now compares against the hash taken while streaming.
func TestVerifyBinaryOnFlashUsesTheStreamedHash(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "streborn-armv7l")
	body := patternBytes(300 << 10)
	if err := os.WriteFile(dst, body, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := verifyBinaryOnFlash(dst, sumHex(body)); err != nil {
		t.Errorf("a matching binary failed the verify: %v", err)
	}
	if err := verifyBinaryOnFlash(dst, sumHex([]byte("something else"))); err == nil {
		t.Error("a binary that differs from the upload passed the verify")
	}
}
