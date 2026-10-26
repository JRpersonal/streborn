package main

import (
	"context"
	"testing"
)

// The agent's stale folder key (a key whose saved file addresses the media
// server no longer has) reaches the box record the banner reads, and an
// agent that has none, or is older, leaves it empty.
func TestStaleFolderKeyReachesTheBoxRecord(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // no :8090 enrichment in a unit test
	b := strBoxFromVersion(ctx, "192.0.2.1", 8888,
		[]byte(`{"version":"v1.0.21","staleFolderKeySlot":"1","staleFolderKeyAgeSec":"12"}`))
	if b.StaleFolderKeySlot != "1" {
		t.Fatalf("StaleFolderKeySlot = %q, want \"1\"", b.StaleFolderKeySlot)
	}
	old := strBoxFromVersion(ctx, "192.0.2.1", 8888, []byte(`{"version":"v1.0.20"}`))
	if old.StaleFolderKeySlot != "" {
		t.Fatalf("an agent without the field must leave it empty, got %q", old.StaleFolderKeySlot)
	}
}
