package webui

import (
	"context"
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

// #1705 on the phone: the reporter's PC shares music with Windows Media Player,
// whose Search action never answers. The speaker spent its whole 15 s on the
// widened and the title-only query and walked the PC with 0 reads.

func phoneSearchServer(t *testing.T, h http.Handler) (*Server, dlna.Server) {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		dlna.Server{UDN: "uuid:pc", FriendlyName: "PC: account:", CDSControlURL: ts.URL}
}

var phoneSacrifice = dlnatest.Track{ID: "4_1", Title: "Sacrifice", Artist: "Elton John", Album: "Sleeping with the Past",
	Duration: "0:05:08.000", URL: "http://192.0.2.10:10243/WMPNSSv4/1/4_1.mp3"}

// A Windows Media Player share is walked without asking Search, through its
// All Music list, and that list read to its end is a complete answer.
func TestLibrarySearchWalksWindowsMediaPlayerWithoutAsking(t *testing.T) {
	f := &dlnatest.FakeCDS{
		SearchHits: []dlnatest.Track{phoneSacrifice},
		Children: map[string]dlnatest.Container{
			"0": {Folders: []dlnatest.Folder{{ID: "1", Title: "Music"}}},
			"4": {Tracks: []dlnatest.Track{phoneSacrifice}},
		},
	}
	s, srv := phoneSearchServer(t, f)
	srv.Manufacturer, srv.ModelName = "Microsoft Corporation", "Windows Media Player Sharing"
	items, partial := s.searchOneServerBy(context.Background(), srv, "sacrifice", dlna.SearchAny)
	if len(items) != 1 || partial {
		t.Fatalf("items = %d partial = %v, want the track and a complete answer", len(items), partial)
	}
	if searches, browses := f.Counts(); searches != 0 || browses != 1 {
		t.Errorf("searches = %d browses = %d, want no Search and only All Music read", searches, browses)
	}
}

// A Search action that never answers takes its share of the time, not all of
// it, and no title-only query is queued behind it: the walk still runs.
func TestLibrarySearchWalksWhenSearchNeverAnswers(t *testing.T) {
	old := librarySearchActionShare
	librarySearchActionShare = 150 * time.Millisecond
	t.Cleanup(func() { librarySearchActionShare = old })

	f := &dlnatest.FakeCDS{Children: map[string]dlnatest.Container{
		"0": {Tracks: []dlnatest.Track{phoneSacrifice}},
	}}
	var mu sync.Mutex
	searches := 0
	release := make(chan struct{})
	s, srv := phoneSearchServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("SOAPACTION"), "#Search") {
			mu.Lock()
			searches++
			mu.Unlock()
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		f.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	items, _ := s.searchOneServerBy(ctx, srv, "sacrifice", dlna.SearchAny)
	if len(items) != 1 {
		t.Fatalf("items = %d, want the walk to find the track", len(items))
	}
	mu.Lock()
	defer mu.Unlock()
	if searches != 1 {
		t.Errorf("Search asked %d times, want once (no title-only retry behind a query with no answer)", searches)
	}
}
