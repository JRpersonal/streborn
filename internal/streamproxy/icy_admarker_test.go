package streamproxy

import "testing"

func TestAdMarkerTitlesAreRecognised(t *testing.T) {
	for title, want := range map[string]bool{
		"adbreak_end":                true,
		"adbreak_start_length_90000": true,
		"ADBREAK_END":                true,
		"  adbreak_end  ":            true,
		"adbreak":                    true,
		"ad_break_start":             true,
		"ADINJECT_LENGTH_180000":     true,
		"adinject":                   true,
		"AD":                         true,
		"AD - Live":                  false,
		"ad_inject_end":              true,
		"Ad_Lib (Live)":              false,
		"AC_DC":                      false,
		"Adbreak Blues":              false,
		"The Beatles - Help!":        false,
		"adbreakers - live":          false,
		"":                           false,
	} {
		if got := isAdMarkerTitle(title); got != want {
			t.Errorf("isAdMarkerTitle(%q) = %v, want %v", title, got, want)
		}
	}
}

// Issue #1838: Bel RTL frames every ad break with cue titles, and the speaker
// showed "adbreak_end" under the station name. The cue must neither replace
// the title nor reach the display.
func TestAnAdMarkerKeepsTheTitleThatWasShowing(t *testing.T) {
	s := newTitleServer()
	var pushed []string
	s.SetOnTitle(func(title string) { pushed = append(pushed, title) })
	s.noteStreamStart("http://radio/a")
	s.setTitle("http://radio/a", "The Beatles - Help!")
	s.setTitle("http://radio/a", "adbreak_start_length_90000")
	s.setTitle("http://radio/a", "adbreak_end")
	if got := s.CurrentTitle(); got != "The Beatles - Help!" {
		t.Fatalf("title after an ad break = %q, want the song before it", got)
	}
	if len(pushed) != 1 || pushed[0] != "The Beatles - Help!" {
		t.Fatalf("titles pushed to the display = %q, want only the song", pushed)
	}
}

// Discussion #1855: Nostalgie sends its playout record number on both sides of
// the dash for every track, and Skyrock appends one behind the song. Neither
// belongs on the display.
func TestRecordNumbersAreNotTitles(t *testing.T) {
	for title, want := range map[string]bool{
		"9999999 - 9999999":   true,
		"755 - 755":           true,
		"1991 - 1991":         true,
		"Prince - 1999":       false,
		"1999":                false,
		"1999 - 2000":         false,
		"Blink - Blink":       false,
		" - ":                 false,
		"The Beatles - Help!": false,
	} {
		if got := isRecordNumberTitle(title); got != want {
			t.Errorf("isRecordNumberTitle(%q) = %v, want %v", title, got, want)
		}
	}
	for in, want := range map[string]string{
		"The Weeknd - Blinding Lights §6164181": "The Weeknd - Blinding Lights",
		"Juice WRLD - Lucid Dreams§7761267":     "Juice WRLD - Lucid Dreams",
		"Artist - Song §12":                     "Artist - Song §12",
		"Prince - 1999":                         "Prince - 1999",
	} {
		if got := stripRecordSuffix(in); got != want {
			t.Errorf("stripRecordSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestARecordNumberNeverReachesTheDisplay(t *testing.T) {
	s := newTitleServer()
	var pushed []string
	s.SetOnTitle(func(title string) { pushed = append(pushed, title) })
	s.noteStreamStart("http://radio/a")
	s.setTitle("http://radio/a", "9999999 - 9999999")
	if got := s.CurrentTitle(); got != "" {
		t.Fatalf("title = %q, want none for a bare record number", got)
	}
	// Latin-1 on the wire, as Skyrock sends it.
	s.setTitle("http://radio/a", "The Weeknd - Blinding Lights §6164181")
	if got := s.CurrentTitle(); got != "The Weeknd - Blinding Lights" {
		t.Fatalf("title = %q, want the song without the number", got)
	}
	if len(pushed) != 1 || pushed[0] != "The Weeknd - Blinding Lights" {
		t.Fatalf("titles pushed to the display = %q, want only the song", pushed)
	}
}
