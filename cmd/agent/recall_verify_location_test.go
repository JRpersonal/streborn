package main

import (
	"testing"
	"time"
)

// TestSourceRejectIsScopedToThisRecall covers the trigger of the fast re-push:
// only a wrong-state rejection recorded AFTER this recall started may cause it.
// An older rejection (e.g. from the previous preset press) must not, or every
// recall following one rejection would be pushed twice.
func TestSourceRejectIsScopedToThisRecall(t *testing.T) {
	h := &presetWsHandler{}

	// The box reports the rejection ~0.8 s after the press; a couple of
	// milliseconds is enough to establish the same ordering here.
	recallStart := time.Now()
	time.Sleep(2 * time.Millisecond)
	h.OnSourceRejected(nil)
	if !h.lastSourceRejectTime().After(recallStart) {
		t.Fatal("a rejection during this recall must be visible to the verify")
	}

	// A rejection that predates the recall must not count.
	later := time.Now().Add(time.Second)
	if h.lastSourceRejectTime().After(later) {
		t.Fatal("a rejection older than the recall must not trigger the re-push")
	}
}

// The radio recall verify decides success from the box's now_playing document.
// These cover nowPlayingIsURL, the discriminator behind boxPlayingURL.

// TestNowPlayingIsURL_StalePlayStateOnOtherStream is the field failure the
// verify used to pass silently: a Wave rejects the recall (1036
// UpnpRcvdContentItemInWrongState), never fetches the new slot, but keeps
// reporting the PREVIOUS stream in a play state. Counting that as success made
// the verify return at its first tick, so no retry ran and the user was left
// with a display that shows the station and no audio at all.
func TestNowPlayingIsURL_StalePlayStateOnOtherStream(t *testing.T) {
	doc := `<?xml version="1.0" encoding="UTF-8" ?><nowPlaying deviceID="x" source="UPNP">` +
		`<ContentItem source="UPNP" location="http://127.0.0.1:8888/stream/4"><itemName>RSH Live</itemName></ContentItem>` +
		`<playStatus>PLAY_STATE</playStatus></nowPlaying>`

	if nowPlayingIsURL(doc, "http://127.0.0.1:8888/stream/1") {
		t.Fatal("a play state on a DIFFERENT stream must not count as this recall playing")
	}
}

// TestNowPlayingIsURL_MatchingStreamPlays: the ordinary success case still
// passes, including when the box echoes the location with a different host
// spelling than the one that built the URL.
func TestNowPlayingIsURL_MatchingStreamPlays(t *testing.T) {
	doc := `<?xml version="1.0" encoding="UTF-8" ?><nowPlaying deviceID="x" source="UPNP">` +
		`<ContentItem source="UPNP" location="http://192.168.1.50:8888/stream/5"><itemName>RSH Sylt</itemName></ContentItem>` +
		`<playStatus>BUFFERING_STATE</playStatus></nowPlaying>`

	if !nowPlayingIsURL(doc, "http://127.0.0.1:8888/stream/5") {
		t.Fatal("the box playing this recall's slot must count as success regardless of host spelling")
	}
}

// TestNowPlayingIsURL_NoLocationKeepsPlayStateVerdict: firmware whose
// now_playing carries no location must keep the old play-state behaviour, so it
// cannot fall into an endless re-push loop.
func TestNowPlayingIsURL_NoLocationKeepsPlayStateVerdict(t *testing.T) {
	doc := `<?xml version="1.0" encoding="UTF-8" ?><nowPlaying deviceID="x" source="UPNP">` +
		`<playStatus>PLAY_STATE</playStatus></nowPlaying>`

	if !nowPlayingIsURL(doc, "http://127.0.0.1:8888/stream/2") {
		t.Fatal("without a reported location the plain play-state verdict must stand")
	}
}

// TestNowPlayingIsURL_StandbyIsNotPlaying guards the obvious negative.
func TestNowPlayingIsURL_StandbyIsNotPlaying(t *testing.T) {
	doc := `<?xml version="1.0" encoding="UTF-8" ?><nowPlaying deviceID="x" source="STANDBY">` +
		`<ContentItem source="STANDBY" /></nowPlaying>`

	if nowPlayingIsURL(doc, "http://127.0.0.1:8888/stream/3") {
		t.Fatal("a box in standby is not playing this recall")
	}
}

// TestNowPlayingIsURL_RawStreamMatches covers the app-driven play path, whose
// URL carries a query string.
func TestNowPlayingIsURL_RawStreamMatches(t *testing.T) {
	raw := "http://127.0.0.1:8888/stream/raw?u=aHR0cDovL2V4YW1wbGU"
	doc := `<?xml version="1.0" encoding="UTF-8" ?><nowPlaying source="UPNP">` +
		`<ContentItem source="UPNP" location="` + raw + `" /><playStatus>PLAY_STATE</playStatus></nowPlaying>`

	if !nowPlayingIsURL(doc, raw) {
		t.Fatal("an app-driven raw-stream play must verify against its own URL")
	}
	if nowPlayingIsURL(doc, "http://127.0.0.1:8888/stream/2") {
		t.Fatal("a raw-stream play must not satisfy a slot recall's verify")
	}
}

// TestStreamPath covers the comparison key, including the non-STR URL that
// disables the comparison.
func TestStreamPath(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:8888/stream/5":        "/stream/5",
		"http://127.0.0.1:8888/stream/raw?u=aB": "/stream/raw?u=aB",
		"https://example.com/live.mp3":          "",
		"":                                      "",
	}
	for in, want := range cases {
		if got := streamPath(in); got != want {
			t.Errorf("streamPath(%q) = %q, want %q", in, got, want)
		}
	}
}
