package main

import (
	"regexp"
	"testing"
)

// The share buttons point at the real announcement discussion, never at a
// placeholder.
func TestBlockfallThreadURL(t *testing.T) {
	if !regexp.MustCompile(`^https://github\.com/JRpersonal/streborn/discussions/[0-9]+$`).MatchString(blockfallThreadURL) {
		t.Fatalf("thread %s", blockfallThreadURL)
	}
}
