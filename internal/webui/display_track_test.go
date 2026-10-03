package webui

import "testing"

// splitStreamTitle mirrors the app's Recently-played heuristic: " - " is
// "Artist - Title", " / " is "Title / Artist" (flipped), no separator = title only.
func TestSplitStreamTitle(t *testing.T) {
	cases := []struct {
		in, artist, title string
	}{
		{"Nacho Sotomayor - I wonder", "Nacho Sotomayor", "I wonder"},
		{"Don't let me go / Kelvin Jones", "Kelvin Jones", "Don't let me go"}, // slash flips
		{"Just A Station Name", "", "Just A Station Name"},                    // no separator
		{"A - B - C", "A", "B - C"},                                           // first dash splits
		{"- leading dash", "", "- leading dash"},                              // empty side -> not split
		{"", "", ""},
	}
	for _, c := range cases {
		a, ti := splitStreamTitle(c.in)
		if a != c.artist || ti != c.title {
			t.Errorf("splitStreamTitle(%q) = (%q,%q), want (%q,%q)", c.in, a, ti, c.artist, c.title)
		}
	}
}

// displayTrackText applies the per-box mode; "title"/"artist" fall back to the
// full string when there is no separator, so the display is never blank.
func TestDisplayTrackText(t *testing.T) {
	full := "Nacho Sotomayor - I wonder"
	nosep := "Station Jingle"
	cases := []struct {
		modeFile, in, want string
	}{
		{"both", full, full},
		{"title", full, "I wonder"},
		{"artist", full, "Nacho Sotomayor"},
		{"title", nosep, nosep},  // no artist/title split -> full
		{"artist", nosep, nosep}, // no artist -> full
		{"", full, full},         // unknown mode -> both
	}
	for _, c := range cases {
		s := &Server{displayTrackPath: t.TempDir() + "/dt"}
		if c.modeFile != "" {
			if err := writeFlagFile(modePathFor(s.displayTrackPath), c.modeFile); err != nil {
				t.Fatal(err)
			}
		}
		if got := s.displayTrackText(c.in); got != c.want {
			t.Errorf("mode=%q displayTrackText(%q) = %q, want %q", c.modeFile, c.in, got, c.want)
		}
	}
}

// A display push re-buffers the box (an audible gap, #500), so a title change
// that would put the same text on screen for the same play must not push again,
// while a new play always gets its first push.
func TestDisplayAlreadyShows(t *testing.T) {
	s := &Server{}
	play := &lastPlayInfo{boxURL: "http://127.0.0.1:8888/stream/3"}
	s.lastPlay = play
	if s.displayAlreadyShows("Lobo") {
		t.Fatal("nothing pushed yet, but reported as shown")
	}
	s.noteDisplayShown(play, "Lobo")
	if !s.displayAlreadyShows("Lobo") {
		t.Error("same text for the same play should count as shown")
	}
	if s.displayAlreadyShows("Die Prinzen") {
		t.Error("different text must push")
	}
	if s.displayAlreadyShows("") {
		t.Error("empty text is never 'shown'")
	}
	s.lastPlay = &lastPlayInfo{boxURL: play.boxURL}
	if s.displayAlreadyShows("Lobo") {
		t.Error("a new play must get its first push even with the same text")
	}
}
