package webui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// elfBody builds a payload that passes the ELF magic check.
func elfBody(n int) []byte {
	b := make([]byte, n)
	copy(b, []byte{0x7f, 'E', 'L', 'F'})
	for i := 4; i < n; i++ {
		b[i] = byte(i % 251)
	}
	return b
}

// TestStreamBinaryAtomicWritesAndHashes is the core of the fix: the bytes reach
// the file without ever being held whole in memory, and the hash the caller
// stamps beside the binary is the hash of what actually landed.
func TestStreamBinaryAtomicWritesAndHashes(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "go-librespot")
	body := elfBody(64 * 1024)

	sum, n, err := streamBinaryAtomic(dst, bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("streamBinaryAtomic: %v", err)
	}
	if n != int64(len(body)) {
		t.Errorf("wrote %d bytes, want %d", n, len(body))
	}
	want := sha256.Sum256(body)
	if sum != hex.EncodeToString(want[:]) {
		t.Errorf("hash = %s, want the hash of the written bytes", sum)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Error("the file on disk differs from what was sent")
	}
	// The engine has to be executable on the speaker. Windows does not model
	// the bit, so this can only be checked where it means something; the box
	// and CI are both Linux.
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(dst); err == nil && fi.Mode().Perm()&0o111 == 0 {
			t.Error("the binary is not executable")
		}
	}
	// No temp left behind.
	if _, err := os.Stat(dst + ".new"); !os.IsNotExist(err) {
		t.Error("the .new temp survived a successful write")
	}
}

// A stream that dies mid-body must not leave a truncated binary in place: the
// speaker would then run, or try to run, half an engine.
func TestStreamBinaryAtomicLeavesNothingBehindOnFailure(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "go-librespot")
	body := elfBody(8 * 1024)
	broken := io.MultiReader(bytes.NewReader(body[:2048]), &erroringReader{})

	if _, _, err := streamBinaryAtomic(dst, broken, int64(len(body))); err == nil {
		t.Fatal("a broken stream was reported as a successful write")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Error("a truncated binary was left at the destination")
	}
	if _, err := os.Stat(dst + ".new"); !os.IsNotExist(err) {
		t.Error("a truncated .new temp was left behind")
	}
}

type erroringReader struct{}

func (*erroringReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// TestStreamUploadedELFRejectsNonBinaryBeforeWriting keeps the guard that
// matters on a device: something that is not a binary must never reach the
// flash, and the check has to happen on the peeked head rather than after the
// whole body has been stored.
func TestStreamUploadedELFRejectsNonBinaryBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "go-librespot")
	s := &Server{logger: slog.New(slog.DiscardHandler)}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := s.streamUploadedELF(w, r, s.logger, "sidecar", dst); ok {
			t.Error("a non-ELF upload was accepted")
		}
	}))
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/octet-stream",
		strings.NewReader("<html>this is the web UI, not a binary</html>"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Error("a non-binary body was written to the destination anyway")
	}
	if _, err := os.Stat(dst + ".new"); !os.IsNotExist(err) {
		t.Error("a non-binary body left a temp behind")
	}
}

// The happy path end to end, through a real HTTP request, so the peek does not
// eat the first four bytes of the binary it is checking.
func TestStreamUploadedELFRoundTrip(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "go-librespot")
	s := &Server{logger: slog.New(slog.DiscardHandler)}
	body := elfBody(256 * 1024)

	var gotSum string
	var gotSize int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sum, size, ok := s.streamUploadedELF(w, r, s.logger, "sidecar", dst)
		if !ok {
			t.Error("a valid ELF upload was rejected")
			return
		}
		gotSum, gotSize = sum, size
	}))
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/octet-stream", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if gotSize != int64(len(body)) {
		t.Errorf("size = %d, want %d", gotSize, len(body))
	}
	want := sha256.Sum256(body)
	if gotSum != hex.EncodeToString(want[:]) {
		t.Error("the reported hash is not the hash of the upload")
	}
	on, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(on, body) {
		t.Error("the stored binary differs from the upload; the ELF peek may have consumed its head")
	}
}

// The buffering variant must keep behaving exactly as before: the agent-update
// endpoint still needs the bytes after the write for its fallback tiers, so the
// shared reclaim preamble must not have changed what it does.
func TestWriteBinaryAtomicStillWorksAfterTheSplit(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "streborn-armv7l")
	body := elfBody(32 * 1024)
	if err := writeBinaryAtomic(dst, body); err != nil {
		t.Fatalf("writeBinaryAtomic: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Error("the buffered write no longer stores the exact bytes")
	}
	if _, err := os.Stat(dst + ".new"); !os.IsNotExist(err) {
		t.Error("the .new temp survived a successful buffered write")
	}
}
