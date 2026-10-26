package webui

import (
	"context"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
)

// The #1525 tracks: the folder key 6 started, and the folder then saved on
// key 6 with the Library's "Save folder as preset" while the first one played.
var (
	staleCardOldTracks = []queueItem{
		{URL: "http://192.0.2.10:9000/disk/old-01.mp3", Title: "Old One", Mime: "audio/mpeg"},
		{URL: "http://192.0.2.10:9000/disk/old-02.mp3", Title: "Old Two", Mime: "audio/mpeg"},
	}
	staleCardNewURLs = []string{"http://192.0.2.10:9000/disk/new-01.mp3", "http://192.0.2.10:9000/disk/new-02.mp3"}
)

func folderKeyOn(slot int, name string, urls ...string) presets.Preset {
	p := queueKey("NAS", urls...)
	p.Slot, p.Name = slot, name
	return p
}

// startStaleCardQueue plays the old folder as if key 6 started it, then gives
// key 6 the new folder with a plain preset write, which NoteQueueSavedSlot never
// hears about.
func startStaleCardQueue(t *testing.T, s *Server) {
	t.Helper()
	if err := s.presets.SetSlot(folderKeyOn(6, "Old", staleCardOldTracks[0].URL, staleCardOldTracks[1].URL)); err != nil {
		t.Fatal(err)
	}
	s.queue.load(staleCardOldTracks, 0, false, repeatOff)
	s.recentQueueCard = recentCardCtx{key: "queue:slot:6", name: "Old"}
	s.queueFolder = s.recentQueueCard
	if snap := s.queueSnapshotWithCard(); snap.CardSlot != 6 || !snap.SlotChecked {
		t.Fatalf("before the overwrite: card_slot %d checked %v, want 6 true", snap.CardSlot, snap.SlotChecked)
	}
	if !s.queuePlaysSlot(6) {
		t.Fatal("before the overwrite: key 6 must count as the folder that plays")
	}
	if err := s.presets.SetSlot(folderKeyOn(6, "New", staleCardNewURLs...)); err != nil {
		t.Fatal(err)
	}
}

// A key given another folder while its old one plays goes dark, a press on it
// is not taken for the old folder, and a save of what plays stores the playing
// tracks, not the key's new folder (#1525).
func TestStaleQueueSlotCardNoLongerNamesTheKey(t *testing.T) {
	s := queueSaveTestServer(t)
	startStaleCardQueue(t, s)

	snap := s.queueSnapshotWithCard()
	if snap.Card != "queue:slot:6" {
		t.Fatalf("card rewritten to %q; Library and Recently-played match on it", snap.Card)
	}
	if snap.Slot != 0 || snap.CardSlot != 0 || !snap.SlotChecked {
		t.Fatalf("after the overwrite: slot %d card_slot %d checked %v, want 0 0 true", snap.Slot, snap.CardSlot, snap.SlotChecked)
	}
	if s.queuePlaysSlot(6) {
		t.Fatal("after the overwrite: a press on key 6 must start its new folder, not skip the old one")
	}

	p, ok := s.LiveQueuePreset(3)
	if !ok || p.Slot != 3 || len(p.Items) != 2 || p.Items[0].URL != staleCardOldTracks[0].URL {
		t.Fatalf("save of the running queue: %+v ok=%v, want the playing tracks", p, ok)
	}
}

// The old folder still sits on another key: that key is lit and counts as
// the folder that plays.
func TestStaleQueueSlotCardFallsBackToTheKeyThatHoldsTheFolder(t *testing.T) {
	s := queueSaveTestServer(t)
	if err := s.presets.SetSlot(folderKeyOn(2, "Old", staleCardOldTracks[0].URL, staleCardOldTracks[1].URL)); err != nil {
		t.Fatal(err)
	}
	startStaleCardQueue(t, s)

	if snap := s.queueSnapshotWithCard(); snap.Slot != 2 || snap.CardSlot != 0 {
		t.Fatalf("slot %d card_slot %d, want 2 0", snap.Slot, snap.CardSlot)
	}
	if !s.queuePlaysSlot(2) || s.queuePlaysSlot(6) {
		t.Fatal("key 2 holds the playing folder, key 6 no longer does")
	}
}

// A queue a key started and then saved on a second key while it played lights
// both: the card's key and the saved one (#1217 kept).
func TestQueueFromAKeySavedOnAnotherKeyLightsBoth(t *testing.T) {
	s := queueSaveTestServer(t)
	if err := s.presets.SetSlot(folderKeyOn(5, "Old", staleCardOldTracks[0].URL, staleCardOldTracks[1].URL)); err != nil {
		t.Fatal(err)
	}
	s.queue.load(staleCardOldTracks, 0, false, repeatOff)
	s.recentQueueCard = recentCardCtx{key: "queue:slot:5", name: "Old"}
	s.queueFolder = s.recentQueueCard
	p, ok := s.LiveQueuePreset(2)
	if !ok {
		t.Fatal("no preset from the live queue")
	}
	if err := s.presets.SetSlot(p); err != nil {
		t.Fatal(err)
	}
	s.NoteQueueSavedSlot(2)
	if snap := s.queueSnapshotWithCard(); snap.CardSlot != 5 || snap.Slot != 2 {
		t.Fatalf("card_slot %d slot %d, want 5 2", snap.CardSlot, snap.Slot)
	}
	if !s.queuePlaysSlot(5) || !s.queuePlaysSlot(2) || s.queuePlaysSlot(3) {
		t.Fatal("keys 5 and 2 hold the playing folder, key 3 does not")
	}
}

// The hardware press on the overwritten key starts its new folder instead of
// skipping to the old folder's next track (#1525).
func TestHardwareKeyOnOverwrittenFolderKeyStartsTheNewFolder(t *testing.T) {
	s, _ := newPlayTestServer(t)
	startStaleCardQueue(t, s)

	if !s.RecallSlot(context.Background(), 6) {
		t.Fatal("RecallSlot did not claim the folder key")
	}
	items, _, active := s.queue.contents()
	if !active || len(items) != 2 || items[0].URL != staleCardNewURLs[0] {
		t.Fatalf("queue after the press: active=%v items=%+v, want the new folder", active, items)
	}
}

// A folder key holding an item without a URL still matches its own queue:
// presetItemsToQueue drops that item when the key starts, so counting it on
// the key's side left the key dark and a press restarted the folder (#1525).
func TestFolderKeyWithAnEmptyItemStillHoldsItsQueue(t *testing.T) {
	s := queueSaveTestServer(t)
	key := folderKeyOn(6, "Old", staleCardOldTracks[0].URL, "", staleCardOldTracks[1].URL)
	if err := s.presets.SetSlot(key); err != nil {
		t.Fatal(err)
	}
	stored, ok := s.presets.Get(6)
	if !ok || len(stored.Items) != 3 {
		t.Fatalf("store kept %d items, want 3 (the empty one included)", len(stored.Items))
	}
	s.queue.load(presetItemsToQueue(stored.Items), 0, false, repeatOff)
	s.recentQueueCard = recentCardCtx{key: "queue:slot:6", name: "Old"}
	s.queueFolder = s.recentQueueCard

	if snap := s.queueSnapshotWithCard(); snap.CardSlot != 6 || !snap.SlotChecked {
		t.Fatalf("card_slot %d checked %v, want 6 true", snap.CardSlot, snap.SlotChecked)
	}
	if !s.queuePlaysSlot(6) {
		t.Fatal("key 6 holds the playing folder")
	}
	if presetHoldsQueueTracks(folderKeyOn(6, "Empty", "", ""), nil) {
		t.Fatal("a key with no real track holds no queue")
	}
}
