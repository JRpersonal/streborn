package main

import (
	"context"
	"testing"
)

// The agent's "playback dropped out on a weak Wi-Fi signal" marker reaches the
// box record, an older agent leaves it empty, and neither a cached record nor a
// speaker whose agent is gone carries a stale one.
func TestWeakWifiDropoutReachesTheBoxRecord(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // no :8090 enrichment in a unit test
	body := []byte(`{"version":"v1.3.3","build":"x","weakWifiDropout":"POOR_SIGNAL","weakWifiDropoutSource":"radio","weakWifiDropoutAgeSec":"125"}`)
	b := strBoxFromVersion(ctx, "192.0.2.1", 8888, body)
	if b.WeakWifiDropout != "POOR_SIGNAL" || b.WeakWifiDropoutAgeSec != 125 {
		t.Fatalf("got %q / %d, want POOR_SIGNAL / 125", b.WeakWifiDropout, b.WeakWifiDropoutAgeSec)
	}

	old := strBoxFromVersion(ctx, "192.0.2.1", 8888, []byte(`{"version":"v1.3.2"}`))
	if old.WeakWifiDropout != "" || old.WeakWifiDropoutAgeSec != 0 {
		t.Fatalf("an older agent must leave the fields empty, got %q / %d", old.WeakWifiDropout, old.WeakWifiDropoutAgeSec)
	}

	record, _, _ := classifyKnownBox(b, BoxInfo{}, false, true)
	if record.WeakWifiDropout != "" || record.WeakWifiDropoutAgeSec != 0 {
		t.Fatalf("cached record kept a stale marker: %q / %d", record.WeakWifiDropout, record.WeakWifiDropoutAgeSec)
	}

	gone := degradeToSTRNotRunning(b)
	if gone.WeakWifiDropout != "" || gone.WeakWifiDropoutAgeSec != 0 {
		t.Fatalf("a speaker whose agent is gone kept the marker: %q / %d", gone.WeakWifiDropout, gone.WeakWifiDropoutAgeSec)
	}
}
