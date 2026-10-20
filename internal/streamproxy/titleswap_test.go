package streamproxy

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func swapServer(t *testing.T) (*Server, string) {
	t.Helper()
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	path := filepath.Join(t.TempDir(), "title-swap.json")
	s.SetTitleSwapPath(path)
	return s, path
}

func TestSwapStreamTitle(t *testing.T) {
	cases := map[string]string{
		"Sultans of swing - Dire Straits":     "Dire Straits - Sultans of swing",
		"Song - Remastered 2011 - Artist":     "Artist - Song - Remastered 2011",
		"Song – Artist":                       "Artist - Song",
		"Artist / Song":                       "Artist - Song",
		"Just a title":                        "Just a title",
		"  Sultans of swing - Dire Straits  ": "Dire Straits - Sultans of swing",
		"- Dire Straits":                      "- Dire Straits",
		// a slash in the artist name does not beat the dash
		"More Than You Know - Axwell / Ingrosso":  "Axwell / Ingrosso - More Than You Know",
		`More Than You Know - Axwell /\ Ingrosso`: `Axwell /\ Ingrosso - More Than You Know`,
		"Highway to Hell - AC/DC":                 "AC/DC - Highway to Hell",
		"AC/DC / Highway to Hell":                 "AC/DC - Highway to Hell",
	}
	for in, want := range cases {
		if got := swapStreamTitle(in); got != want {
			t.Errorf("swapStreamTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// Only the flagged station is turned round; every other station keeps the
// title as sent.
func TestSetTitleSwapsFlaggedStationOnly(t *testing.T) {
	s, _ := swapServer(t)
	const ndr, other = "http://192.0.2.1/ndr1mv", "http://192.0.2.1/other"
	if _, _, err := s.SetTitleSwap(ndr, true); err != nil {
		t.Fatal(err)
	}
	var fired []string
	s.SetOnTitle(func(title string) { fired = append(fired, title) })

	s.setTitle(ndr, "Sultans of swing - Dire Straits")
	if got := s.CurrentTitle(); got != "Dire Straits - Sultans of swing" {
		t.Errorf("flagged station title = %q", got)
	}
	s.setTitle(other, "Sultans of swing - Dire Straits")
	if got := s.CurrentTitle(); got != "Sultans of swing - Dire Straits" {
		t.Errorf("unflagged station title = %q", got)
	}
	if len(fired) != 2 || fired[0] != "Dire Straits - Sultans of swing" {
		t.Errorf("onTitle got %q", fired)
	}
}

// The flag survives a restart, and setting it to what it already is does not
// touch the flash.
func TestTitleSwapPersistsAndWritesOnlyOnChange(t *testing.T) {
	s, path := swapServer(t)
	const st = "http://192.0.2.1/ndr1mv"
	if _, _, err := s.SetTitleSwap(st, true); err != nil {
		t.Fatal(err)
	}
	s2 := &Server{logger: s.logger}
	s2.SetTitleSwapPath(path)
	if !s2.TitleSwapped(st) {
		t.Fatal("flag lost across a reload")
	}
	if got := s2.TitleSwapStations(); len(got) != 1 || got[0] != st {
		t.Errorf("stations = %q", got)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s2.SetTitleSwap(st, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("an unchanged flag was written to flash")
	}

	if _, _, err := s2.SetTitleSwap(st, false); err != nil {
		t.Fatal(err)
	}
	s3 := &Server{logger: s.logger}
	s3.SetTitleSwapPath(path)
	if s3.TitleSwapped(st) {
		t.Error("cleared flag came back after a reload")
	}
}

// Flipping the flag for the station playing now corrects the live title at
// once, and turning it off restores the title as sent.
func TestSetTitleSwapReappliesLiveTitle(t *testing.T) {
	s, _ := swapServer(t)
	const st = "http://192.0.2.1/ndr1mv"
	var fired []string
	s.SetOnTitle(func(title string) { fired = append(fired, title) })
	s.setTitle(st, "Sultans of swing - Dire Straits")

	station, title, err := s.SetTitleSwap("", true)
	if err != nil {
		t.Fatal(err)
	}
	if station != st || title != "Dire Straits - Sultans of swing" {
		t.Errorf("SetTitleSwap = %q, %q", station, title)
	}
	if !s.TitleSwapped("") {
		t.Error("current station not reported as swapped")
	}
	if _, title, _ = s.SetTitleSwap("", false); title != "Sultans of swing - Dire Straits" {
		t.Errorf("after unflag title = %q", title)
	}
	want := []string{"Sultans of swing - Dire Straits", "Dire Straits - Sultans of swing", "Sultans of swing - Dire Straits"}
	if len(fired) != len(want) {
		t.Fatalf("onTitle got %q, want %q", fired, want)
	}
	for i := range want {
		if fired[i] != want[i] {
			t.Errorf("onTitle[%d] = %q, want %q", i, fired[i], want[i])
		}
	}
}

func TestSetTitleSwapNeedsAStation(t *testing.T) {
	s, _ := swapServer(t)
	if _, _, err := s.SetTitleSwap("", true); err != ErrTitleSwapNoStation {
		t.Errorf("err = %v, want ErrTitleSwapNoStation", err)
	}
}
