package webui

import (
	"testing"

	"github.com/JRpersonal/streborn/internal/recent"
)

// TestRecentRadioStaleTitleSuppressed covers #755: after a station change the
// box's now-playing (ICY) text can still name the PREVIOUS station's last song
// for a beat, and that lagged title must not be recorded under the new station.
func TestRecentRadioStaleTitleSuppressed(t *testing.T) {
	s := &Server{recent: recent.New()}

	// Station A plays a song.
	s.recentNoteCard("radio", "klove", "KLove", "", "http://klove", "", "")
	s.recentNoteRadioTrack("Phil Wickham - This is Our God")

	// Switch to station B. Its first ICY title is still A's last song (lagged).
	s.recentNoteCard("radio", "rush", "Exclusively Rush", "", "http://rush", "", "")
	s.recentNoteRadioTrack("Phil Wickham - This is Our God") // stale: must be dropped
	s.recentNoteRadioTrack("AC/DC - Thunderstruck")          // B's real song

	all := s.recent.All()
	for _, e := range all {
		if e.CardName == "Exclusively Rush" && e.Track == "Phil Wickham - This is Our God" {
			t.Fatalf("stale carry-over recorded under the wrong station: %+v", e)
		}
	}
	// The rock station's real song is recorded under it.
	var rushTrack string
	for _, e := range all {
		if e.CardKey == "rush" {
			rushTrack = e.Track
		}
	}
	if rushTrack != "AC/DC - Thunderstruck" {
		t.Fatalf("rush station's real song not recorded (got %q): %+v", rushTrack, all)
	}

	// A real repeat of the same song on the SAME station still records (the guard
	// is only for the first title after a switch, and is spent once a different
	// title arrives).
	s.recentNoteRadioTrack("AC/DC - Back in Black")
	found := false
	for _, e := range s.recent.All() {
		if e.CardKey == "rush" && e.Track == "AC/DC - Back in Black" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a later track on the same station was wrongly suppressed")
	}
}
