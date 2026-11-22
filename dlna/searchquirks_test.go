package dlna_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/dlna"
	"github.com/JRpersonal/streborn/dlna/dlnatest"
)

// The #1705 shapes, from the reporter's diagnostic bundles of 2026-11-22: a
// Synology NAS that drops kept-alive connections, two Windows Media Player
// shares, and one song that exists under two artists.

func wmpServer(t *testing.T, f *dlnatest.FakeCDS) dlna.Server {
	t.Helper()
	srv := walkServer(t, f)
	srv.Manufacturer, srv.ModelName = "Microsoft Corporation", "Windows Media Player Sharing"
	return srv
}

// wmpTree is Windows Media Player's layout: Music, Videos and Pictures under
// the root, the music views below Music, and the same track handed out under
// a different object id and stream URL in each view it appears in.
func wmpTree(allMusic []dlnatest.Track) map[string]dlnatest.Container {
	album := make([]dlnatest.Track, 0, len(allMusic))
	for _, tr := range allMusic {
		tr.ID = strings.Replace(tr.ID, "4_", "7_", 1)
		tr.URL = strings.Replace(tr.URL, "/4_", "/7_", 1)
		album = append(album, tr)
	}
	return map[string]dlnatest.Container{
		"0": {Folders: []dlnatest.Folder{{ID: "1", Title: "Music"}, {ID: "2", Title: "Videos"}, {ID: "3", Title: "Pictures"}}},
		"1": {Folders: []dlnatest.Folder{{ID: "4", Title: "All Music"}, {ID: "6", Title: "Artist"}, {ID: "7", Title: "Album"}}},
		"4": {Tracks: allMusic},
		"7": {Tracks: album},
	}
}

var sacrifice = dlnatest.Track{ID: "4_0000abcd-0001", Title: "Sacrifice", Artist: "Elton John",
	Album: "Sleeping with the Past", Duration: "0:05:08.000", Mime: "audio/x-flac",
	URL: "http://192.0.2.9:10243/WMPNSSv4/1/4_0000abcd-0001.flac"}

// A Windows Media Player share is searched through its All Music list, once
// per track, and that list read to its end is the whole answer: no other view
// is read, nothing is listed twice, and the list is not called incomplete.
func TestWalkSearchReadsWindowsMediaPlayerAllMusicOnly(t *testing.T) {
	music := append(fillers(30), sacrifice)
	for i := range music[:30] {
		music[i].ID, music[i].URL = "4_f"+music[i].ID, "http://192.0.2.9:10243/WMPNSSv4/1/4_f"+music[i].ID+".mp3"
	}
	f := &dlnatest.FakeCDS{Children: wmpTree(music)}
	res := dlna.WalkSearch(context.Background(), wmpServer(t, f), "sacrifice", dlna.SearchTitle, dlna.WalkOptions{MaxHits: 40})
	if len(res.Items) != 1 || res.Items[0].Artist != "Elton John" {
		t.Fatalf("items = %+v, want the one track once", res.Items)
	}
	if res.Partial || !res.ShortcutComplete || res.Shortcut != dlna.WMPAllMusicID || res.RootErr != nil {
		t.Errorf("All Music read to its end is the whole answer: %+v", res)
	}
	if _, browses := f.Counts(); browses != 1 {
		t.Errorf("browses = %d, want only the All Music list read", browses)
	}
}

// When the All Music list cannot be read, or holds no track at all, the tree
// is walked as for any other server, and the copies the views hand out under
// their own URLs still come back once.
func TestWalkSearchWalksTheTreeWhenAllMusicIsMissing(t *testing.T) {
	tree := wmpTree([]dlnatest.Track{sacrifice})
	tree["6"] = tree["4"]
	delete(tree, "4")
	f := &dlnatest.FakeCDS{Children: tree}
	res := dlna.WalkSearch(context.Background(), wmpServer(t, f), "sacrifice", dlna.SearchAny, dlna.WalkOptions{MaxHits: 40})
	if len(res.Items) != 1 {
		t.Fatalf("items = %+v, want the track found in the tree, once", res.Items)
	}
	if res.ShortcutComplete || res.RootErr != nil {
		t.Errorf("an empty All Music must not end the search: %+v", res)
	}
}

// Any server can list one track under two URLs; the same title, artist, album
// and length on one server are one track. The same title by another artist is
// another track: the reporter's "Sacrifice" by Elton John and by Sinead
// O'Connor are both kept.
func TestWalkSearchDeduplicatesOneTrackUnderTwoURLs(t *testing.T) {
	again := sacrifice
	again.ID, again.URL = "b", "http://192.0.2.9/other-url.flac"
	cover := dlnatest.Track{ID: "c", Title: "Sacrifice", Artist: "Sinead O'Connor", Album: "Two Rooms",
		Duration: "0:04:52.000", Mime: "audio/x-flac", URL: "http://192.0.2.9/c.flac"}
	srv := walkServer(t, &dlnatest.FakeCDS{Children: map[string]dlnatest.Container{
		"0":      {Folders: []dlnatest.Folder{{ID: "songs", Title: "Songs"}, {ID: "artist", Title: "Artists"}}},
		"songs":  {Tracks: []dlnatest.Track{sacrifice, cover}},
		"artist": {Tracks: []dlnatest.Track{again}},
	}})
	res := dlna.WalkSearch(context.Background(), srv, "sacrifice", dlna.SearchAny, dlna.WalkOptions{MaxHits: 40})
	if len(res.Items) != 2 || res.Items[0].Artist != "Elton John" || res.Items[1].Artist != "Sinead O'Connor" {
		t.Fatalf("items = %+v, want each artist's track once", res.Items)
	}
	if res.SlowestContainer == "" || res.SlowestBrowse <= 0 {
		t.Errorf("the slowest browse is not recorded: %q %v", res.SlowestContainer, res.SlowestBrowse)
	}
}

// Too little metadata to tell two songs apart keeps them apart.
func TestTrackKeyKeepsThinMetadataApart(t *testing.T) {
	a := dlna.Item{Title: "Intro", StreamURL: "http://192.0.2.9/a.mp3"}
	b := dlna.Item{Title: "Intro", StreamURL: "http://192.0.2.9/b.mp3"}
	if dlna.TrackKey(a) == dlna.TrackKey(b) {
		t.Error("two untagged tracks called Intro were merged")
	}
	a.Album, b.Album, a.DurationSec, b.DurationSec = "X", "X", 60, 60
	if dlna.TrackKey(a) != dlna.TrackKey(b) {
		t.Error("the same tagged track under two URLs was kept twice")
	}
	if got := dlna.DedupTracks([]dlna.Item{a, b, a}); len(got) != 1 {
		t.Errorf("DedupTracks kept %d, want 1", len(got))
	}
}

func TestIsWindowsMediaShare(t *testing.T) {
	cases := []struct {
		srv  dlna.Server
		want bool
	}{
		{dlna.Server{CDSControlURL: "http://192.0.2.3:2869/upnphost/udhisapi.dll?control=uuid:x+urn:upnp-org:serviceId:ContentDirectory"}, true},
		{dlna.Server{Manufacturer: "Microsoft Corporation", ModelName: "Windows Media Player Sharing"}, true},
		{dlna.Server{CDSControlURL: "http://192.0.2.4:50001/ContentDirectory/control", Manufacturer: "Synology Inc"}, false},
		{dlna.Server{Manufacturer: "Microsoft Corporation", ModelName: "Xbox"}, false},
	}
	for _, c := range cases {
		if got := dlna.IsWindowsMediaShare(c.srv); got != c.want {
			t.Errorf("IsWindowsMediaShare(%+v) = %v, want %v", c.srv, got, c.want)
		}
	}
}

func searchHits(n int) []dlnatest.Track {
	out := make([]dlnatest.Track, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, dlnatest.Track{ID: fmt.Sprintf("s%d", i), Title: fmt.Sprintf("Sacrifice %d", i),
			URL: fmt.Sprintf("http://192.0.2.9/s%d.mp3", i)})
	}
	return out
}

// A Search answer is only "more than it handed over" when the server really
// has more: a server that caps the page is paged, a count that is not a count
// of hits (the next page brings nothing) is corrected, and only hits left
// behind at the caller's cap say there is more.
func TestSearchPagesAndDoesNotTrustABogusCount(t *testing.T) {
	ctx := context.Background()
	capped := walkServer(t, &dlnatest.FakeCDS{SearchHits: searchHits(5), MaxPerPage: 2})
	res, err := dlna.SearchBy(ctx, capped, "sacrifice", dlna.SearchTitle, 40)
	if err != nil || len(res.Items) != 5 || res.HasMore() {
		t.Fatalf("a capped server: %d items, more=%v, err=%v; want all 5 and nothing more", len(res.Items), res.HasMore(), err)
	}

	bogus := walkServer(t, &dlnatest.FakeCDS{SearchHits: searchHits(3), SearchTotal: 5000})
	res, err = dlna.SearchBy(ctx, bogus, "sacrifice", dlna.SearchTitle, 40)
	if err != nil || len(res.Items) != 3 || res.HasMore() {
		t.Fatalf("a bogus count: %d items, more=%v, err=%v; want 3 and nothing more", len(res.Items), res.HasMore(), err)
	}

	many := walkServer(t, &dlnatest.FakeCDS{SearchHits: searchHits(10), MaxPerPage: 2})
	res, err = dlna.SearchBy(ctx, many, "sacrifice", dlna.SearchTitle, 3)
	if err != nil || len(res.Items) != 3 || !res.HasMore() {
		t.Fatalf("more hits than asked for: %d items, more=%v, err=%v; want 3 and more", len(res.Items), res.HasMore(), err)
	}
}
