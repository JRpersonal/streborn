package webui

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/dlna"
	"github.com/JRpersonal/streborn/internal/mediaservers"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/upnp"
)

func treeTitles(items []dlna.Item) string {
	var out []string
	for _, it := range items {
		out = append(out, it.Title)
	}
	return strings.Join(out, ",")
}

// artistTree is the #1682 library: an artist folder holding one folder per
// album and no songs. The server lists the albums Z to A and each album's
// songs alphabetically, numbered in another order.
func artistTree() *fakeCDS {
	return &fakeCDS{children: map[string]fakeContainer{
		"artist": {folders: []fakeFolder{{id: "z", title: "Zenith"}, {id: "a", title: "_Anfang"}}},
		"z": {tracks: []fakeTrack{
			{id: "z1", title: "Alpha", url: "http://192.0.2.9/z1.mp3", trackNo: 2},
			{id: "z2", title: "Omega", url: "http://192.0.2.9/z2.mp3", trackNo: 1},
		}},
		"a": {tracks: []fakeTrack{
			{id: "a1", title: "Eins", url: "http://192.0.2.9/a1.mp3", trackNo: 1},
			{id: "a2", title: "Zwei", url: "http://192.0.2.9/a2.mp3", trackNo: 2},
		}},
	}}
}

// A folder of album folders plays every album, each in its track order, the
// albums in the server's order, or A to Z when the listener sorts that way.
func TestCollectFolderTracksPlaysTheAlbumsOfAnArtistFolder(t *testing.T) {
	s, srv := testSearchServer(t, artistTree())
	tree, err := s.collectFolderTracks(context.Background(), srv, "artist", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := treeTitles(tree.items); got != "Omega,Alpha,Eins,Zwei" {
		t.Errorf("server order = %s, want Omega,Alpha,Eins,Zwei", got)
	}
	if !tree.subfolders || tree.cut || tree.cutDepth || tree.folders != 3 {
		t.Errorf("tree flags = %+v", tree)
	}
	tree, err = s.collectFolderTracks(context.Background(), srv, "artist", true)
	if err != nil {
		t.Fatal(err)
	}
	if got := treeTitles(tree.items); got != "Eins,Zwei,Omega,Alpha" {
		t.Errorf("A to Z = %s, want the _Anfang album first, each in track order", got)
	}
}

// A folder that holds songs plays exactly those, as it always did: its
// sub-folders are not added.
func TestCollectFolderTracksKeepsAFolderWithSongsToItsOwn(t *testing.T) {
	f := artistTree()
	c := f.children["artist"]
	c.tracks = []fakeTrack{{id: "own", title: "Own", url: "http://192.0.2.9/own.mp3"}}
	f.children["artist"] = c
	s, srv := testSearchServer(t, f)
	tree, err := s.collectFolderTracks(context.Background(), srv, "artist", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := treeTitles(tree.items); got != "Own" || tree.subfolders {
		t.Errorf("got %s (subfolders %v), want only the folder's own song", got, tree.subfolders)
	}
}

// Folders deeper than libraryTreeMaxDepth are left out and the walk says so.
func TestCollectFolderTracksStopsAtTheDepthLimit(t *testing.T) {
	f := &fakeCDS{children: map[string]fakeContainer{}}
	// root > l1 > l2 > l3 > l4, a song on every level below the root.
	ids := []string{"root", "l1", "l2", "l3", "l4"}
	for i, id := range ids {
		c := fakeContainer{}
		if i+1 < len(ids) {
			c.folders = []fakeFolder{{id: ids[i+1], title: ids[i+1]}}
		}
		if i > 0 {
			c.tracks = []fakeTrack{{id: id + "t", title: id, url: "http://192.0.2.9/" + id + ".mp3"}}
		}
		f.children[id] = c
	}
	s, srv := testSearchServer(t, f)
	tree, err := s.collectFolderTracks(context.Background(), srv, "root", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := treeTitles(tree.items); got != "l1,l2,l3" {
		t.Errorf("got %s, want the three levels below the folder", got)
	}
	if !tree.cutDepth || tree.cut {
		t.Errorf("flags = %+v, want cutDepth only", tree)
	}
}

// A huge tree stops at libraryTreeMaxTracks songs and says it was cut. The same
// song listed in two folders plays once.
func TestCollectFolderTracksCapsTheSongsAndSkipsDuplicates(t *testing.T) {
	f := &fakeCDS{children: map[string]fakeContainer{"root": {}}}
	root := fakeContainer{}
	for a := 0; a < 12; a++ {
		id := "album" + strconv.Itoa(a)
		root.folders = append(root.folders, fakeFolder{id: id, title: id})
		c := fakeContainer{}
		for n := 0; n < 100; n++ {
			c.tracks = append(c.tracks, fakeTrack{id: id + "-" + strconv.Itoa(n), title: "Song " + strconv.Itoa(n),
				url: "http://192.0.2.9/" + id + "/" + strconv.Itoa(n) + ".mp3"})
		}
		f.children[id] = c
	}
	// album1 repeats album0's first song.
	f.children["album1"].tracks[0] = f.children["album0"].tracks[0]
	f.children["root"] = root
	s, srv := testSearchServer(t, f)
	tree, err := s.collectFolderTracks(context.Background(), srv, "root", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.items) != libraryTreeMaxTracks || !tree.cut {
		t.Fatalf("got %d songs (cut %v), want %d and cut", len(tree.items), tree.cut, libraryTreeMaxTracks)
	}
	seen := map[string]bool{}
	for _, it := range tree.items {
		if seen[it.StreamURL] {
			t.Fatalf("%s played twice", it.StreamURL)
		}
		seen[it.StreamURL] = true
	}
}

// A replayed folder card of an artist folder plays the albums again instead of
// answering "folder-empty".
func TestBrowseQueueItemsReplaysAFolderOfFolders(t *testing.T) {
	s, srv := testSearchServer(t, artistTree())
	items, err := s.browseQueueItems(context.Background(), srv, "artist")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range items {
		got = append(got, it.Title)
	}
	if !reflect.DeepEqual(got, []string{"Omega", "Alpha", "Eins", "Zwei"}) {
		t.Fatalf("replay = %v", got)
	}
}

// The endpoint obeys the same rule browse, search and the card replay do:
// registered music sources only.
func TestLibraryPlayFolderServesOnlyRegisteredServers(t *testing.T) {
	store, err := mediaservers.Load(filepath.Join(t.TempDir(), "mediaservers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add(mediaservers.Server{ID: "AAAA-BBBB", Name: "NAS"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), mediaServers: store, renderer: &upnp.Renderer{}}
	post := func(method, body string) int {
		rr := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/api/library/play-folder", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		s.handleLibraryPlayFolder(rr, r)
		return rr.Code
	}
	if c := post(http.MethodPost, `{"udn":"uuid:11111111-2222-3333-4444-555555555555","id":"64$0"}`); c != http.StatusNotFound {
		t.Errorf("unregistered server: %d, want 404", c)
	}
	if c := post(http.MethodPost, `{"id":"64$0"}`); c != http.StatusBadRequest {
		t.Errorf("no udn: %d, want 400", c)
	}
	if c := post(http.MethodGet, ``); c != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d, want 405", c)
	}
}

// The phone page asks the speaker to walk a folder of folders.
func TestPhonePagePlaysAFolderOfFoldersThroughTheSpeaker(t *testing.T) {
	for _, want := range []string{
		"'/api/library/play-folder'",
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("phone page is missing %q", want)
		}
	}
}

// A folder key saved from an artist folder whose server renumbered its files
// reads the albums again, not only the artist folder, which holds no songs.
func TestFolderKeyOfAFolderOfFoldersFindsItsTracksAgain(t *testing.T) {
	f := newFakeRenumberingServer(t, false, [2]string{"a", "Intro"}, [2]string{"b", "Song"})
	f.parentOf = "artist"
	var resolves []string
	s := reresolveServer(t, f, &resolves)
	key := presets.Preset{Slot: 3, Type: "queue", Name: "Artist", Source: "PC: Media",
		Folder: &presets.FolderRef{ServerUDN: "uuid:PC-MEDIA", ContainerID: "artist"},
		Items: []presets.PresetItem{
			{URL: f.oldURL("x1"), Title: "Intro"},
			{URL: f.oldURL("x2"), Title: "Song"},
		}}
	if err := s.presets.SetSlot(key); err != nil {
		t.Fatal(err)
	}
	pressed, _ := s.presets.Get(3)
	s.healQueuePresetIfMoved(context.Background(), 3, &pressed)
	if pressed.Items[0].URL != f.newURL("a") || pressed.Items[1].URL != f.newURL("b") {
		t.Fatalf("tracks not found again below the artist folder: %+v", pressed.Items)
	}
}
