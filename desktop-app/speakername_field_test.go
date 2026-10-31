package main

import (
	"context"
	"testing"
)

// The agent's "this speaker's name breaks its renderer" flag (#1582) reaches
// the box record the banner reads, and an older agent leaves it unset.
func TestSpeakerNameUnsafeReachesTheBoxRecord(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // no :8090 enrichment in a unit test
	b := strBoxFromVersion(ctx, "192.0.2.1", 8888,
		[]byte(`{"version":"v1.1.4","speakerNameUnsafe":"true","upnpRendererMissing":"true"}`))
	if !b.SpeakerNameUnsafe {
		t.Fatal("SpeakerNameUnsafe not set from the agent's answer")
	}
	old := strBoxFromVersion(ctx, "192.0.2.1", 8888, []byte(`{"version":"v1.1.3"}`))
	if old.SpeakerNameUnsafe {
		t.Fatal("an agent without the field must leave it unset")
	}
	if degradeToSTRNotRunning(b).SpeakerNameUnsafe {
		t.Fatal("a speaker whose agent stopped answering keeps an agent-only flag")
	}
}
