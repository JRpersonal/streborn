package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapOTAJournal(t *testing.T) {
	dir := t.TempDir()

	// Over-cap file: must be trimmed to under the cap and start at a line boundary.
	path := filepath.Join(dir, "ota-history.log")
	var b strings.Builder
	for i := 0; i < 5000; i++ {
		b.WriteString("time=2026-06-23T00:00:00Z host=192.0.2.1 outcome: " + strings.Repeat("x", 30) + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	capOTAJournal(path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > maxOTAJournalBytes {
		t.Errorf("journal not capped: %d > %d", len(data), maxOTAJournalBytes)
	}
	if !strings.HasPrefix(string(data), "time=") {
		t.Errorf("trimmed journal does not start at a line boundary: %q", string(data[:40]))
	}

	// Under-cap file is left byte-for-byte untouched.
	small := filepath.Join(dir, "small.log")
	const content = "time=2026-06-23T00:00:00Z host=192.0.2.1 outcome: ok\n"
	if err := os.WriteFile(small, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	capOTAJournal(small)
	if d, _ := os.ReadFile(small); string(d) != content {
		t.Errorf("under-cap file was modified: %q", string(d))
	}
}
