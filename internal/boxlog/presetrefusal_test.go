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

// TestPresetRefusalsSinceKeepsTheReason: the reconcile counts the refusals of
// one sweep and needs to know why each happened. Field bundles 2026-10-26: a
// sweep with one Spotify key (UPnP form) logged exactly one "invalid SourceID"
// refusal, a sweep with two Spotify keys exactly two, every time.
func TestPresetRefusalsSinceKeepsTheReason(t *testing.T) {
	r := &Reader{forensics: newForensics()}
	if got := r.PresetRefusalsSince(time.Time{}); len(got) != 0 {
		t.Fatalf("nothing seen yet, got %v", got)
	}
	const sourceID = `Oct 26 10:11:03 rhino local0.err BoseApp[1943]: [(002150):Presets:ERROR]UpdatePresetFailureCB Update or Add preset failed with Request Failed, Marge returned: AddPreset - failed due to invalid SourceID`
	const wrongState = `Oct 22 19:04:57 spotty local0.err BoseApp[1943]: [(002150):Presets:ERROR]UpdatePresetFailureCB Update or Add preset failed with MargeHSM is in the wrong state`
	t0 := time.Date(2026, 10, 26, 15, 11, 3, 0, time.UTC)
	r.forensics.observe(sourceID, t0)
	r.forensics.observe(sourceID, t0.Add(time.Second))
	r.forensics.observe(wrongState, t0.Add(2*time.Second))

	got := r.PresetRefusalsSince(t0.Add(500 * time.Millisecond))
	if len(got) != 2 {
		t.Fatalf("want the two refusals at or after the cut, got %v", got)
	}
	if !got[0].InvalidSourceID || got[1].InvalidSourceID {
		t.Fatalf("reasons misread: %+v", got)
	}
	for i := 0; i < 2*presetRefusalKeep; i++ {
		r.forensics.observe(sourceID, t0.Add(time.Duration(10+i)*time.Second))
	}
	if n := len(r.PresetRefusalsSince(time.Time{})); n != presetRefusalKeep {
		t.Fatalf("the record must stay bounded at %d, holds %d", presetRefusalKeep, n)
	}
	var nilReader *Reader
	if nilReader.PresetRefusalsSince(time.Time{}) != nil {
		t.Fatal("nil reader must report nothing")
	}
}
