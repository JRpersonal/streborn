package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// A single library track showed its title and then just "Playing" on the phone
// page, while the desktop app showed artist and album for the same play
// (#1839). The app only knew them because it had started the play itself: the
// speaker reports neither for a file it fetches directly, and the agent threw
// away nothing because it was never told. So the play request carries them,
// the agent keeps them with the lone file, and GET /api/queue hands them out.

const loneFileURL = "http://192.0.2.10:8200/MediaItems/42.flac"

// playLoneFile starts one library file through POST /api/play.
func playLoneFile(t *testing.T, s *Server, body string) {
	t.Helper()
	// The end watch this arms is called off by any newer recall generation.
	t.Cleanup(s.bumpRecallGen)
	w := httptest.NewRecorder()
	s.handlePlay(w, httptest.NewRequest(http.MethodPost, "/api/play", strings.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("play: status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
}

// queueAnswer is GET /api/queue as a client reads it.
func queueAnswer(t *testing.T, s *Server) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleQueue(w, httptest.NewRequest(http.MethodGet, "/api/queue", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/queue: status = %d", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("GET /api/queue: %v (%s)", err, w.Body.String())
	}
	return got
}

func TestALoneLibraryFileKeepsTheArtistAndAlbumItsStartNamed(t *testing.T) {
	s, _ := newPlayTestServer(t)
	playLoneFile(t, s, `{"url":"`+loneFileURL+`","title":"Amanda","mime":"audio/flac",`+
		`"duration_sec":256,"artist":"Boston","album":"Third Stage"}`)

	tr := activeFileTrack(t, s)
	if tr.artist != "Boston" || tr.album != "Third Stage" {
		t.Fatalf("the lone file lost its metadata: artist %q, album %q", tr.artist, tr.album)
	}
	got := queueAnswer(t, s)
	if active, _ := got["active"].(bool); active {
		t.Fatal("a single track must not report an active queue")
	}
	file, ok := got["file"].(map[string]any)
	if !ok {
		t.Fatalf("GET /api/queue names no file: %v", got)
	}
	for k, want := range map[string]string{
		"url": loneFileURL, "title": "Amanda", "artist": "Boston", "album": "Third Stage",
	} {
		if file[k] != want {
			t.Errorf("file.%s = %v, want %q", k, file[k], want)
		}
	}
}

// The label must not depend on a known length: currentDirectFile asks for one
// because it feeds a seek, and a track whose server named no duration still
// has an artist.
func TestALoneLibraryFileOfUnknownLengthStillNamesItsArtist(t *testing.T) {
	s, _ := newPlayTestServer(t)
	playLoneFile(t, s, `{"url":"`+loneFileURL+`","title":"Amanda","mime":"audio/flac","artist":"Boston"}`)

	if _, ok := s.currentDirectFile(); ok {
		t.Fatal("a file of unknown length must stay unseekable")
	}
	file, ok := queueAnswer(t, s)["file"].(map[string]any)
	if !ok || file["artist"] != "Boston" {
		t.Fatalf("file = %v, want the artist without a known length", file)
	}
	if _, has := file["album"]; has {
		t.Errorf("an album nobody named must be omitted: %v", file)
	}
}

func TestTheLoneFileGoesStaleWithANewerPlayOrAQueue(t *testing.T) {
	s, _ := newPlayTestServer(t)
	if got := queueAnswer(t, s); got["file"] != nil {
		t.Fatalf("nothing played yet, file = %v", got["file"])
	}
	playLoneFile(t, s, `{"url":"`+loneFileURL+`","title":"Amanda","mime":"audio/flac","artist":"Boston"}`)
	if _, ok := s.currentLoneFile(); !ok {
		t.Fatal("the file just started is not the current lone file")
	}

	// A queue takes over: the record is the folder's business now.
	s.queue.mu.Lock()
	s.queue.items = []queueItem{{Title: "a"}}
	s.queue.order = []int{0}
	s.queue.active = true
	s.queue.mu.Unlock()
	if _, ok := s.currentLoneFile(); ok {
		t.Error("an active queue must hide the lone file")
	}
	if got := queueAnswer(t, s); got["file"] != nil {
		t.Errorf("an active queue still names a file: %v", got["file"])
	}
	s.queue.mu.Lock()
	s.queue.active = false
	s.queue.mu.Unlock()
	if _, ok := s.currentLoneFile(); !ok {
		t.Fatal("the file is current again once no queue runs")
	}

	// Any newer play (a station, a key) moves the recall generation on.
	s.setLastPlay("http://192.0.2.1:8888/stream/1", "A Station", "", "audio/mpeg")
	if _, ok := s.currentLoneFile(); ok {
		t.Error("a newer play must make the lone file stale")
	}
	if got := queueAnswer(t, s); got["file"] != nil {
		t.Errorf("a station still carries the old file: %v", got["file"])
	}
}

// A station is no file: nothing it sends as artist is kept.
func TestARadioPlayRecordsNoLoneFile(t *testing.T) {
	s, _ := newPlayTestServer(t)
	w := httptest.NewRecorder()
	s.handlePlay(w, httptest.NewRequest(http.MethodPost, "/api/play",
		strings.NewReader(`{"url":"http://stream.example/relax","title":"Test Station","artist":"x"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if got := queueAnswer(t, s); got["file"] != nil {
		t.Errorf("a station recorded a file: %v", got["file"])
	}
}

func TestCleanTrackTextBoundsWhatAClientSends(t *testing.T) {
	if got := cleanTrackText("  Boston\r\n\t "); got != "Boston" {
		t.Errorf("trim: %q", got)
	}
	if got := cleanTrackText("AC\x00/\x1bDC"); got != "AC / DC" {
		t.Errorf("control characters: %q", got)
	}
	// Markup is text, kept as it is: the clients escape it where they print.
	if got := cleanTrackText("<b>Björk</b>"); got != "<b>Björk</b>" {
		t.Errorf("text changed: %q", got)
	}
	long := cleanTrackText(strings.Repeat("ä", 5000))
	if n := utf8.RuneCountInString(long); n != trackTextMax {
		t.Errorf("length = %d runes, want %d", n, trackTextMax)
	}
	if !utf8.ValidString(cleanTrackText("ok\xff\xfe")) {
		t.Error("invalid UTF-8 survived")
	}
}

// The phone page sends what its Library rows know and prints what the agent
// kept, from the queue read it already makes.
func TestPhoneRemoteShowsALoneFilesArtistAndAlbum(t *testing.T) {
	page := strings.ReplaceAll(indexHTML, "\r\n", "\n")
	for _, name := range []string{"async function playLibTrack(", "async function playBrowseTrack("} {
		start := strings.Index(page, name)
		if start < 0 {
			t.Fatalf("%s is gone; if it moved, move this test with it", name)
		}
		fn := page[start:]
		if end := strings.Index(fn, "\n}\n"); end > 0 {
			fn = fn[:end]
		}
		for _, want := range []string{"artist: it.artist || ''", "album: it.album || ''"} {
			if !strings.Contains(fn, want) {
				t.Errorf("%s does not send %q", name, want)
			}
		}
	}
	if !strings.Contains(page, "if (!song && playing && upnpPlay && !queue && !proxiedRadioLoc(nowLoc)) song = loneFileMeta(loneFile, nowLoc);") {
		t.Error("the now-playing line no longer names a single track's artist and album")
	}
}
