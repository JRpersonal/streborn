package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/dlna"
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
	librarySearchFn = func(ctx context.Context, srv dlna.Server, query, field string) (dlna.BrowseResult, error) {
		mu.Lock()
		gotField = field
		mu.Unlock()
		switch srv.FriendlyName {
		case "Zeta NAS":
			// Five matches, one handed over: the list is not the whole answer.
			return dlna.BrowseResult{TotalMatches: 5, Items: []dlna.Item{
				{ID: "z1", Title: "Tom Sawyer", Class: "object.item.audioItem.musicTrack", StreamURL: "http://192.0.2.2/z1.flac"},
			}}, nil
		case "Alpha NAS":
			return dlna.BrowseResult{Items: []dlna.Item{
				{ID: "a1", Title: "Limelight", Class: "object.item.audioItem.musicTrack", StreamURL: "http://192.0.2.1/a1.flac"},
				{ID: "a2", Title: "cover.jpg", Class: "object.item.imageItem.photo", StreamURL: "http://192.0.2.1/a2.jpg"},
			}}, nil
		case "Sleepy NAS":
			<-ctx.Done()
			return dlna.BrowseResult{}, ctx.Err()
		}
		return dlna.BrowseResult{}, errors.New("UPnPError 401: no Search action")
	}
	parent, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	out := searchServers(parent, []dlna.Server{
		{FriendlyName: "Zeta NAS"}, {FriendlyName: "No Search"}, {FriendlyName: "Alpha NAS"}, {FriendlyName: "Sleepy NAS"},
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
	if len(out.Unanswered) != 2 || out.Unanswered[0] != "No Search" || out.Unanswered[1] != "Sleepy NAS" {
		t.Errorf("unanswered = %v, want No Search and Sleepy NAS", out.Unanswered)
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
