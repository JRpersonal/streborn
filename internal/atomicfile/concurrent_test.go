package atomicfile

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Two goroutines writing the same file used to share one temp name: the second
// truncated the first one's temp file mid-write, so the first either renamed a
// torn mix into place or failed outright. The field case was a preset recall
// and the resume guard both updating last-play.json in the same moment
// (2026-09-08):
//
//	last-play persist failed err="rename: ... last-play.json.tmp -> last-play.json:
//	  no such file or directory"
//
// Not a cosmetic error: the file that failed to be written is the one the
// speaker resumes from after a power cut.
func TestConcurrentWritesToTheSamePathAllSucceed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "last-play.json")

	const writers = 24
	payloads := make([][]byte, writers)
	for i := range payloads {
		// Long enough that a torn write is visible rather than lucky.
		payloads[i] = bytes.Repeat([]byte(fmt.Sprintf("%02d", i)), 4096)
	}

	errs := make(chan error, writers)
	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	for i := 0; i < writers; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()
			errs <- WriteFile(p, payloads[i], 0o644)
		}(i)
	}
	start.Done()
	done.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("concurrent WriteFile: %v", err)
		}
	}

	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// Whoever won, the file must hold exactly one writer's payload, whole.
	var matched bool
	for _, want := range payloads {
		if bytes.Equal(got, want) {
			matched = true
			break
		}
	}
	if !matched {
		t.Fatalf("file holds a torn mix: %d bytes, starts %q, ends %q",
			len(got), first(got, 16), last(got, 16))
	}
	if _, err := os.Stat(p + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file left behind: %v", err)
	}
}

// Different paths must not serialize against each other, and must not collide.
func TestConcurrentWritesToDifferentPathsStayIndependent(t *testing.T) {
	dir := t.TempDir()
	const files = 12
	var wg sync.WaitGroup
	for i := 0; i < files; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := filepath.Join(dir, fmt.Sprintf("store-%02d.json", i))
			if err := WriteFile(p, []byte(fmt.Sprintf(`{"n":%d}`, i)), 0o644); err != nil {
				t.Errorf("WriteFile %s: %v", p, err)
			}
		}(i)
	}
	wg.Wait()
	for i := 0; i < files; i++ {
		p := filepath.Join(dir, fmt.Sprintf("store-%02d.json", i))
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("ReadFile %s: %v", p, err)
		}
		if want := fmt.Sprintf(`{"n":%d}`, i); string(got) != want {
			t.Errorf("%s = %q, want %q", p, got, want)
		}
	}
}

// The lock is keyed by the cleaned path, so two spellings of the same file
// share it rather than racing each other.
func TestLockIsSharedAcrossPathSpellings(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "presets.json")
	b := filepath.Join(dir, "sub", "..", "presets.json")
	if lockFor(a) != lockFor(b) {
		t.Fatal("the same file got two different locks")
	}
}

func first(b []byte, n int) []byte {
	if len(b) < n {
		return b
	}
	return b[:n]
}

func last(b []byte, n int) []byte {
	if len(b) < n {
		return b
	}
	return b[len(b)-n:]
}
