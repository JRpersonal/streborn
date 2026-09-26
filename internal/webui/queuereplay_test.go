package webui

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/mediaservers"
	"github.com/JRpersonal/streborn/internal/upnp"
)

// The card key is the folder's whole identity, and it is not a clean two-field
// split: a UDN carries colons of its own. Getting this wrong means a replay
// browses the wrong container, or nothing at all.
func TestParseQueueCardKey(t *testing.T) {
	cases := []struct {
		key, udn, container string
		ok                  bool
	}{
		// The real shape the desktop app writes: uuid:... plus a DLNA object id.
		{"queue:uuid:4d696e69-44c5-1215-ab12-000000000000:64$0", "uuid:4d696e69-44c5-1215-ab12-000000000000", "64$0", true},
		// A folder that IS the server root: the app leaves the container empty.
		{"queue:uuid:AAAA-BBBB:", "uuid:AAAA-BBBB", "0", true},
		{"queue:AAAA-BBBB:1$5$3", "AAAA-BBBB", "1$5$3", true},
		// Not folder cards: a radio station, a Spotify context, a truncated key.
		{"spotify:playlist:37i9dQ", "", "", false},
		{"http://stream.example/1", "", "", false},
		{"queue:nocolon", "", "", false},
		{"queue:", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		udn, container, ok := parseQueueCardKey(c.key)
		if ok != c.ok || udn != c.udn || container != c.container {
			t.Errorf("parseQueueCardKey(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.key, udn, container, ok, c.udn, c.container, c.ok)
		}
	}
}

// The replay endpoint makes the speaker reach out to a media server, so it obeys
// the same rule browse and search do: registered music sources only, never an
// arbitrary UPnP device somebody names in the request.
func TestQueueReplayCardServesOnlyRegisteredServers(t *testing.T) {
	store, err := mediaservers.Load(filepath.Join(t.TempDir(), "mediaservers.json"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := store.Add(mediaservers.Server{ID: "AAAA-BBBB", Name: "NAS"}); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	// A renderer is needed for the handler to get past its own guard; the calls
	// below never reach it.
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), mediaServers: store, renderer: &upnp.Renderer{}}

	post := func(body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/queue/replay-card", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		s.handleQueueReplayCard(rr, r)
		return rr
	}

	if rr := post(`{"key":"queue:uuid:CCCC-DDDD:64$0"}`); rr.Code != 404 {
		t.Errorf("a server that is not a registered music source must 404, got %d", rr.Code)
	}
	// A card that is not a folder card must not be treated as one: that is what
	// keeps a radio or Spotify card on its own replay path.
	if rr := post(`{"key":"spotify:playlist:37i9dQ"}`); rr.Code != 400 {
		t.Errorf("a non-folder card key must 400, got %d", rr.Code)
	}
	if rr := post(`{"key":""}`); rr.Code != 400 {
		t.Errorf("an empty key must 400, got %d", rr.Code)
	}
}

// Both clients had the same defect and both are fixed through this one endpoint,
// so the phone page must actually call it. It also keeps the single-track path
// for the cards that really are single tracks.
func TestPhoneRemoteReplaysFolderCardsAsQueues(t *testing.T) {
	for _, want := range []string{
		"/api/queue/replay-card",                    // the new endpoint
		"(e.cardKey || '').indexOf('queue:') === 0", // only folder cards take it
		"url: e.cardURL",                            // a single track still replays as one
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("phone page is missing %q", want)
		}
	}
}

// The whole point of the fix is that the REST of the folder arrives, so the
// paging is where it can quietly fail again. A ContentDirectory server is allowed
// to cap RequestedCount, and a loop that advances by the page size it asked for
// and stops on the first short page would replay the first 100 of 300 tracks on
// exactly those servers: the #666 lesson, one endpoint over.
func TestBrowseQueueItemsPagesAServerThatCapsThePageSize(t *testing.T) {
	c := fakeContainer{}
	for i := 0; i < 300; i++ {
		c.tracks = append(c.tracks, fakeTrack{
			id:    "t" + strconv.Itoa(i),
			title: "Track " + strconv.Itoa(i),
			url:   "http://192.0.2.9/t" + strconv.Itoa(i) + ".mp3",
		})
	}
	f := &fakeCDS{maxPerPage: 100, children: map[string]fakeContainer{"0": c}}
	s, srv := testSearchServer(t, f)

	items, err := s.browseQueueItems(context.Background(), srv, "0")
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if len(items) != 300 {
		t.Fatalf("a capping server must not truncate the queue: got %d tracks, want 300", len(items))
	}
	if items[0].Title != "Track 0" || items[299].Title != "Track 299" {
		t.Fatalf("tracks out of server order: first %q last %q", items[0].Title, items[299].Title)
	}
}

// A server that ignores StartingIndex must end the browse instead of paging for
// ever, and what it did hand over is still a playable queue.
func TestBrowseQueueItemsStopsOnAServerThatIgnoresPaging(t *testing.T) {
	c := fakeContainer{}
	for i := 0; i < libraryBrowsePage; i++ {
		c.tracks = append(c.tracks, fakeTrack{id: "t" + strconv.Itoa(i),
			title: "Track " + strconv.Itoa(i), url: "http://192.0.2.9/t" + strconv.Itoa(i) + ".mp3"})
	}
	f := &fakeCDS{ignoreStart: true, children: map[string]fakeContainer{"0": c}}
	s, srv := testSearchServer(t, f)

	done := make(chan []queueItem, 1)
	go func() {
		items, _ := s.browseQueueItems(context.Background(), srv, "0")
		done <- items
	}()
	select {
	case items := <-done:
		if len(items) != libraryBrowsePage {
			t.Fatalf("want the one page the server keeps handing back, got %d", len(items))
		}
	case <-time.After(20 * time.Second):
		t.Fatal("browseQueueItems never finished against a server that ignores StartingIndex")
	}
}
