package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/dlna"
	"github.com/JRpersonal/streborn/dlna/dlnatest"
)

// #1325: one search across every media server the app knows, merged, each hit
// naming its server, and a server that fails or hangs only drops out of the
// result instead of holding it back.
func TestSearchServersMergesEveryServerAndNamesTheMissingOnes(t *testing.T) {
	old := librarySearchFn
	t.Cleanup(func() { librarySearchFn = old })
	// Written by every server's goroutine at once, so it needs the lock or the
	// race detector flags the test itself.
	var mu sync.Mutex
	var gotField string
	librarySearchFn = func(ctx context.Context, srv dlna.Server, query, field string) serverSearch {
		mu.Lock()
		gotField = field
		mu.Unlock()
		switch srv.FriendlyName {
		case "Zeta NAS":
			// More matches than handed over: the list is not the whole answer.
			return serverSearch{Path: searchPathSearch, Partial: true, Items: []dlna.Item{
				{ID: "z1", Title: "Tom Sawyer", Class: "object.item.audioItem.musicTrack", StreamURL: "http://192.0.2.2/z1.flac"},
			}}
		case "Alpha NAS":
			return serverSearch{Path: searchPathWalk, Items: []dlna.Item{
				{ID: "a1", Title: "Limelight", Class: "object.item.audioItem.musicTrack", StreamURL: "http://192.0.2.1/a1.flac"},
				{ID: "a2", Title: "cover.jpg", Class: "object.item.imageItem.photo", StreamURL: "http://192.0.2.1/a2.jpg"},
			}}
		case "Sleepy NAS":
			<-ctx.Done()
			return serverSearch{Path: searchPathWalk, Err: ctx.Err()}
		}
		return serverSearch{Path: searchPathWalk, Err: errors.New("connection refused")}
	}
	parent, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	out := searchServers(parent, nil, []dlna.Server{
		{FriendlyName: "Zeta NAS"}, {FriendlyName: "Dead NAS"}, {FriendlyName: "Alpha NAS"}, {FriendlyName: "Sleepy NAS"},
	}, "rush", dlna.SearchArtist)

	if gotField != dlna.SearchArtist {
		t.Errorf("field = %q, want artist passed through", gotField)
	}
	if out.Searched != 4 {
		t.Errorf("searched = %d, want 4", out.Searched)
	}
	if len(out.Items) != 2 || out.Items[0].Title != "Limelight" || out.Items[1].Title != "Tom Sawyer" {
		t.Fatalf("items = %+v, want the two tracks, Alpha's first, the image dropped", out.Items)
	}
	if out.Items[0].Server != "Alpha NAS" || out.Items[1].Server != "Zeta NAS" {
		t.Errorf("servers = %q, %q", out.Items[0].Server, out.Items[1].Server)
	}
	if len(out.Unanswered) != 2 || out.Unanswered[0] != "Dead NAS" || out.Unanswered[1] != "Sleepy NAS" {
		t.Errorf("unanswered = %v, want Dead NAS and Sleepy NAS", out.Unanswered)
	}
	if !out.Partial {
		t.Error("a server with more matches than it handed over must mark the list partial")
	}
}

func TestSearchLibraryIgnoresAnEmptyQuery(t *testing.T) {
	a := &App{}
	out, err := a.SearchLibrary("   ", dlna.SearchAny)
	if err != nil || len(out.Items) != 0 || out.Searched != 0 {
		t.Fatalf("got %+v, %v; want nothing asked", out, err)
	}
}

func fakeLibraryServer(t *testing.T, name string, f *dlnatest.FakeCDS) dlna.Server {
	t.Helper()
	ts := httptest.NewServer(f)
	t.Cleanup(ts.Close)
	return dlna.Server{UDN: "uuid:" + name, FriendlyName: name, CDSControlURL: ts.URL, Address: "192.0.2.10"}
}

// #1705: a Synology NAS and a Windows Media Player share both answered the
// Search action with "UPnP error 501 (Action Failed)". The app found nothing
// and said neither server had answered, while the phone page walked the same
// NAS and found the songs. The app now walks too: the hits come back, the
// server is not listed as unanswered, and the log names the path and the
// Search error.
func TestSearchServersWalksAServerWhoseSearchFaults(t *testing.T) {
	f := &dlnatest.FakeCDS{
		SearchFaults: true,
		Children: map[string]dlnatest.Container{
			"0":     {Folders: []dlnatest.Folder{{ID: "music", Title: "Music"}}},
			"music": {Folders: []dlnatest.Folder{{ID: "album", Title: "Moving Pictures"}}},
			"album": {Tracks: []dlnatest.Track{
				{ID: "t1", Title: "Limelight", Artist: "Rush", Album: "Moving Pictures", URL: "http://192.0.2.10/1.mp3"},
				{ID: "t2", Title: "YYZ", Artist: "Rush", Album: "Moving Pictures", URL: "http://192.0.2.10/2.mp3"},
			}},
		},
	}
	srv := fakeLibraryServer(t, "Synology", f)
	var logBuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logBuf, nil))

	out := searchServers(context.Background(), log, []dlna.Server{srv}, "limelight", dlna.SearchAny)

	if len(out.Unanswered) != 0 {
		t.Fatalf("a server that refuses Search but browses fine was reported unanswered: %v", out.Unanswered)
	}
	if len(out.Items) != 1 || out.Items[0].Title != "Limelight" || out.Items[0].Server != "Synology" {
		t.Fatalf("items = %+v, want Limelight from Synology", out.Items)
	}
	if out.Partial {
		t.Error("a walk that covered the whole tree must not be partial")
	}
	if searches, _ := f.Counts(); searches != 2 {
		t.Errorf("want the widened Search and the title-only retry before the walk, got %d Search calls", searches)
	}
	got := logBuf.String()
	for _, want := range []string{"library search: server answered", "server=Synology", "path=walk", "hits=1",
		"UPnP error 501", "titleOnlyErr="} {
		if !strings.Contains(got, want) {
			t.Errorf("log line misses %q:\n%s", want, got)
		}
	}
}

// A search narrowed to the artist never retries title-only (that would list
// songs whose TITLE matches), and the walk matches the artist alone.
func TestLibrarySearchOneArtistWalkWithoutTitleRetry(t *testing.T) {
	f := &dlnatest.FakeCDS{
		SearchFaults: true,
		Children: map[string]dlnatest.Container{
			"0": {Tracks: []dlnatest.Track{
				{ID: "t1", Title: "Rush Hour", Artist: "Jane Wiedlin", URL: "http://192.0.2.10/1.mp3"},
				{ID: "t2", Title: "Limelight", Artist: "Rush", URL: "http://192.0.2.10/2.mp3"},
			}},
		},
	}
	srv := fakeLibraryServer(t, "WMP", f)
	res := librarySearchOne(context.Background(), srv, "rush", dlna.SearchArtist)
	if res.Err != nil || res.Path != searchPathWalk {
		t.Fatalf("got path=%q err=%v, want a walk", res.Path, res.Err)
	}
	if len(res.Items) != 1 || res.Items[0].Title != "Limelight" {
		t.Fatalf("items = %+v, want only Limelight", res.Items)
	}
	if res.SearchErr == nil || res.TitleOnlyErr != nil {
		t.Errorf("searchErr=%v titleOnlyErr=%v, want only the Search error", res.SearchErr, res.TitleOnlyErr)
	}
	if searches, _ := f.Counts(); searches != 1 {
		t.Errorf("want one Search call and no title-only retry, got %d", searches)
	}
}

// A server that answers Search is used as it is: no walk.
func TestLibrarySearchOneUsesSearchWhenItAnswers(t *testing.T) {
	f := &dlnatest.FakeCDS{SearchHits: []dlnatest.Track{
		{ID: "t1", Title: "Limelight", Artist: "Rush", URL: "http://192.0.2.10/1.mp3"},
	}}
	res := librarySearchOne(context.Background(), fakeLibraryServer(t, "NAS", f), "limelight", dlna.SearchAny)
	if res.Path != searchPathSearch || len(res.Items) != 1 || res.Err != nil {
		t.Fatalf("got %+v, want the Search answer", res)
	}
	if _, browses := f.Counts(); browses != 0 {
		t.Errorf("the tree was walked although Search answered: %d browses", browses)
	}
}

// A 200 with zero hits is not proof of absence (the FRITZ!Box quirk): the walk
// runs and finds the track.
func TestLibrarySearchOneWalksWhenSearchFindsNothing(t *testing.T) {
	f := &dlnatest.FakeCDS{Children: map[string]dlnatest.Container{
		"0": {Tracks: []dlnatest.Track{{ID: "t1", Title: "Fly FRITZ!", URL: "http://192.0.2.10/1.mp3"}}},
	}}
	res := librarySearchOne(context.Background(), fakeLibraryServer(t, "FRITZ", f), "fly fritz!", dlna.SearchAny)
	if res.Path != searchPathWalk || len(res.Items) != 1 {
		t.Fatalf("got %+v, want the walk to find the track", res)
	}
}

// Only a server whose root cannot be read is unanswered, and the log says why.
func TestSearchServersUnansweredOnlyWhenTheRootCannotBeRead(t *testing.T) {
	srv := fakeLibraryServer(t, "Broken", &dlnatest.FakeCDS{SearchFaults: true, BrowseFaults: true})
	var logBuf bytes.Buffer
	out := searchServers(context.Background(), slog.New(slog.NewTextHandler(&logBuf, nil)),
		[]dlna.Server{srv}, "anything", dlna.SearchAny)
	if len(out.Unanswered) != 1 || out.Unanswered[0] != "Broken" {
		t.Fatalf("unanswered = %v, want Broken", out.Unanswered)
	}
	got := logBuf.String()
	for _, want := range []string{"library search: server could not be searched", "server=Broken", "err=", "readErrors="} {
		if !strings.Contains(got, want) {
			t.Errorf("log line misses %q:\n%s", want, got)
		}
	}
}

// A walk the per-server deadline cuts short after the root was read is a
// partial answer, not an unanswered server.
func TestLibrarySearchOneDeadlineAfterRootIsPartial(t *testing.T) {
	f := &dlnatest.FakeCDS{SearchFaults: true, Children: map[string]dlnatest.Container{
		"0": {Folders: []dlnatest.Folder{{ID: "a", Title: "A"}}},
	}}
	// The root answers; every container below it hangs until the caller gives
	// up, a NAS too slow for the time a search has.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(r.Header.Get("SOAPACTION"), "#Browse") && !strings.Contains(string(body), "<ObjectID>0</ObjectID>") {
			<-r.Context().Done()
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		f.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	srv := dlna.Server{FriendlyName: "Slow", CDSControlURL: ts.URL}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	res := librarySearchOne(ctx, srv, "zzz", dlna.SearchAny)
	if res.Err != nil {
		t.Fatalf("a server whose root answered was reported unanswered: %v", res.Err)
	}
	if !res.Partial {
		t.Errorf("a walk cut short by the deadline must be partial: %+v", res)
	}
}
