package webui

import (
	"net/url"
	"os"
	"testing"
)

// #1339: the desktop app asks the selected speaker for the removal notes every
// few seconds while its Recently-played tab is open. That answer comes from
// memory, not the flash: removing the file underneath does not change it, and
// a removal on the phone is in the very next answer.
func TestFavoriteRemovalNotesAreServedFromMemory(t *testing.T) {
	s := favServer(t)
	putFavs(t, s, twoFavs)
	deleteFav(t, s, "id=u-1", "192.168.1.20:5555")
	if notes := favRemovals(t, s); len(notes) != 1 || notes[0].ID != "u-1" {
		t.Fatalf("notes after the first removal = %+v", notes)
	}
	if err := os.Remove(favoriteRemovalsPath); err != nil {
		t.Fatal(err)
	}
	if notes := favRemovals(t, s); len(notes) != 1 || notes[0].ID != "u-1" {
		t.Fatalf("the quick check went back to the flash: %+v", notes)
	}
	deleteFav(t, s, "id="+url.QueryEscape("NAS folder|http://192.0.2.5:8200/folder"), "192.168.1.20:5555")
	notes := favRemovals(t, s)
	if len(notes) != 2 || notes[0].ID != "NAS folder|http://192.0.2.5:8200/folder" {
		t.Fatalf("a fresh removal is not in the next answer: %+v", notes)
	}
	if _, err := os.Stat(favoriteRemovalsPath); err != nil {
		t.Fatalf("the notes were not written back to the flash: %v", err)
	}
}

// A caller must not be able to change the cached notes through the slice it
// was handed.
func TestFavoriteRemovalNotesCacheIsCopied(t *testing.T) {
	s := favServer(t)
	putFavs(t, s, twoFavs)
	deleteFav(t, s, "id=u-1", "192.168.1.20:5555")
	got := loadFavoriteRemovals()
	got[0].ID = "changed"
	if again := loadFavoriteRemovals(); again[0].ID != "u-1" {
		t.Fatalf("the cache was changed through a returned slice: %+v", again)
	}
}
