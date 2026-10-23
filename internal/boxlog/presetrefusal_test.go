package boxlog

import (
	"testing"
	"time"
)

// TestPresetRefusalRecorded: the firmware's UpdatePresetFailureCB line is the
// only sign a preset rewrite was refused (the CLI acknowledges it and the slot
// stays listed), so its time must be readable even when the class's log gap
// keeps a repeat out of the agent log.
func TestPresetRefusalRecorded(t *testing.T) {
	r := &Reader{forensics: newForensics()}
	if !r.LastPresetRefusal().IsZero() {
		t.Fatal("no refusal seen yet, want zero time")
	}
	now := time.Date(2026, 10, 22, 19, 4, 57, 0, time.UTC)
	r.forensics.observe(lineMargeUpdate, now)
	if got := r.LastPresetRefusal(); !got.Equal(now) {
		t.Fatalf("refusal at %v, want %v", got, now)
	}
	later := now.Add(2 * time.Second)
	r.forensics.observe(lineMargeUpdate, later)
	if got := r.LastPresetRefusal(); !got.Equal(later) {
		t.Fatalf("a repeat inside the log gap must still move the stamp: got %v, want %v", got, later)
	}
	// Another marge error is not a preset refusal.
	r.forensics.observe(`Sep  6 18:52:40 rhino local0.err BoseApp[1943]: [(002150):MargeClient:ERROR]AddRecentCB Failed`, later.Add(time.Second))
	if got := r.LastPresetRefusal(); !got.Equal(later) {
		t.Fatalf("AddRecentCB must not count as a preset refusal, stamp moved to %v", got)
	}
	var nilReader *Reader
	if !nilReader.LastPresetRefusal().IsZero() {
		t.Fatal("nil reader must report zero")
	}
}
