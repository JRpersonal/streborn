package webui

import (
	"os"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/dlna"
)

// Three reports from one owner, one missing field between them.
//
// #844 a single library track never stops, #817 the same track replayed from
// Recently played only buffers, #845 no progress bar for a single track. All of
// it follows from what the phone page is handed when it browses a media server:
// a MIME decides whether the speaker fetches the file itself or gets it through
// the endless-radio proxy, and a duration decides whether the speaker knows
// where the end is.
//
// Plenty of servers put no MIME in protocolInfo, and the file extension is
// right there in the URL.
func TestALibraryTrackGetsAMimeEvenWhenTheServerOmitsIt(t *testing.T) {
	cases := []struct {
		name string
		item dlna.Item
		want string
	}{
		{"the server said so", dlna.Item{MimeType: "audio/flac", StreamURL: "http://192.0.2.5:8200/x.mp3"}, "audio/flac"},
		{"no MIME, mp3", dlna.Item{StreamURL: "http://192.0.2.5:8200/Music/song.mp3"}, "audio/mpeg"},
		{"no MIME, flac", dlna.Item{StreamURL: "http://192.0.2.5:8200/Music/song.flac"}, "audio/flac"},
		{"no MIME, m4a", dlna.Item{StreamURL: "http://192.0.2.5:8200/Music/song.m4a"}, "audio/mp4"},
		// A query parameter is not a file extension: mimeFromURL cuts at "?" on
		// purpose, so this one correctly yields nothing and keeps the proxy.
		{"extension only inside a query", dlna.Item{StreamURL: "http://192.0.2.5:8200/get?f=song.mp3"}, ""},
		{"extension before a query", dlna.Item{StreamURL: "http://192.0.2.5:8200/song.mp3?x=1"}, "audio/mpeg"},
		// The one that must NOT be guessed: a bare id, which is what Windows
		// Media Player serves. Guessing a codec there would have the speaker
		// decode the wrong thing, which is worse than the proxy.
		{"bare id, nothing to go on", dlna.Item{StreamURL: "http://192.0.2.5:10243/WMPNSSv4/1871282578/1_abcdef"}, ""},
		{"no URL at all", dlna.Item{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := trackMime(c.item); got != c.want {
				t.Errorf("trackMime = %q, want %q", got, c.want)
			}
		})
	}
}

// The wiring, because the helper existed in this package all along and the two
// places that build a track record simply never called it.
func TestBothLibraryListingsUseTheFallback(t *testing.T) {
	for _, f := range []string{"librarybrowse.go", "librarysearch.go"} {
		src := readRepoFile(t, f)
		if !strings.Contains(src, "Mime: trackMime(it)") {
			t.Errorf("%s no longer passes the track MIME through the fallback; if it moved, move this test with it", f)
		}
		if strings.Contains(src, "Mime: it.MimeType") {
			t.Errorf("%s still hands the server's raw MIME straight out, so a server that sends none is back to the proxy", f)
		}
	}
}

// A length is only meaningful when the speaker fetches the file itself. Through
// the proxy the byte count is the proxy's, and claiming the stream is seekable
// over it would be a lie the firmware acts on.
func TestTheLengthIsOnlySentForADirectPlay(t *testing.T) {
	src := readRepoFile(t, "playback.go")
	i := strings.Index(src, "var meta upnp.TrackMeta")
	if i < 0 {
		t.Fatal("the track meta is gone from the play path; if it moved, move this test with it")
	}
	window := src[i:min(i+400, len(src))]
	if !strings.Contains(window, "playDirect") {
		t.Error("the length is sent without checking that the speaker fetches the file itself")
	}
	if !strings.Contains(window, "req.DurationSec > 0") {
		t.Error("a zero length is not guarded, so a track of unknown length claims to be seekable")
	}
}

// The phone page has to send what it already knows: durationSec is in the
// browse and the search JSON and was being dropped on the floor.
func TestThePhonePageSendsTheLengthItAlreadyHas(t *testing.T) {
	page := readIndexHTML(t)
	// Three call sites: a search hit, a track inside a folder listing, and the
	// folder queue, which has sent it since the queue shipped and is the reason
	// a folder always drew a bar while a single track never did.
	if n := strings.Count(page, "duration_sec: it.durationSec || 0"); n != 3 {
		t.Errorf("%d of the 3 play calls send the length, want 3", n)
	}
}

// readRepoFile reads a source file of this package, so a test can pin the
// WIRING and not only the helper. The helpers in this repo have a habit of
// existing correctly and never being called.
func readRepoFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
