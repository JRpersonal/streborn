package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// makeTarGz writes a .tar.gz at path with the given name->content entries.
func makeTarGz(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestExtractLargestFile: the Linux tarball holds the binary plus maybe a small
// readme; extraction must pull the binary (the largest regular file), not the
// readme, and not hardcode a name a future build rename would break.
func TestExtractLargestFile(t *testing.T) {
	dir := t.TempDir()
	tgz := filepath.Join(dir, "app.tar.gz")
	binContent := string(bytes.Repeat([]byte("X"), 5000)) // the "binary"
	makeTarGz(t, tgz, map[string]string{
		"README.txt": "small readme",
		"ST Reborn":  binContent,
		"LICENSE":    "license text",
	})
	out := filepath.Join(dir, "extracted")
	if err := extractLargestFile(tgz, out); err != nil {
		t.Fatalf("extractLargestFile: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != binContent {
		t.Fatalf("extracted the wrong entry (len=%d, want %d)", len(got), len(binContent))
	}
}

// TestExtractLargestFileEmptyArchive: an archive with no regular files is an
// error, not a silent empty extract that would later be installed as the app.
func TestExtractLargestFile_Empty(t *testing.T) {
	dir := t.TempDir()
	tgz := filepath.Join(dir, "empty.tar.gz")
	makeTarGz(t, tgz, map[string]string{})
	if err := extractLargestFile(tgz, filepath.Join(dir, "out")); err == nil {
		t.Fatal("expected an error for an empty archive")
	}
}

// TestCopyFile round-trips bytes and overwrites an existing destination (the swap
// path copies the verified update over the freed exe path).
func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("new binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old stale longer content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "new binary" {
		t.Fatalf("dst = %q, want %q", got, "new binary")
	}
}
