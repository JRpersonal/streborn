package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestFirmwareRefusedPresetWriteSince: only a refusal stamped at or after the
// sweep start belongs to the sweep; an older one (or none, or no syslog
// reader at all) does not.
func TestFirmwareRefusedPresetWriteSince(t *testing.T) {
	t.Cleanup(func() { setPresetRefusalSource(nil) })
	start := time.Date(2026, 10, 22, 19, 4, 56, 0, time.UTC)

	setPresetRefusalSource(nil)
	if firmwareRefusedPresetWriteSince(start) {
		t.Fatal("no syslog reader wired: must report no refusal")
	}

	var at time.Time
	setPresetRefusalSource(func() time.Time { return at })
	if firmwareRefusedPresetWriteSince(start) {
		t.Fatal("zero stamp: must report no refusal")
	}
	at = start.Add(-time.Second)
	if firmwareRefusedPresetWriteSince(start) {
		t.Fatal("a refusal before the sweep belongs to an earlier write")
	}
	at = start.Add(900 * time.Millisecond) // the bundle: 19:04:56.494 -> 19:04:57.327
	if !firmwareRefusedPresetWriteSince(start) {
		t.Fatal("a refusal during the sweep must be reported")
	}
}

// TestReportRewrittenSlotsIsHonest pins the 2026-10-22 SoundTouch 20 bundle:
// the firmware refused every boot-time write and STR logged "rewrote slots the
// box already held, it still lists them", which read as success. A refused
// rewrite must say so at WARN; an unrefused one must not claim more than the
// readback knows.
func TestReportRewrittenSlotsIsHonest(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	if reportRewrittenSlots(logger, nil, true) {
		t.Fatal("nothing rewritten: nothing to report")
	}
	if buf.Len() != 0 {
		t.Fatalf("nothing rewritten must log nothing, got %q", buf.String())
	}

	if !reportRewrittenSlots(logger, []int{1, 2, 3, 4, 5, 6}, true) {
		t.Fatal("a refused rewrite must be reported as refused")
	}
	out := buf.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "refused") {
		t.Fatalf("a refused rewrite must log a WARN naming the refusal, got %q", out)
	}
	if strings.Contains(out, "it still lists them") {
		t.Fatalf("the old success-sounding line must be gone, got %q", out)
	}

	buf.Reset()
	if reportRewrittenSlots(logger, []int{6}, false) {
		t.Fatal("no refusal seen: must not report one")
	}
	out = buf.String()
	if !strings.Contains(out, "level=INFO") || !strings.Contains(out, "cannot confirm") {
		t.Fatalf("an unrefused rewrite must say the list cannot confirm it, got %q", out)
	}
}
