package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/dlna"
	"github.com/JRpersonal/streborn/dlna/dlnatest"
)

// #1705, diagnostic bundles of 2026-11-22: a Synology NAS that refuses Search,
// and two PCs sharing with Windows Media Player, one of them never found.

// A walk that could not cover a server's library is named, so the note under
// the list says which server and why, instead of claiming the server had more
// hits than it hands over.
func TestSearchServersNamesTheServerWhoseWalkWasCutShort(t *testing.T) {
	old := librarySearchFn
	t.Cleanup(func() { librarySearchFn = old })
	librarySearchFn = func(_ context.Context, srv dlna.Server, _, _ string) serverSearch {
		hit := dlna.Item{ID: srv.FriendlyName, Title: "Sacrifice", Class: "object.item.audioItem.musicTrack",
			StreamURL: "http://192.0.2.1/" + srv.FriendlyName + ".flac"}
		if srv.FriendlyName == "NAS" {
			return serverSearch{Path: searchPathWalk, Incomplete: true, Items: []dlna.Item{hit}}
		}
		return serverSearch{Path: searchPathSearch, Items: []dlna.Item{hit}}
	}
	out := searchServers(context.Background(), nil, []dlna.Server{{FriendlyName: "NAS"}, {FriendlyName: "PC"}}, "sacrifice", dlna.SearchAny)
	if len(out.Incomplete) != 1 || out.Incomplete[0] != "NAS" || !out.Partial {
		t.Fatalf("incomplete = %v partial = %v, want NAS named and the list partial", out.Incomplete, out.Partial)
	}
	if len(out.Items) != 2 {
		t.Errorf("items = %d, want both servers' hits", len(out.Items))
	}
}

func wmpFake() *dlnatest.FakeCDS {
	sacrifice := dlnatest.Track{ID: "4_0000abcd-0001", Title: "Sacrifice", Artist: "Elton John",
		Album: "Sleeping with the Past", Duration: "0:05:08.000", Mime: "audio/x-flac",
		URL: "http://192.0.2.10:10243/WMPNSSv4/1/4_0000abcd-0001.flac"}
	inAlbum := sacrifice
	inAlbum.ID, inAlbum.URL = "7_0000abcd-0001", "http://192.0.2.10:10243/WMPNSSv4/1/7_0000abcd-0001.flac"
	return &dlnatest.FakeCDS{
		SearchHits: []dlnatest.Track{sacrifice},
		Children: map[string]dlnatest.Container{
			"0": {Folders: []dlnatest.Folder{{ID: "1", Title: "Music"}}},
			"1": {Folders: []dlnatest.Folder{{ID: "4", Title: "All Music"}, {ID: "7", Title: "Album"}}},
			"4": {Tracks: []dlnatest.Track{sacrifice}},
			"7": {Tracks: []dlnatest.Track{inAlbum}},
		},
	}
}

// Windows Media Player's share is not asked to search (on both of the
// reporter's PCs it never answered and kept the share busy), it is searched
// through its All Music list, and that list read to its end is a complete
// answer: the track once, no "may not be complete" note.
func TestLibrarySearchOneWalksWindowsMediaPlayerWithoutAsking(t *testing.T) {
	f := wmpFake()
	srv := fakeLibraryServer(t, "PC: account:", f)
	srv.Manufacturer, srv.ModelName = "Microsoft Corporation", "Windows Media Player Sharing"
	var logBuf bytes.Buffer
	out := searchServers(context.Background(), slog.New(slog.NewTextHandler(&logBuf, nil)), []dlna.Server{srv}, "sacrifice", dlna.SearchTitle)
	if searches, _ := f.Counts(); searches != 0 {
		t.Errorf("the share was asked to search %d times, want never", searches)
	}
	if len(out.Items) != 1 || out.Items[0].Title != "Sacrifice" {
		t.Fatalf("items = %+v, want the track once", out.Items)
	}
	if out.Partial || len(out.Incomplete) != 0 || len(out.Unanswered) != 0 {
		t.Errorf("All Music read to its end is the whole answer: %+v", out)
	}
	got := logBuf.String()
	for _, want := range []string{"path=walk", "shortcut=4", "shortcutComplete=true", "slowestMs=", "Windows Media Player sharing does not answer Search"} {
		if !strings.Contains(got, want) {
			t.Errorf("log line misses %q:\n%s", want, got)
		}
	}
}

// A Search answer that hands the same track out twice under two URLs is one
// row; the same title by another artist stays a row of its own.
func TestSearchServersListsATrackOnceAnswerHandsOutTwice(t *testing.T) {
	f := wmpFake()
	twice := f.SearchHits[0]
	twice.ID, twice.URL = "7_0000abcd-0001", "http://192.0.2.10:10243/WMPNSSv4/1/7_0000abcd-0001.flac"
	cover := dlnatest.Track{ID: "x", Title: "Sacrifice", Artist: "Sinead O'Connor", Album: "Two Rooms",
		Duration: "0:04:52.000", Mime: "audio/x-flac", URL: "http://192.0.2.10/x.flac"}
	f.SearchHits = append(f.SearchHits, twice, cover)
	srv := fakeLibraryServer(t, "Other PC", f)
	out := searchServers(context.Background(), nil, []dlna.Server{srv}, "sacrifice", dlna.SearchTitle)
	if len(out.Items) != 2 || out.Items[0].Artist != "Elton John" || out.Items[1].Artist != "Sinead O'Connor" {
		t.Fatalf("items = %+v, want each artist's track once", out.Items)
	}
	if out.Partial {
		t.Error("a Search that handed over every hit is not partial")
	}
}
