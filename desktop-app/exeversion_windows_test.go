//go:build windows

package main

import (
	"os"
	"testing"
)

// A file that does not exist, or a binary without a version resource (the
// test binary itself), carries no version, and must say so instead of 0.0.0.
func TestExeResourceVersionWithoutAResource(t *testing.T) {
	if _, _, _, ok := exeResourceVersion(`C:\definitely\not\here\STR-Windows.exe`); ok {
		t.Error("a missing file produced a version")
	}
	self, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	if maj, min, patch, ok := exeResourceVersion(self); ok {
		t.Errorf("the test binary has no version resource, got %d.%d.%d", maj, min, patch)
	}
}

// STR_EXE_VERSION_PROBE=<path to a released STR-Windows exe> checks the reader
// against a real, stamped build. Optional: CI has no released exe at hand.
func TestExeResourceVersionOnARealRelease(t *testing.T) {
	path := os.Getenv("STR_EXE_VERSION_PROBE")
	if path == "" {
		t.Skip("set STR_EXE_VERSION_PROBE to a released STR-Windows exe")
	}
	maj, min, patch, ok := exeResourceVersion(path)
	if !ok {
		t.Fatalf("no version read from %s", path)
	}
	t.Logf("%s carries %d.%d.%d (as %s)", path, maj, min, patch, exeVersionString(path))
}
