package webui

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// writeRandomFile creates a file of n random bytes and returns its path and
// the hex SHA256 a whole-file hash would give.
func writeRandomFile(t *testing.T, n int) (string, string) {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(p, b, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return p, hex.EncodeToString(sum[:])
}

func TestFileSHA256MatchesWholeFileHash(t *testing.T) {
	p, want := writeRandomFile(t, 300<<10)
	got, err := fileSHA256(p)
	if err != nil || got != want {
		t.Fatalf("fileSHA256 = %q, %v; want %q", got, err, want)
	}
	if _, err := fileSHA256(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a missing file must be an error, not an empty hash")
	}
}

// The binary hashes behind /api/agent/version must not cost memory in
// proportion to the file. Reading a 13-16 MB binary whole put the agent at
// 42-44 MB resident after every boot and rebooted a SoundTouch 20 in a loop
// (#1083). 8 MB here, with a budget far below it.
func TestFileSHA256DoesNotBufferTheFile(t *testing.T) {
	p, _ := writeRandomFile(t, 8<<20)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	if _, err := fileSHA256(p); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	if got := after.TotalAlloc - before.TotalAlloc; got > 1<<20 {
		t.Fatalf("hashing an 8 MB file allocated %d bytes, want well under 1 MB", got)
	}
}

func TestGoLibrespotStampWritesAndReusesTheMarker(t *testing.T) {
	p, want := writeRandomFile(t, 64<<10)
	present, sha := goLibrespotStampAt(p)
	if !present || sha != want {
		t.Fatalf("stamp = %v %q, want present %q", present, sha, want)
	}
	b, err := os.ReadFile(p + ".sha256")
	if err != nil || string(b) != want {
		t.Fatalf("marker = %q, %v; want %q", b, err, want)
	}
	// A marker at least as new as the binary is trusted as is.
	if err := os.WriteFile(p+".sha256", []byte("cached"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, sha := goLibrespotStampAt(p); sha != "cached" {
		t.Fatalf("stamp ignored a fresh marker: %q", sha)
	}
}

func TestGoLibrespotStampConcurrentPollsAgree(t *testing.T) {
	p, want := writeRandomFile(t, 256<<10)
	var wg sync.WaitGroup
	got := make([]string, 4)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, got[i] = goLibrespotStampAt(p)
		}()
	}
	wg.Wait()
	for i, g := range got {
		if g != want {
			t.Fatalf("poll %d got %q, want %q", i, g, want)
		}
	}
}

func TestGoLibrespotStampAbsentOrStub(t *testing.T) {
	dir := t.TempDir()
	if present, _ := goLibrespotStampAt(filepath.Join(dir, "none")); present {
		t.Fatal("a missing engine reported present")
	}
	stub := filepath.Join(dir, "stub")
	if err := os.WriteFile(stub, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if present, _ := goLibrespotStampAt(stub); present {
		t.Fatal("an empty stub reported present")
	}
}
