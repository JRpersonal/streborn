package dlna

import (
	"strings"
	"testing"
)

// #1325: the library search can be narrowed to the album, the artist or the
// title. "Any" keeps the widened criteria of #666 word for word, because the
// phone page's search depends on it.
func TestSearchCriteriaPerField(t *testing.T) {
	const audio = `upnp:class derivedfrom "object.item.audioItem" and `
	cases := map[string]string{
		SearchAny:    audio + `(dc:title contains "rush" or upnp:artist contains "rush" or upnp:album contains "rush")`,
		"":           audio + `(dc:title contains "rush" or upnp:artist contains "rush" or upnp:album contains "rush")`,
		"bogus":      audio + `(dc:title contains "rush" or upnp:artist contains "rush" or upnp:album contains "rush")`,
		SearchTitle:  audio + `dc:title contains "rush"`,
		SearchArtist: audio + `upnp:artist contains "rush"`,
		SearchAlbum:  audio + `upnp:album contains "rush"`,
	}
	for field, want := range cases {
		if got := SearchCriteria("rush", field); got != want {
			t.Errorf("field %q:\n got %s\nwant %s", field, got, want)
		}
	}
}

// A quote or backslash the user typed stays inside the criteria string for
// every field, not only for the widened one.
func TestSearchCriteriaEscapesEveryField(t *testing.T) {
	for _, f := range []string{SearchAny, SearchTitle, SearchArtist, SearchAlbum} {
		got := SearchCriteria(`a"b\`, f)
		if !strings.Contains(got, `contains "a\"b\\"`) {
			t.Errorf("field %q: %s", f, got)
		}
	}
}
