package webui

import (
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
)

// stickID3 is a synthetic MP3 start: an ID3v2.3 tag naming artist and album,
// then a little silence.
func stickID3(artist, album string) []byte {
	frame := func(id, text string) []byte {
		body := append([]byte{0}, text...)
		out := binary.BigEndian.AppendUint32([]byte(id), uint32(len(body)))
		return append(append(out, 0, 0), body...)
	}
	body := append(frame("TPE1", artist), frame("TALB", album)...)
	n := len(body)
	out := append([]byte{'I', 'D', '3', 3, 0, 0}, byte(n>>21&0x7f), byte(n>>14&0x7f), byte(n>>7&0x7f), byte(n&0x7f))
	out = append(out, body...)
	return append(out, make([]byte, 1024)...)
}

// taggedStickServer is a play test server whose speaker holds a music stick
// with one tagged and one untagged song in "Singles".
func taggedStickServer(t *testing.T) *Server {
	t.Helper()
	f := newFakeStick(t, "spotty")
	media := t.TempDir()
	dir := filepath.Join(media, "Singles")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"01 Tagged.mp3":   stickID3("Sample Singer", "Sample Album"),
		"02 Untagged.mp3": make([]byte, 1024),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.plug(fatSector("fat32", 64))
	f.firmwareMount(media, "vfat")
	s, _ := newPlayTestServer(t)
	t.Cleanup(func() {
		s.usb.mu.Lock()
		s.usbDropLocked("test end")
		s.usb.mu.Unlock()
	})
	if _, _, ok := s.usbServed(true); !ok {
		t.Fatal("the stick must be served")
	}
	return s
}

const singlesLoop = "http://127.0.0.1:8888/usb/file/Singles/"

// Discussion #1136 (ST20, 2026-11-25): a song from the stick showed no artist
// in Now Playing. A client that names none (an older app, a replay) now gets
// the one the song's tag names, and the tag's album over the folder's.
func TestAStickSongPlayedWithoutAnArtistIsNamedByItsTag(t *testing.T) {
	s := taggedStickServer(t)
	w := postPlayBody(t, s, `{"url":"`+singlesLoop+`01%20Tagged.mp3","title":"01 Tagged","album":"Singles"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("play: %d %s", w.Code, w.Body.String())
	}
	lp, ok := s.play.snapshot()
	if !ok || lp.artist != "Sample Singer" || lp.album != "Sample Album" {
		t.Fatalf("play on record: %+v", lp)
	}

	// A song without a tag keeps what the client named.
	w = postPlayBody(t, s, `{"url":"`+singlesLoop+`02%20Untagged.mp3","title":"02 Untagged","album":"Singles"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("play: %d %s", w.Code, w.Body.String())
	}
	if lp, _ := s.play.snapshot(); lp.artist != "" || lp.album != "Singles" {
		t.Fatalf("untagged play on record: %+v", lp)
	}

	// What the client named wins over the tag.
	w = postPlayBody(t, s, `{"url":"`+singlesLoop+`01%20Tagged.mp3","title":"01 Tagged","artist":"Client Artist"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("play: %d %s", w.Code, w.Body.String())
	}
	if lp, _ := s.play.snapshot(); lp.artist != "Client Artist" {
		t.Fatalf("client artist: %+v", lp)
	}
}

// A key saved from the stick before its listing named the artist holds the
// folder as its album and no artist; it plays under the tag's.
func TestAStickKeySavedWithoutAnArtistIsNamedByTheTag(t *testing.T) {
	s := taggedStickServer(t)
	a, al, from := s.keyTrackLabel(presets.Preset{Slot: 3, StreamURL: singlesLoop + "01%20Tagged.mp3", Album: "Singles"})
	if a != "Sample Singer" || al != "Sample Album" || from != "" {
		t.Fatalf("album-only key: %q %q %q", a, al, from)
	}
	a, al, from = s.keyTrackLabel(presets.Preset{Slot: 3, StreamURL: singlesLoop + "01%20Tagged.mp3"})
	if a != "Sample Singer" || al != "Sample Album" || from == "" {
		t.Fatalf("bare key: %q %q %q", a, al, from)
	}
	// Anything not on this speaker's stick is never read.
	if a, al := s.stickTagLabel("http://192.0.2.5:9000/music/01.mp3", "X"); a != "" || al != "X" {
		t.Fatalf("NAS URL: %q %q", a, al)
	}
}

// The phone page's stick search names the artist the way its browse does.
func TestThePhoneStickSearchNamesTheTagArtist(t *testing.T) {
	s := taggedStickServer(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/library/search?q="+url.QueryEscape("tagged")+"&field=title", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	req.Host = "192.0.2.10:8888"
	s.handleLibrarySearch(rr, req)
	var out struct {
		Results []struct{ Title, Artist, Album string } `json:"results"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("search: %d %s", rr.Code, rr.Body.String())
	}
	var tagged, plain bool
	for _, r := range out.Results {
		switch r.Title {
		case "01 Tagged":
			tagged = r.Artist == "Sample Singer" && r.Album == "Sample Album"
		case "02 Untagged":
			plain = r.Artist == "" && r.Album == "Singles"
		}
	}
	if !tagged || !plain {
		t.Fatalf("results: %+v", out.Results)
	}
}
