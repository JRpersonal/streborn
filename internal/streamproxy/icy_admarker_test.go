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
