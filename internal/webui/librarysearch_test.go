package webui

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/dlna"
)

// The phone library search is the only search path a user has on the phone, and
// #666 reported it finding "the same song sometimes". These tests pin the two
// mechanisms that made the outcome depend on where a track sits in the library
// rather than on what was typed.

// fakeCDS is a ContentDirectory the search can be pointed at without a LAN.
type fakeCDS struct {
	mu sync.Mutex
	// searchHits is what the Search action answers with. Zero items with no
	// fault is the FRITZ!Box shape that used to end the search.
	searchHits []fakeTrack
	// narrowHits, when set, is what a TITLE-ONLY Search (criteria without
	// upnp:artist) answers: the #666 QNAP shape where the widened boolean
	// criteria comes back empty while the narrow form hits.
	narrowHits []fakeTrack
	// searchFaults makes Search answer a SOAP fault, the Synology shape.
	searchFaults bool
	// children maps an object id to its children. Items are paged with the
	// StartingIndex the caller sends.
	children map[string]fakeContainer
	// ignoreStart models a server that hands back the same first page for
	// every StartingIndex.
	ignoreStart bool
	// maxPerPage models a server that silently caps RequestedCount, which many
	// ContentDirectory implementations do. Zero means it honours what is asked.
	maxPerPage int

	searches int
	browses  int
}

type fakeContainer struct {
	folders []fakeFolder
	tracks  []fakeTrack
}

type fakeFolder struct {
	id, title string
}

type fakeTrack struct {
	id, title, artist, album, url string
}

func (f *fakeCDS) counts() (searches, browses int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.searches, f.browses
}

func (f *fakeCDS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	action := r.Header.Get("SOAPACTION")
	f.mu.Lock()
	defer f.mu.Unlock()

	if strings.Contains(action, "#Search") {
		f.searches++
		if f.searchFaults {
			// UPnPError 501, what a server without SearchCaps answers.
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault><faultcode>s:Client</faultcode><detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>501</errorCode></UPnPError></detail></s:Fault></s:Body></s:Envelope>`)
			return
		}
		if f.narrowHits != nil && !strings.Contains(string(body), "upnp:artist") {
			writeSOAP(w, "Search", didl(nil, f.narrowHits), len(f.narrowHits), len(f.narrowHits))
			return
		}
		writeSOAP(w, "Search", didl(nil, f.searchHits), len(f.searchHits), len(f.searchHits))
		return
	}

	f.browses++
	id := soapValue(string(body), "ObjectID")
	start, _ := strconv.Atoi(soapValue(string(body), "StartingIndex"))
	count, _ := strconv.Atoi(soapValue(string(body), "RequestedCount"))
	if f.ignoreStart {
		start = 0
	}
	if f.maxPerPage > 0 && count > f.maxPerPage {
		count = f.maxPerPage
	}
	c := f.children[id]
	all := len(c.folders) + len(c.tracks)
	folders := pageFolders(c.folders, start, count)
	// Tracks come after the folders in the child list, so their offset is
	// whatever of the folder block the page did not consume.
	tStart := start - len(c.folders)
	if tStart < 0 {
		tStart = 0
	}
	tracks := pageTracks(c.tracks, tStart, count-len(folders))
	writeSOAP(w, "Browse", didl(folders, tracks), all, len(folders)+len(tracks))
}

func pageFolders(in []fakeFolder, start, count int) []fakeFolder {
	if start >= len(in) || count <= 0 {
		return nil
	}
	end := start + count
	if end > len(in) {
		end = len(in)
	}
	return in[start:end]
}

func pageTracks(in []fakeTrack, start, count int) []fakeTrack {
	if start >= len(in) || count <= 0 {
		return nil
	}
	end := start + count
	if end > len(in) {
		end = len(in)
	}
	return in[start:end]
}

func soapValue(body, tag string) string {
	openTag, closeTag := "<"+tag+">", "</"+tag+">"
	i := strings.Index(body, openTag)
	if i < 0 {
		return ""
	}
	rest := body[i+len(openTag):]
	j := strings.Index(rest, closeTag)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

func didl(folders []fakeFolder, tracks []fakeTrack) string {
	var b strings.Builder
	b.WriteString(`<DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/">`)
	for _, f := range folders {
		fmt.Fprintf(&b, `<container id="%s" parentID="0" childCount="1"><dc:title>%s</dc:title><upnp:class>object.container</upnp:class></container>`, f.id, f.title)
	}
	for _, t := range tracks {
		fmt.Fprintf(&b, `<item id="%s" parentID="0"><dc:title>%s</dc:title><upnp:artist>%s</upnp:artist><upnp:album>%s</upnp:album><upnp:class>object.item.audioItem.musicTrack</upnp:class><res protocolInfo="http-get:*:audio/mpeg:*">%s</res></item>`,
			t.id, t.title, t.artist, t.album, t.url)
	}
	b.WriteString(`</DIDL-Lite>`)
	return b.String()
}

func writeSOAP(w http.ResponseWriter, action, result string, total, returned int) {
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(result)
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	fmt.Fprintf(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:%sResponse xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1"><Result>%s</Result><NumberReturned>%d</NumberReturned><TotalMatches>%d</TotalMatches><UpdateID>1</UpdateID></u:%sResponse></s:Body></s:Envelope>`,
		action, esc, returned, total, action)
}

func testSearchServer(t *testing.T, f *fakeCDS) (*Server, dlna.Server) {
	t.Helper()
	ts := httptest.NewServer(f)
	t.Cleanup(ts.Close)
	return &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		dlna.Server{UDN: "uuid:test", FriendlyName: "Fake NAS", CDSControlURL: ts.URL}
}

// TestLibrarySearchFallsBackWhenSearchAnswersNothing is the FRITZ!Box shape: the
// Search action succeeds and returns zero items, which used to be taken as the
// final answer. Measured live, "Fly FRITZ" returns a hit on that server and
// "Fly FRITZ!" returns 200 with NumberReturned 0 for the same track, so the
// empty answer must not end the search.
//
// The wanted track also sits at child index 150 of a 250 track container that
// is five levels down, which is where the old walk failed twice over: it never
// paged past the first 100 children and never descended past depth 4.
func TestLibrarySearchFallsBackWhenSearchAnswersNothing(t *testing.T) {
	deep := fakeContainer{}
	for i := 0; i < 250; i++ {
		deep.tracks = append(deep.tracks, fakeTrack{
			id:    "t" + strconv.Itoa(i),
			title: "Filler " + strconv.Itoa(i),
			url:   "http://192.0.2.9/t" + strconv.Itoa(i) + ".mp3",
		})
	}
	deep.tracks[150] = fakeTrack{id: "wanted", title: "symbol", artist: "Adrianne Lenker",
		album: "abysskiss", url: "http://192.0.2.9/symbol.mp3"}

	f := &fakeCDS{
		children: map[string]fakeContainer{
			"0":  {folders: []fakeFolder{{"c1", "Music"}}},
			"c1": {folders: []fakeFolder{{"c2", "Folder"}}},
			"c2": {folders: []fakeFolder{{"c3", "Acoustic"}}},
			"c3": {folders: []fakeFolder{{"c4", "Adrianne Lenker"}}},
			"c4": deep,
		},
	}
	s, srv := testSearchServer(t, f)
	items, _ := s.searchOneServer(context.Background(), srv, "symbol")
	if len(items) != 1 || items[0].Title != "symbol" {
		t.Fatalf("the deep, late track was not found: got %d items %+v", len(items), items)
	}
}

// A widened Search that answers 200 with zero hits gets one narrow retry
// before the walk is paid: per-server criteria quirks make the boolean form
// answer empty where the title-only form hits (#666's QNAP walked three times
// for nothing on a query its server could have answered). The walk must not
// run at all when the retry hits.
func TestLibrarySearchRetriesNarrowWhenWidenedAnswersNothing(t *testing.T) {
	f := &fakeCDS{
		narrowHits: []fakeTrack{{id: "n1", title: "symbol", artist: "Adrianne Lenker",
			album: "abysskiss", url: "http://192.0.2.9/symbol.mp3"}},
		children: map[string]fakeContainer{"0": {}},
	}
	s, srv := testSearchServer(t, f)
	items, partial := s.searchOneServer(context.Background(), srv, "symbol")
	if len(items) != 1 || items[0].Title != "symbol" {
		t.Fatalf("narrow retry hit not returned: got %d items %+v", len(items), items)
	}
	if partial {
		t.Fatalf("a full narrow answer must not be reported partial")
	}
	searches, browses := f.counts()
	if searches != 2 || browses != 0 {
		t.Fatalf("want exactly 2 searches (widened + narrow) and no walk, got searches=%d browses=%d", searches, browses)
	}
}

// The walk must match artist and album too, the way the desktop Library filters
// a loaded folder, or the phone answers a different question from the app.
func TestLibrarySearchWalkMatchesArtistAndAlbum(t *testing.T) {
	f := &fakeCDS{
		children: map[string]fakeContainer{
			"0": {tracks: []fakeTrack{
				{id: "t1", title: "Wait for Me", artist: "Susan Tedeschi", album: "Hope",
					url: "http://192.0.2.9/1.mp3"},
			}},
		},
	}
	s, srv := testSearchServer(t, f)
	for _, q := range []string{"Susan", "Hope", "wait"} {
		items, _ := s.searchOneServer(context.Background(), srv, q)
		if len(items) != 1 {
			t.Errorf("query %q found %d items, want 1", q, len(items))
		}
	}
}

// A server that DOES answer Search with hits must be used as it is: the walk is
// expensive on the speaker and must not run when it is not needed.
func TestLibrarySearchUsesSearchWhenItAnswers(t *testing.T) {
	f := &fakeCDS{
		searchHits: []fakeTrack{{id: "t1", title: "symbol", artist: "Adrianne Lenker",
			album: "abysskiss", url: "http://192.0.2.9/symbol.mp3"}},
		children: map[string]fakeContainer{"0": {}},
	}
	s, srv := testSearchServer(t, f)
	items, partial := s.searchOneServer(context.Background(), srv, "symbol")
	if len(items) != 1 {
		t.Fatalf("want the Search answer, got %d items", len(items))
	}
	if partial {
		t.Error("a complete Search answer must not be reported as partial")
	}
	if _, browses := f.counts(); browses != 0 {
		t.Errorf("the tree was walked although Search answered: %d browses", browses)
	}
}

// A server whose Search action faults (Synology answers UPnPError 501) has to
// end up on the walk, with the narrow title-only retry attempted first.
func TestLibrarySearchWalksWhenSearchFaults(t *testing.T) {
	f := &fakeCDS{
		searchFaults: true,
		children: map[string]fakeContainer{
			"0": {tracks: []fakeTrack{{id: "t1", title: "cradle", url: "http://192.0.2.9/c.mp3"}}},
		},
	}
	s, srv := testSearchServer(t, f)
	items, _ := s.searchOneServer(context.Background(), srv, "cradle")
	if len(items) != 1 {
		t.Fatalf("the walk did not run for a server that cannot Search: %d items", len(items))
	}
	if searches, _ := f.counts(); searches != 2 {
		t.Errorf("want the widened Search and the title-only retry, got %d Search calls", searches)
	}
}

// TestLibrarySearchPagesAServerThatCapsThePageSize is the same defect as #666
// itself, one layer down: a container is only fully searched if the walk keeps
// asking for the next page. Many ContentDirectory servers silently cap
// RequestedCount below what the caller asked for, and a walk that stops as soon
// as a page comes back shorter than it asked for then searches the first cap
// worth of tracks and calls the rest absent. The server says how many children
// the container has, so that number, not the size of the page it chose to send,
// decides whether the walk is finished.
func TestLibrarySearchPagesAServerThatCapsThePageSize(t *testing.T) {
	c := fakeContainer{}
	for i := 0; i < 300; i++ {
		c.tracks = append(c.tracks, fakeTrack{
			id:    "t" + strconv.Itoa(i),
			title: "Filler " + strconv.Itoa(i),
			url:   "http://192.0.2.9/t" + strconv.Itoa(i) + ".mp3",
		})
	}
	c.tracks[250] = fakeTrack{id: "wanted", title: "symbol", artist: "Adrianne Lenker",
		album: "abysskiss", url: "http://192.0.2.9/symbol.mp3"}

	f := &fakeCDS{maxPerPage: 100, children: map[string]fakeContainer{"0": c}}
	s, srv := testSearchServer(t, f)
	items, _ := s.searchOneServer(context.Background(), srv, "symbol")
	if len(items) != 1 || items[0].Title != "symbol" {
		t.Fatalf("a server that caps the page size hides everything past the cap: got %d items %+v",
			len(items), items)
	}
}

// A server that ignores StartingIndex would page forever. The walk must notice
// that a page brought no child it had not already seen and stop.
func TestLibrarySearchStopsOnAServerThatIgnoresPaging(t *testing.T) {
	c := fakeContainer{}
	for i := 0; i < libraryWalkPage; i++ {
		c.tracks = append(c.tracks, fakeTrack{id: "t" + strconv.Itoa(i),
			title: "Filler " + strconv.Itoa(i), url: "http://192.0.2.9/t.mp3"})
	}
	f := &fakeCDS{ignoreStart: true, children: map[string]fakeContainer{"0": c}}
	s, srv := testSearchServer(t, f)

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.searchOneServer(context.Background(), srv, "nothing here")
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the walk never stopped on a server that ignores StartingIndex")
	}
	if _, browses := f.counts(); browses > 3 {
		t.Errorf("want the repeated page detected at once, got %d browses", browses)
	}
}

// A library big enough to exhaust the browse budget must report that the answer
// is partial, so the page can say so instead of implying the library was fully
// searched and came up empty.
func TestLibrarySearchReportsPartialOnBudget(t *testing.T) {
	children := map[string]fakeContainer{}
	root := fakeContainer{}
	for i := 0; i < libraryWalkMaxBrowses+10; i++ {
		id := "c" + strconv.Itoa(i)
		root.folders = append(root.folders, fakeFolder{id, "Folder " + strconv.Itoa(i)})
		children[id] = fakeContainer{tracks: []fakeTrack{
			{id: "t" + strconv.Itoa(i), title: "Filler", url: "http://192.0.2.9/t.mp3"},
		}}
	}
	children["0"] = root
	f := &fakeCDS{children: children}
	s, srv := testSearchServer(t, f)
	items, partial := s.searchOneServer(context.Background(), srv, "no such song")
	if len(items) != 0 {
		t.Fatalf("unexpected hits: %d", len(items))
	}
	if !partial {
		t.Error("a walk cut off by the browse budget must be reported as partial")
	}
	if _, browses := f.counts(); browses > libraryWalkMaxBrowses {
		t.Errorf("the browse budget was exceeded: %d calls", browses)
	}
}

// The same file is reachable through the Songs list, its album and the folder
// tree. A deeper walk meets all three, and the phone must not show one song
// three times.
func TestLibrarySearchDeduplicatesTheSameFile(t *testing.T) {
	track := fakeTrack{id: "a", title: "symbol", url: "http://192.0.2.9/symbol.mp3"}
	other := track
	other.id = "b"
	f := &fakeCDS{
		children: map[string]fakeContainer{
			"0":     {folders: []fakeFolder{{"songs", "Songs"}, {"album", "abysskiss"}}},
			"songs": {tracks: []fakeTrack{track}},
			"album": {tracks: []fakeTrack{other}},
		},
	}
	s, srv := testSearchServer(t, f)
	items, _ := s.searchOneServer(context.Background(), srv, "symbol")
	if len(items) != 1 {
		t.Fatalf("the same stream URL was returned %d times", len(items))
	}
}

// A non-audio item must never surface now that the walk reaches Video and
// Pictures branches.
func TestLibrarySearchSkipsNonAudio(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("SOAPACTION"), "#Search") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		result := `<DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/"><item id="v1" parentID="0"><dc:title>symbol</dc:title><upnp:class>object.item.videoItem</upnp:class><res protocolInfo="http-get:*:video/mp4:*">http://192.0.2.9/symbol.mp4</res></item></DIDL-Lite>`
		writeSOAP(w, "Browse", result, 1, 1)
	}))
	defer ts.Close()
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	items, _ := s.searchOneServer(context.Background(),
		dlna.Server{FriendlyName: "Fake", CDSControlURL: ts.URL}, "symbol")
	if len(items) != 0 {
		t.Fatalf("a video item was offered as a song: %+v", items)
	}
}
