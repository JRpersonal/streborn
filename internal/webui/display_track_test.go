package webui

import "testing"

// splitStreamTitle mirrors the app's Recently-played heuristic: " - " is
// "Artist - Title", " / " is "Title / Artist" (flipped) when the title has no
// dash, no separator = title only. A dash wins over a slash, because a slash is
// also part of artist names (1LIVE, 2026-10-20: "Axwell /\ Ingrosso").
func TestSplitStreamTitle(t *testing.T) {
	cases := []struct {
		in, artist, title string
	}{
		{"Nacho Sotomayor - I wonder", "Nacho Sotomayor", "I wonder"},
		{"Don't let me go / Kelvin Jones", "Kelvin Jones", "Don't let me go"}, // slash flips (SWR3)
		{"Just A Station Name", "", "Just A Station Name"},                    // no separator
		{"A - B - C", "A", "B - C"},                                           // first dash splits
		{"- leading dash", "", "- leading dash"},                              // empty side -> not split
		{"", "", ""},
		// a slash inside the artist name never beats the dash
		{`Axwell /\ Ingrosso - More Than You Know`, `Axwell /\ Ingrosso`, "More Than You Know"},
		{"Axwell / Ingrosso - More Than You Know", "Axwell / Ingrosso", "More Than You Know"},
		{"Axwell \u039b Ingrosso - More Than You Know", "Axwell \u039b Ingrosso", "More Than You Know"},
		{"AC/DC - Highway to Hell", "AC/DC", "Highway to Hell"},
		{"Simon & Garfunkel - The Boxer", "Simon & Garfunkel", "The Boxer"},
		{"Avicii - Levels - Skrillex Remix", "Avicii", "Levels - Skrillex Remix"},
		{"Queen \u2013 Under Pressure", "Queen", "Under Pressure"},
		{"Kelvin Jones /", "", "Kelvin Jones /"},         // nothing after the slash
		{`Axwell /\ Ingrosso`, "", `Axwell /\ Ingrosso`}, // "/\" is no separator
		{"   ", "", ""},
	}
	for _, c := range cases {
		a, ti := splitStreamTitle(c.in)
		if a != c.artist || ti != c.title {
			t.Errorf("splitStreamTitle(%q) = (%q,%q), want (%q,%q)", c.in, a, ti, c.artist, c.title)
		}
	}
}

// isPlaceholderStreamTitle drops StreamTitles that carry no song (#1305) and
// keeps every real one, including short and odd-looking songs.
func TestIsPlaceholderStreamTitle(t *testing.T) {
	cases := []struct {
		in, station string
		want        bool
	}{
		{"Now playing info goes here", "Radio Bob", true},
		{"  now PLAYING info goes here  ", "", true},
		{"Unknown", "", true},
		{"Unknown - Unknown", "", true},
		{"unknown artist - unknown title", "", true},
		{"N/A", "", true},
		{"No title", "", true},
		{"Untitled", "", true},
		{"", "", true},
		{"   ", "", true},
		{"-", "", true},
		{" - ", "", true},
		{"--- ...", "", true},
		{"Radio Bob", "Radio Bob", true},
		{" RADIO BOB ", "Radio Bob", true},
		// real titles stay
		{"Queen - Under Pressure", "Radio Bob", false},
		{"Morning Show", "Radio Bob", false},
		{"Radio Bob", "", false},
		{"Radio Bob - Rock Hits", "Radio Bob", false},
		{"U2 - One", "", false},
		{"Unknown Pleasures", "", false},
		{"Untitled #1 - Sigur Ros", "", false},
		{"!!!", "", true},
		{"!!! - Heart of Hearts", "", false},
	}
	for _, c := range cases {
		if got := isPlaceholderStreamTitle(c.in, c.station); got != c.want {
			t.Errorf("isPlaceholderStreamTitle(%q, %q) = %v, want %v", c.in, c.station, got, c.want)
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
	s.play.last = play
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
	s.play.last = &lastPlayInfo{boxURL: play.boxURL}
	if s.displayAlreadyShows("Lobo") {
		t.Error("a new play must get its first push even with the same text")
	}
}
