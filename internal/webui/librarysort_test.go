package webui

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/dlna"
)

// libraryAZCases is the order A to Z must produce. The desktop app
// (src/librarysort.test.js) and the phone page (src/phonelibrarysort.test.js)
// pin the very same list, so the three copies of the rule cannot drift.
var libraryAZCases = struct{ in, want []string }{
	in: []string{"beta", "_Inbox", "10 Years", "2 Unlimited", "Alpha", "Äpfel", "_archive",
		"track 10", "track 2", "Zebra", "apple", "Ölberg", "Ostsee"},
	want: []string{"_archive", "_Inbox", "2 Unlimited", "10 Years", "Alpha", "Äpfel", "apple",
		"beta", "Ölberg", "Ostsee", "track 2", "track 10", "Zebra"},
}

// A Linux media server sends "_Inbox" last; A to Z puts it, and the folders
// starting with a digit, first, ignores case and accents and counts numbers
// as numbers (#1257).
func TestLibraryNameLessOrdersAToZ(t *testing.T) {
	got := append([]string(nil), libraryAZCases.in...)
	sort.SliceStable(got, func(i, j int) bool { return libraryNameLess(got[i], got[j]) })
	if !reflect.DeepEqual(got, libraryAZCases.want) {
		t.Fatalf("A to Z\n got %q\nwant %q", got, libraryAZCases.want)
	}
}

func TestSortLibraryFoldersLeavesTheInputAlone(t *testing.T) {
	in := []dlna.Container{{ID: "1", Title: "b"}, {ID: "2", Title: "_a"}}
	out := sortLibraryFolders(in)
	if in[0].ID != "1" || out[0].ID != "2" {
		t.Fatalf("in %v out %v", in, out)
	}
}

// An album and a playlist keep their order under A to Z; loose files are
// sorted by title.
func TestSortLibraryTracksKeepsAlbumsAndPlaylists(t *testing.T) {
	titles := func(items []dlna.Item) []string {
		var out []string
		for _, it := range items {
			out = append(out, it.Title)
		}
		return out
	}
	album := []dlna.Item{{Title: "Zed", TrackNumber: 1}, {Title: "Ace", TrackNumber: 2}}
	if got := titles(sortLibraryTracks(album, "")); !reflect.DeepEqual(got, []string{"Zed", "Ace"}) {
		t.Errorf("numbered album resorted: %v", got)
	}
	loose := []dlna.Item{{Title: "Zed"}, {Title: "_intro"}, {Title: "Ace"}}
	if got := titles(sortLibraryTracks(loose, "object.container.storageFolder")); !reflect.DeepEqual(got, []string{"_intro", "Ace", "Zed"}) {
		t.Errorf("loose files = %v", got)
	}
	if got := titles(sortLibraryTracks(loose, "object.container.playlistContainer")); !reflect.DeepEqual(got, []string{"Zed", "_intro", "Ace"}) {
		t.Errorf("playlist resorted: %v", got)
	}
	if got := titles(sortLibraryTracks(loose, "object.container.album.musicAlbum")); !reflect.DeepEqual(got, []string{"Zed", "_intro", "Ace"}) {
		t.Errorf("album class resorted: %v", got)
	}
	// Two songs sharing a track number are not an album's numbering.
	dup := []dlna.Item{{Title: "b", TrackNumber: 1}, {Title: "a", TrackNumber: 1}}
	if got := titles(sortLibraryTracks(dup, "")); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("duplicate numbers = %v", got)
	}
}

// The phone page lists A to Z with its own copy of the rule
// (src/phonelibrarysort.test.js runs it against libraryAZCases).
func TestPhonePageSortsTheLibrary(t *testing.T) {
	for _, want := range []string{"function libSortCompare(", "strLibSort"} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("phone page is missing %q", want)
		}
	}
}
