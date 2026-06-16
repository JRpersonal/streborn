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
		{"Just A Station Name", "", "Just A Station Name"},                     // no separator
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
