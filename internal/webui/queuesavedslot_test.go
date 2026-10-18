package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
)

// A folder started with "Play folder" and then saved on a key lights that key:
// GET /api/queue names the key as slot, while the card keeps naming the
// folder, which Library and Recently-played match on (#1217).
func TestQueueSavedSlotLightsTheKey(t *testing.T) {
	s := queueSaveTestServer(t)
	const folderKey = "queue:uuid:abc:64$1"
	s.queue.load(queueSaveTracks, 0, false, repeatOff)
	s.queueFolder = recentCardCtx{key: folderKey, name: "Abbey Road"}
	if snap := s.queueSnapshotWithCard(); snap.Slot != 0 {
		t.Fatalf("not saved yet: slot %d", snap.Slot)
	}
	if s.queuePlaysSlot(3) {
		t.Fatal("not saved yet: key 3 does not play")
	}

	rec := httptest.NewRecorder()
	s.handleQueueSaveSlot(rec, httptest.NewRequest(http.MethodPost, "/api/queue/save-slot", strings.NewReader(`{"slot":3}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	snap := s.queueSnapshotWithCard()
	if snap.Slot != 3 {
		t.Fatalf("after the save: slot %d, want 3", snap.Slot)
	}
	if s.queueFolder.key != folderKey {
		t.Fatalf("folder card rewritten to %q", s.queueFolder.key)
	}
	if !s.queuePlaysSlot(3) || s.queuePlaysSlot(2) {
		t.Fatal("pressing key 3 must count as the folder that plays, key 2 not")
	}

	// Shuffle toggled during play: still the same folder on the key.
	s.queue.setShuffle(true)
	if got := s.queueSnapshotWithCard().Slot; got != 3 {
		t.Fatalf("after a shuffle toggle: slot %d, want 3", got)
	}

	// The key given something else: it no longer plays.
	if err := s.presets.SetSlot(presets.Preset{Slot: 3, Name: "Radio", Type: "radio", StreamURL: "http://radio.example.com/live"}); err != nil {
		t.Fatal(err)
	}
	if got := s.queueSnapshotWithCard().Slot; got != 0 {
		t.Fatalf("key overwritten: slot %d, want 0", got)
	}
	if s.queuePlaysSlot(3) {
		t.Fatal("key overwritten: it must not count as the folder that plays")
	}

	// Another folder playing under a stale record lights nothing.
	s.NoteQueueSavedSlot(3)
	if err := s.presets.SetSlot(presets.Preset{Slot: 3, Name: "Other", Type: "queue",
		Items: []presets.PresetItem{{URL: "http://192.0.2.10:9000/disk/99.mp3"}}}); err != nil {
		t.Fatal(err)
	}
	if got := s.queueSnapshotWithCard().Slot; got != 0 {
		t.Fatalf("other folder on the key: slot %d, want 0", got)
	}
	s.queue.clear()
	if got := s.queueSnapshotWithCard().Slot; got != 0 {
		t.Fatalf("queue stopped: slot %d, want 0", got)
	}
}

// GET /api/queue carries the preset store's revision, so the app re-reads the
// presets as soon as a save lands instead of on its slow timer (#1217).
func TestQueueSnapshotCarriesThePresetRevision(t *testing.T) {
	s := queueSaveTestServer(t)
	before := s.queueSnapshotWithCard().PresetRev
	if err := s.presets.SetSlot(presets.Preset{Slot: 1, Name: "Radio", Type: "radio", StreamURL: "http://radio.example.com/live"}); err != nil {
		t.Fatal(err)
	}
	after := s.queueSnapshotWithCard().PresetRev
	if after <= before || after != s.presets.Revision() {
		t.Fatalf("revision before %d, after %d, store %d", before, after, s.presets.Revision())
	}
}
