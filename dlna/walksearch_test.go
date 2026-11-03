package dlna_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/dlna"
	"github.com/JRpersonal/streborn/dlna/dlnatest"
)

// The bounded walk is the search for servers whose Search action is missing or
// broken, shared by the phone page (#666) and the desktop app (#1705). These
// tests pin its bounds and the coverage defects #666 found in the old walk.

func walkServer(t *testing.T, f *dlnatest.FakeCDS) dlna.Server {
	t.Helper()
	ts := httptest.NewServer(f)
	t.Cleanup(ts.Close)
	return dlna.Server{UDN: "uuid:test", FriendlyName: "Fake NAS", CDSControlURL: ts.URL}
}

func fillers(n int) []dlnatest.Track {
	out := make([]dlnatest.Track, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, dlnatest.Track{ID: "t" + strconv.Itoa(i), Title: "Filler " + strconv.Itoa(i),
			URL: "http://192.0.2.9/t" + strconv.Itoa(i) + ".mp3"})
	}
	return out
}

var wanted = dlnatest.Track{ID: "wanted", Title: "symbol", Artist: "Adrianne Lenker",
	Album: "abysskiss", URL: "http://192.0.2.9/symbol.mp3"}

// The wanted track sits at child index 150 of a 250 track container five levels
// down, where the old walk failed twice over: it never paged past the first 100
// children and never descended past depth 4.
func TestWalkSearchFindsADeepLateTrack(t *testing.T) {
	deep := dlnatest.Container{Tracks: fillers(250)}
	deep.Tracks[150] = wanted
	srv := walkServer(t, &dlnatest.FakeCDS{Children: map[string]dlnatest.Container{
		"0":  {Folders: []dlnatest.Folder{{ID: "c1", Title: "Music"}}},
		"c1": {Folders: []dlnatest.Folder{{ID: "c2", Title: "Folder"}}},
		"c2": {Folders: []dlnatest.Folder{{ID: "c3", Title: "Acoustic"}}},
		"c3": {Folders: []dlnatest.Folder{{ID: "c4", Title: "Adrianne Lenker"}}},
		"c4": deep,
	}})
	res := dlna.WalkSearch(context.Background(), srv, "symbol", dlna.SearchAny, dlna.WalkOptions{MaxHits: 40})
	if len(res.Items) != 1 || res.Items[0].Title != "symbol" {
		t.Fatalf("the deep, late track was not found: %+v", res.Items)
	}
	if res.RootErr != nil || res.Partial {
		t.Errorf("a fully walked tree: rootErr=%v partial=%v", res.RootErr, res.Partial)
	}
}

// Many ContentDirectory servers silently cap RequestedCount. TotalMatches, not
// the size of the page the server chose to send, decides whether a container
// is finished, or everything past the cap is unsearchable.
func TestWalkSearchPagesAServerThatCapsThePageSize(t *testing.T) {
	c := dlnatest.Container{Tracks: fillers(300)}
	c.Tracks[250] = wanted
	srv := walkServer(t, &dlnatest.FakeCDS{MaxPerPage: 100, Children: map[string]dlnatest.Container{"0": c}})
	res := dlna.WalkSearch(context.Background(), srv, "symbol", dlna.SearchAny, dlna.WalkOptions{MaxHits: 40})
	if len(res.Items) != 1 {
		t.Fatalf("a server that caps the page size hides everything past the cap: %+v", res.Items)
	}
}

// A server that ignores StartingIndex would page forever: a page that brings no
// child not already seen stops the container.
func TestWalkSearchStopsOnAServerThatIgnoresPaging(t *testing.T) {
	f := &dlnatest.FakeCDS{IgnoreStart: true, Children: map[string]dlnatest.Container{
		"0": {Tracks: fillers(dlna.WalkPageSize)},
	}}
	srv := walkServer(t, f)
	done := make(chan struct{})
	go func() {
		defer close(done)
		dlna.WalkSearch(context.Background(), srv, "nothing here", dlna.SearchAny, dlna.WalkOptions{MaxHits: 40})
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the walk never stopped on a server that ignores StartingIndex")
	}
	if _, browses := f.Counts(); browses > 3 {
		t.Errorf("want the repeated page detected at once, got %d browses", browses)
	}
}

// A library big enough to exhaust the browse budget reports a partial answer
// and never exceeds the budget.
func TestWalkSearchReportsPartialOnBudget(t *testing.T) {
	children := map[string]dlnatest.Container{}
	root := dlnatest.Container{}
	for i := 0; i < dlna.WalkMaxBrowses+10; i++ {
		id := "c" + strconv.Itoa(i)
		root.Folders = append(root.Folders, dlnatest.Folder{ID: id, Title: "Folder " + strconv.Itoa(i)})
		children[id] = dlnatest.Container{Tracks: fillers(1)}
	}
	children["0"] = root
	f := &dlnatest.FakeCDS{Children: children}
	res := dlna.WalkSearch(context.Background(), walkServer(t, f), "no such song", dlna.SearchAny, dlna.WalkOptions{MaxHits: 40})
	if len(res.Items) != 0 || !res.Partial {
		t.Fatalf("want no hits and a partial answer, got %d hits partial=%v", len(res.Items), res.Partial)
	}
	if _, browses := f.Counts(); browses > dlna.WalkMaxBrowses || res.Browses != browses {
		t.Errorf("browse budget: server saw %d, walk counted %d, cap %d", browses, res.Browses, dlna.WalkMaxBrowses)
	}
	if res.RootErr != nil {
		t.Errorf("a server that answered is not unreachable: %v", res.RootErr)
	}
}

// One file reachable through the Songs list and its album is listed once.
func TestWalkSearchDeduplicatesTheSameFile(t *testing.T) {
	other := wanted
	other.ID = "b"
	srv := walkServer(t, &dlnatest.FakeCDS{Children: map[string]dlnatest.Container{
		"0":     {Folders: []dlnatest.Folder{{ID: "songs", Title: "Songs"}, {ID: "album", Title: "abysskiss"}}},
		"songs": {Tracks: []dlnatest.Track{wanted}},
		"album": {Tracks: []dlnatest.Track{other}},
	}})
	res := dlna.WalkSearch(context.Background(), srv, "symbol", dlna.SearchAny, dlna.WalkOptions{MaxHits: 40})
	if len(res.Items) != 1 {
		t.Fatalf("the same stream URL was returned %d times", len(res.Items))
	}
}

// A video never surfaces as a song.
func TestWalkSearchSkipsNonAudio(t *testing.T) {
	srv := walkServer(t, &dlnatest.FakeCDS{Children: map[string]dlnatest.Container{
		"0": {Tracks: []dlnatest.Track{{ID: "v1", Title: "symbol", Class: "object.item.videoItem",
			Mime: "video/mp4", URL: "http://192.0.2.9/symbol.mp4"}}},
	}})
	res := dlna.WalkSearch(context.Background(), srv, "symbol", dlna.SearchAny, dlna.WalkOptions{MaxHits: 40})
	if len(res.Items) != 0 {
		t.Fatalf("a video item was offered as a song: %+v", res.Items)
	}
}

// Any matches title, artist and album; a narrowed field matches that field
// alone (#1325).
func TestWalkSearchMatchesTheField(t *testing.T) {
	srv := walkServer(t, &dlnatest.FakeCDS{Children: map[string]dlnatest.Container{
		"0": {Tracks: []dlnatest.Track{
			{ID: "t1", Title: "Rush Hour", Artist: "Jane Wiedlin", Album: "Fur", URL: "http://192.0.2.9/1.mp3"},
			{ID: "t2", Title: "Limelight", Artist: "Rush", Album: "Moving Pictures", URL: "http://192.0.2.9/2.mp3"},
			{ID: "t3", Title: "Wait for Me", Artist: "Susan Tedeschi", Album: "Hope", URL: "http://192.0.2.9/3.mp3"},
		}},
	}})
	cases := []struct {
		q, field string
		want     []string
	}{
		{"rush", dlna.SearchAny, []string{"Rush Hour", "Limelight"}},
		{"rush", "", []string{"Rush Hour", "Limelight"}},
		{"rush", dlna.SearchArtist, []string{"Limelight"}},
		{"rush", dlna.SearchTitle, []string{"Rush Hour"}},
		{"HOPE", dlna.SearchAlbum, []string{"Wait for Me"}},
		{"hope", dlna.SearchTitle, nil},
	}
	for _, c := range cases {
		res := dlna.WalkSearch(context.Background(), srv, c.q, c.field, dlna.WalkOptions{MaxHits: 40})
		var got []string
		for _, it := range res.Items {
			got = append(got, it.Title)
		}
		if len(got) != len(c.want) {
			t.Errorf("%q in %q: got %v, want %v", c.q, c.field, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%q in %q: got %v, want %v", c.q, c.field, got, c.want)
			}
		}
	}
}

// MaxHits ends the walk as soon as it is reached.
func TestWalkSearchStopsAtMaxHits(t *testing.T) {
	srv := walkServer(t, &dlnatest.FakeCDS{Children: map[string]dlnatest.Container{
		"0": {Tracks: fillers(50)},
	}})
	res := dlna.WalkSearch(context.Background(), srv, "filler", dlna.SearchAny, dlna.WalkOptions{MaxHits: 7})
	if len(res.Items) != 7 {
		t.Fatalf("want 7 hits, got %d", len(res.Items))
	}
}

// A server whose root cannot be read did not answer at all, and the walk says
// so through RootErr; the hook hears about the failed container.
func TestWalkSearchRootUnreadable(t *testing.T) {
	srv := walkServer(t, &dlnatest.FakeCDS{BrowseFaults: true})
	var heard []string
	res := dlna.WalkSearch(context.Background(), srv, "symbol", dlna.SearchAny, dlna.WalkOptions{
		MaxHits: 40,
		OnReadError: func(container string, start int, err error) {
			heard = append(heard, container+"@"+strconv.Itoa(start))
		},
	})
	if res.RootErr == nil {
		t.Fatal("an unreadable root must be reported as RootErr")
	}
	if len(heard) != 1 || heard[0] != "0@0" {
		t.Errorf("OnReadError heard %v, want the root once", heard)
	}
}

// A container below the root that cannot be read is a hole in the coverage
// (Partial), not a server that did not answer.
func TestWalkSearchSubContainerUnreadableIsPartialNotUnreachable(t *testing.T) {
	fake := &dlnatest.FakeCDS{Children: map[string]dlnatest.Container{
		"0": {Folders: []dlnatest.Folder{{ID: "gone", Title: "Gone"}}, Tracks: []dlnatest.Track{wanted}},
	}}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "<ObjectID>gone</ObjectID>") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		fake.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	srv := dlna.Server{FriendlyName: "Fake NAS", CDSControlURL: ts.URL}
	var heard []string
	res := dlna.WalkSearch(context.Background(), srv, "symbol", dlna.SearchAny, dlna.WalkOptions{
		MaxHits:     40,
		OnReadError: func(container string, _ int, _ error) { heard = append(heard, container) },
	})
	if res.RootErr != nil || len(res.Items) != 1 {
		t.Fatalf("root read and hit expected, got rootErr=%v items=%d", res.RootErr, len(res.Items))
	}
	if !res.Partial {
		t.Error("an unreadable container must mark the answer partial")
	}
	if len(heard) != 1 || heard[0] != "gone" {
		t.Errorf("OnReadError heard %v, want the one failed container", heard)
	}
}

// A deadline that is gone before the root is asked leaves the server
// unanswered: nothing of it was searched.
func TestWalkSearchExpiredContextIsUnanswered(t *testing.T) {
	srv := walkServer(t, &dlnatest.FakeCDS{Children: map[string]dlnatest.Container{"0": {Tracks: []dlnatest.Track{wanted}}}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := dlna.WalkSearch(ctx, srv, "symbol", dlna.SearchAny, dlna.WalkOptions{MaxHits: 40})
	if res.RootErr == nil || !res.Partial || res.Browses != 0 {
		t.Fatalf("want rootErr, partial and no browse, got %+v", res)
	}
}
