package main

import (
	"context"
	"testing"
)

// The agent's internet verdict reaches the box record, and a record served
// from the cache while the agent is silent does not carry a stale one.
func TestInternetVerdictReachesTheBoxRecord(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // no :8090 enrichment in a unit test
	body := []byte(`{"version":"v1.0.13","build":"x","internet":"unreachable","internetReason":"dns"}`)
	b := strBoxFromVersion(ctx, "192.0.2.1", 8888, body)
	if b.Internet != "unreachable" || b.InternetReason != "dns" {
		t.Fatalf("got internet=%q reason=%q, want unreachable/dns", b.Internet, b.InternetReason)
	}

	old := strBoxFromVersion(ctx, "192.0.2.1", 8888, []byte(`{"version":"v1.0.12"}`))
	if old.Internet != "" {
		t.Fatalf("an older agent must leave the field empty, got %q", old.Internet)
	}

	record, _, _ := classifyKnownBox(b, BoxInfo{}, false, true)
	if record.Internet != "" || record.InternetReason != "" {
		t.Fatalf("cached record kept a stale verdict: %q/%q", record.Internet, record.InternetReason)
	}
}
