package webui

import "testing"

// A preset stores several candidate logo URLs separated by "|", ordered by how
// likely each is to belong to the station. The speaker takes exactly one, and
// taking the FIRST one meant handing it a vector logo or a 16-pixel favicon
// most of the time: Energy NRJ leads with an .svg, Absolut Relax with an .ico.
// The speaker accepts the URL and reports artImageStatus="IMAGE_PRESENT", and
// then nothing is drawn, which is what "the logo above the station name is gone"
// looked like from the outside (2026-08-05).
func TestFirstArtURLPrefersSomethingDrawable(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			"skips the leading svg",
			"https://e.de/logo.svg|https://icons.example/e.png",
			"https://icons.example/e.png",
		},
		{
			"skips the leading favicon",
			"https://icons.example/ip3/absolutradio.de.ico|https://cdn.example/cover.jpg",
			"https://cdn.example/cover.jpg",
		},
		{
			"takes the first entry when it is already drawable",
			"https://cdn.example/a.png|https://cdn.example/b.png",
			"https://cdn.example/a.png",
		},
		{
			"ignores a cache-busting query when judging the extension",
			"https://e.de/logo.svg|https://cdn.example/cover.jpg?v=3",
			"https://cdn.example/cover.jpg?v=3",
		},
		{
			"falls back to the head when nothing is drawable, rather than to nothing",
			"https://e.de/logo.svg|https://icons.example/e.ico",
			"https://e.de/logo.svg",
		},
		{"a single entry is used as is", "https://cdn.example/a.png", "https://cdn.example/a.png"},
		{"empty stays empty", "", ""},
		{"uppercase extensions count too", "https://e.de/l.SVG|https://e.de/L.PNG", "https://e.de/L.PNG"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstArtURL(tc.in); got != tc.want {
				t.Errorf("firstArtURL(%q)\n got  %q\n want %q", tc.in, got, tc.want)
			}
		})
	}
}
