package main

import (
	"fmt"
	"strings"
	"testing"
)

// The box-to-box transfer writes the WHOLE preset set in one request. Writing
// slot by slot made the target judge every write against a half-written store,
// so a user whose two speakers held the same six stations in a DIFFERENT ORDER
// got a raw 409 dialog and a failed transfer (#882).

// sixStations is the reported situation: both speakers hold these six.
func sixStations() []Preset {
	names := []string{"101 SMOOTH JAZZ", "WDR 5", "1LIVE", "Sunshine Live", "Radio Bob", "Exclusively Rush"}
	var out []Preset
	for i, n := range names {
		out = append(out, Preset{Slot: i + 1, Name: n, Type: "radio", StreamURL: "http://example.com/" + n})
	}
	return out
}

// TestCopySendsTheWholeSetInOneRequest is the reported case end to end: the
// source holds the same six stations as the target but with keys 1 and 6
// swapped. One bulk request must carry the set, and no per-slot write may
// happen at all.
func TestCopySendsTheWholeSetInOneRequest(t *testing.T) {
	source := sixStations()
	source[0], source[5] = source[5], source[0]
	source[0].Slot, source[5].Slot = 1, 6

	src := newFakeBox(t, "127.0.0.2", source)
	dst := newFakeBox(t, "127.0.0.3", nil)
	dst.bulk = true
	installProbe(t, func() bool { return true })

	a := copyApp(t)
	n, err := a.CopyPresetsAcrossBoxes(src.host, src.port(t), dst.host, dst.port(t))
	if err != nil {
		t.Fatalf("the swapped set failed to transfer: %v", err)
	}
	if n != 6 || dst.count() != 6 {
		t.Errorf("copied %d, target holds %d, want 6 and 6", n, dst.count())
	}
	bulk, slot := dst.requests()
	if bulk != 1 || slot != 0 {
		t.Errorf("target saw %d bulk and %d per-slot writes, want exactly one bulk write", bulk, slot)
	}
	if dst.written[1] != "Exclusively Rush" || dst.written[6] != "101 SMOOTH JAZZ" {
		t.Errorf("the swap did not land: key 1 = %q, key 6 = %q", dst.written[1], dst.written[6])
	}
}

// TestCopyFallsBackToPerSlotOnAnOlderAgent: a target whose agent does not know
// the collection PUT answers 405 (404 on an even older route table). The
// transfer must quietly copy slot by slot instead of failing.
func TestCopyFallsBackToPerSlotOnAnOlderAgent(t *testing.T) {
	src := newFakeBox(t, "127.0.0.2", sixStations())
	dst := newFakeBox(t, "127.0.0.3", nil) // bulk off: an older agent
	installProbe(t, func() bool { return true })

	a := copyApp(t)
	n, err := a.CopyPresetsAcrossBoxes(src.host, src.port(t), dst.host, dst.port(t))
	if err != nil {
		t.Fatalf("copy failed against an older agent: %v", err)
	}
	if n != 6 || dst.count() != 6 {
		t.Errorf("copied %d, target holds %d, want 6 and 6", n, dst.count())
	}
	bulk, slot := dst.requests()
	if bulk != 1 || slot != 6 {
		t.Errorf("target saw %d bulk and %d per-slot writes, want one refused bulk try and six per-slot writes", bulk, slot)
	}
}

// TestCopyReportsAConflictAsAFriendlyMessage: the target refused the whole set
// because one station sits on a key the set does not rewrite. The user must
// get a message that names the station and the key, not the raw 409 body, and
// the count must be 0 because nothing was written.
func TestCopyReportsAConflictAsAFriendlyMessage(t *testing.T) {
	src := newFakeBox(t, "127.0.0.2", sixStations())
	dst := newFakeBox(t, "127.0.0.3", nil)
	dst.bulk = true
	dst.conflict = &presetConflict{Code: "already-on-slot", Name: "Exclusively Rush", Slot: 6}
	installProbe(t, func() bool { return true })

	a := copyApp(t)
	n, err := a.CopyPresetsAcrossBoxes(src.host, src.port(t), dst.host, dst.port(t))
	if err == nil {
		t.Fatal("a refused set was reported as a success")
	}
	if n != 0 {
		t.Errorf("reported %d copied, want 0: the bulk write is all-or-nothing", n)
	}
	if dst.count() != 0 {
		t.Errorf("target holds %d presets after a refused set", dst.count())
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, presetConflictPrefix) {
		t.Fatalf("error does not carry the parseable conflict shape: %q", msg)
	}
	if !strings.Contains(msg, `"Exclusively Rush"`) || !strings.Contains(msg, "key 6") {
		t.Errorf("error does not name the station and its key: %q", msg)
	}
	if strings.Contains(msg, "{") {
		t.Errorf("error still carries the raw JSON body: %q", msg)
	}
	if _, slot := dst.requests(); slot != 0 {
		t.Errorf("the transfer fell back to per-slot writes after a real refusal (%d writes)", slot)
	}
}

// TestCopyStillReportsAGenuineBulkFailure: a target that refuses the set for
// any other reason must not be silently downgraded to the per-slot path, which
// would walk straight back into the half-written-store problem.
func TestCopyStillReportsAGenuineBulkFailure(t *testing.T) {
	src := newFakeBox(t, "127.0.0.2", sixStations())
	dst := newFakeBox(t, "127.0.0.3", nil)
	dst.bulk = true
	dst.bulkFails = true
	installProbe(t, func() bool { return true })

	a := copyApp(t)
	n, err := a.CopyPresetsAcrossBoxes(src.host, src.port(t), dst.host, dst.port(t))
	if err == nil {
		t.Fatal("a rejected set was reported as a success")
	}
	if n != 0 {
		t.Errorf("reported %d copied, want 0", n)
	}
	if _, slot := dst.requests(); slot != 0 {
		t.Errorf("fell back to %d per-slot writes after a real rejection", slot)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error does not carry the target's status: %v", err)
	}
}

// TestCopySkipsSlotsWithoutAName keeps the existing filter: an empty source
// slot is not part of the set and is not written to the target.
func TestCopySkipsSlotsWithoutAName(t *testing.T) {
	src := newFakeBox(t, "127.0.0.2", []Preset{
		{Slot: 1, Name: "1LIVE", Type: "radio", StreamURL: "http://example.com/1"},
		{Slot: 2, Name: "", Type: "radio"},
		{Slot: 9, Name: "out of range", Type: "radio", StreamURL: "http://example.com/9"},
	})
	dst := newFakeBox(t, "127.0.0.3", nil)
	dst.bulk = true
	installProbe(t, func() bool { return true })

	a := copyApp(t)
	n, err := a.CopyPresetsAcrossBoxes(src.host, src.port(t), dst.host, dst.port(t))
	if err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if n != 1 || dst.count() != 1 {
		t.Errorf("copied %d, target holds %d, want 1 and 1 (%v)", n, dst.count(), dst.written)
	}
}

// TestCopyBulkCarriesEveryFieldVerbatim: a Spotify or folder key must reach the
// target with its own fields, the same guarantee the per-slot copy gave.
func TestCopyBulkCarriesEveryFieldVerbatim(t *testing.T) {
	src := newFakeBox(t, "127.0.0.2", []Preset{
		{Slot: 1, Name: "Chill", Type: "spotify", URI: "spotify:playlist:abc", Account: "user@example.com"},
		{Slot: 2, Name: "Folder", Type: "queue", Shuffle: true,
			Items: []byte(`[{"url":"http://nas/1.mp3","title":"One"}]`)},
	})
	dst := newFakeBox(t, "127.0.0.3", nil)
	dst.bulk = true
	installProbe(t, func() bool { return true })

	a := copyApp(t)
	if _, err := a.CopyPresetsAcrossBoxes(src.host, src.port(t), dst.host, dst.port(t)); err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if dst.written[1] != "Chill" || dst.written[2] != "Folder" {
		t.Errorf("target holds %v, want both keys", dst.written)
	}
}

// TestPutPresetsBulkReportsAnOlderAgentAsUnsupported pins the seam the fallback
// hangs on: 404 and 405 mean "no such endpoint", everything else is a real
// answer the caller must surface.
func TestPutPresetsBulkReportsAnOlderAgentAsUnsupported(t *testing.T) {
	dst := newFakeBox(t, "127.0.0.3", nil)
	a := copyApp(t)
	set := []Preset{{Slot: 1, Name: "1LIVE", Type: "radio", StreamURL: "http://example.com/1"}}

	supported, err := a.putPresetsBulk(dst.host, dst.port(t), set)
	if supported || err != nil {
		t.Fatalf("a 405 target reported supported=%v err=%v, want false and no error", supported, err)
	}
	dst.bulk = true
	supported, err = a.putPresetsBulk(dst.host, dst.port(t), set)
	if !supported || err != nil {
		t.Fatalf("a current target reported supported=%v err=%v, want true and no error", supported, err)
	}
	dst.conflict = &presetConflict{Code: "already-on-slot", Name: `Rush "Live"`, Slot: 4}
	supported, err = a.putPresetsBulk(dst.host, dst.port(t), set)
	if !supported || err == nil {
		t.Fatalf("a 409 reported supported=%v err=%v, want true and the conflict", supported, err)
	}
	// %q keeps a quote inside the station name escaped, which is what the
	// frontend parser undoes; the important part is that the shape is stable.
	if want := fmt.Sprintf("%s%q is already on key 4", presetConflictPrefix, `Rush "Live"`); err.Error() != want {
		t.Errorf("conflict message = %q, want %q", err.Error(), want)
	}
}
