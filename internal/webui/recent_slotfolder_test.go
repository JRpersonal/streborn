package webui

import (
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/recent"
)

// #1744: play one playlist from key 6, save another playlist on key 6, press
// the key. Both used to be filed under "queue:slot:6", so the second
// playlist's songs landed in the first one's card and took its cover.
func TestSlotFolderRecentKeyNamesTheFolder(t *testing.T) {
	scott := presets.Preset{Slot: 6, Name: "Scott Stapp Collection", Type: "queue", Items: []presets.PresetItem{{URL: "http://192.0.2.10/s1.mp3"}}}
	rush := presets.Preset{Slot: 6, Name: "Rush favorites", Type: "queue", Items: []presets.PresetItem{{URL: "http://192.0.2.10/r1.mp3"}}}

	a, b := slotFolderRecentKey(6, scott), slotFolderRecentKey(6, rush)
	if a == b {
		t.Fatalf("two folders on key 6 share the card %q", a)
	}
	for _, k := range []string{a, b} {
		slot, tag, ok := slotFolderCardSlot(k)
		if !ok || slot != 6 || tag == "" {
			t.Fatalf("slotFolderCardSlot(%q) = %d %q %v, want 6, a tag, true", k, slot, tag, ok)
		}
		// The row key never lights a key: that stays the queue's bare card.
		if _, lit := queueSlotCardNum(k); lit {
			t.Fatalf("%q was read as the bare key card", k)
		}
	}
	if again := slotFolderRecentKey(6, scott); again != a {
		t.Fatalf("the same folder must keep its card: %q then %q", a, again)
	}

	// A key that knows its media-server folder keeps one card when renamed.
	ref := &presets.FolderRef{ServerUDN: "uuid:AAAA", ContainerID: "64$3"}
	named := presets.Preset{Slot: 6, Name: "Rush", Type: "queue", Folder: ref, Items: rush.Items}
	renamed := named
	renamed.Name = "Rush, the best"
	if slotFolderRecentKey(6, named) != slotFolderRecentKey(6, renamed) {
		t.Fatal("renaming a key that knows its folder must not open a new card")
	}
}

// A tagged card replays the key only while the key still holds that folder,
// also when the new folder happens to carry the same name.
func TestSlotStillHoldsFolderByTag(t *testing.T) {
	ref := func(c string) *presets.FolderRef { return &presets.FolderRef{ServerUDN: "uuid:AAAA", ContainerID: c} }
	old := presets.Preset{Slot: 6, Name: "Favourites", Type: "queue", Folder: ref("64$1"), Items: []presets.PresetItem{{URL: "http://192.0.2.10/a.mp3"}}}
	cur := old
	cur.Folder = ref("64$2")

	_, tag, _ := slotFolderCardSlot(slotFolderRecentKey(6, old))
	if !slotStillHoldsFolder(old, true, "Favourites", tag) {
		t.Fatal("the key still holds the card's folder")
	}
	if slotStillHoldsFolder(cur, true, "Favourites", tag) {
		t.Fatal("another folder under the same name must not replay as this card")
	}
}

// The rows of a key-started folder go under its own card, while GET
// /api/queue keeps naming the key (the app lights the key on it) and names
// the row card beside it for the "now playing" mark.
func TestKeyStartedFolderRowsAndQueueCard(t *testing.T) {
	s := queueSaveTestServer(t)
	s.recent = recent.New()
	p := folderKeyOn(6, "Rush favorites", "http://192.0.2.10/r1.mp3", "http://192.0.2.10/r2.mp3")
	if err := s.presets.SetSlot(p); err != nil {
		t.Fatal(err)
	}
	rowKey := slotFolderRecentKey(6, p)
	card := recentCardCtx{key: "queue:slot:6", name: p.Name, url: "http://192.0.2.10/r1.mp3", recentKey: rowKey}

	s.queue.load([]queueItem{{URL: "http://192.0.2.10/r1.mp3", Title: "One"}, {URL: "http://192.0.2.10/r2.mp3", Title: "Two"}}, 0, false, repeatOff)
	s.queueFolder = card
	s.recentNoteQueueCard(card, "audio/mpeg", 0)
	s.recentNoteQueueTrack("One", "Rush")
	s.recentNoteQueueTrack("Two", "Rush")

	for _, e := range s.recent.All() {
		if e.CardKey != rowKey {
			t.Fatalf("row filed under %q, want %q: %+v", e.CardKey, rowKey, e)
		}
	}
	snap := s.queueSnapshotWithCard()
	if snap.Card != "queue:slot:6" || snap.CardSlot != 6 {
		t.Fatalf("queue card %q card_slot %d, want queue:slot:6 and 6", snap.Card, snap.CardSlot)
	}
	if snap.RecentCard != rowKey {
		t.Fatalf("recent_card %q, want %q", snap.RecentCard, rowKey)
	}
}
