package recent

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAddSourceChangeReplacesTheCard: a Spotify playlist that once landed as
// a "radio" row (its spotify: URI pushed through the play-a-URL path) must not
// keep that label when the preset key then recalls it as Spotify. The fold
// used to match the card key alone, so the key recall and its first song were
// filed under "radio" and the app labelled the playlist "Radio" (#1077).
func TestAddSourceChangeReplacesTheCard(t *testing.T) {
	const key = "spotify:playlist:abc"
	s := New()
	s.Add(Entry{Source: "radio", CardKey: key, CardName: "Gypsy Soul"})
	s.Add(Entry{Source: "spotify", CardKey: key, CardName: "Gypsy Soul", CardArt: "https://i.scdn.co/image/x"})
	s.Add(Entry{Source: "spotify", CardKey: key, Track: "Song A"})

	all := s.All()
	if len(all) != 1 {
		t.Fatalf("want one row for the card, got %+v", all)
	}
	if all[0].Source != "spotify" {
		t.Fatalf("source = %q, want spotify", all[0].Source)
	}
	if all[0].Track != "Song A" || all[0].CardArt != "https://i.scdn.co/image/x" {
		t.Fatalf("row lost the Spotify card data: %+v", all[0])
	}
}

// TestAddSameSourceStillFolds: the source check must not break the ordinary
// fold of a card-start placeholder and its first song.
func TestAddSameSourceStillFolds(t *testing.T) {
	s := New()
	s.Add(Entry{Source: "spotify", CardKey: "spotify:album:x", CardName: "Album"})
	s.Add(Entry{Source: "spotify", CardKey: "spotify:album:x", Track: "One"})
	eq(t, rows(s), []string{"spotify:album:x/One"})
}

// TestLoadHealsASpotifyCardFiledAsRadio: a ring written by an older agent
// already holds the wrong label; it is corrected on the next boot.
func TestLoadHealsASpotifyCardFiledAsRadio(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recent.json")
	old := `{"recent":[
	 {"ts":"t1","source":"radio","cardKey":"swr3","cardName":"SWR3"},
	 {"ts":"t2","source":"radio","cardKey":"spotify:playlist:abc","cardName":"Gypsy Soul","track":"Song A"}
	]}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	all := s.All()
	if len(all) != 2 {
		t.Fatalf("rows = %+v", all)
	}
	if all[0].Source != "radio" {
		t.Fatalf("a real radio row was relabelled: %+v", all[0])
	}
	if all[1].Source != "spotify" {
		t.Fatalf("spotify card still labelled %q", all[1].Source)
	}
}
