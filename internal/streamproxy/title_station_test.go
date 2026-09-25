// The live title is filed under the STATION the user picked, not under the edge
// or media URL a fetch happens to land on.
//
// It used to be filed under the resolved URL: streamOne is called with
// edgeFor(url), and streamOneDepth recurses onto the media URL of a playlist
// pointer, so curTitleURL became the CDN address. The end-of-stream wipe is
// keyed on the station, so the two never matched and the title was never
// cleared. That is #274 coming back through the side door, and it covers most
// real stations: streamtheworld pins an edge host after the first response, and
// an m3u pointer such as Absolut Relax resolves one level deeper again.

package streamproxy

import (
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestTheWipeClearsATitleThatCameFromAnEdgeURL(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	const station = "https://playerservices.streamtheworld.com/api/livestream-redirect/RADIO538AAC.aac"

	// The handler keys its generation on the station, as production does.
	gen := s.noteStreamStart(station)
	// The fetch resolved to an edge host, but the title belongs to the station.
	s.clearTitleForNewURL(station)
	s.setTitle(station, "Martin Garrix - Repeat It")

	s.wipeTitleIfUnclaimed(station, gen)
	if got := s.CurrentTitle(); got != "" {
		t.Fatalf("title after the stream ended = %q, want empty", got)
	}
}

// The wiring itself, because the bug was that production passed the wrong URL
// into these two calls while every unit test passed the right one by hand.
func TestTheStreamPathFilesTheTitleUnderTheStation(t *testing.T) {
	src, err := os.ReadFile("streamproxy.go")
	if err != nil {
		t.Fatal(err)
	}
	code := string(src)

	for _, want := range []string{
		"s.clearTitleForNewURL(station)",
		"s.setTitle(station, title)",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("the stream path no longer calls %s", want)
		}
	}
	for _, unwanted := range []string{
		"s.clearTitleForNewURL(url)",
		"s.setTitle(url, title)",
	} {
		if strings.Contains(code, unwanted) {
			t.Errorf("the title is filed under the resolved URL again: %s", unwanted)
		}
	}

	// And the station really is threaded through rather than re-derived.
	sig := regexp.MustCompile(`func \(s \*Server\) streamOneDepth\(ctx context\.Context, w http\.ResponseWriter, r \*http\.Request, url, station string`)
	if !sig.MatchString(code) {
		t.Error("streamOneDepth no longer takes the station alongside the url")
	}
	if !strings.Contains(code, "s.streamOneDepth(ctx, w, r, media, station, sendHeaders, depth+1)") {
		t.Error("the playlist-pointer recursion drops the station")
	}
}
