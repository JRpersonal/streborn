// The Spotify audio path had no drop recovery at all. Internet radio has had
// one since v0.7.5 (HandleStreamDisconnect -> maybeRePush); Spotify's Ogg sink
// detached and nothing looked at it, so a two-second Wi-Fi dropout ended
// playback for the rest of the night: field report 2026-09-09, the router
// deregistered the speaker at 01:39:07 and registered it again at 01:39:09, the
// stream was gone at 01:39:08.259 after nearly two hours of playing, and the box
// sat silent until it went to standby at 01:57.
//
// These tests pin the two halves that make that recovery safe rather than a
// recall storm: it must arm on a real drop, and it must stand down on everything
// that only LOOKS like one.

package webui

import (
	"testing"
	"time"
)

// spotifyDropServer is resumeTestServer with the last play pointing at a
// Spotify preset stream instead of a radio slot.
func spotifyDropServer(t *testing.T, boxURL string) *Server {
	t.Helper()
	s, _ := resumeTestServer(t, `<nowPlaying source="INVALID_SOURCE"><playStatus>STOP_STATE</playStatus></nowPlaying>`)
	s.lastPlayMu.Lock()
	s.lastPlay = &lastPlayInfo{boxURL: boxURL, title: "Regen", ts: time.Now()}
	s.lastPlayMu.Unlock()
	return s
}

// rePushArmed reports whether HandleSpotifyStreamDetach took ownership, i.e.
// whether a recovery goroutine is running. That flag is the observable decision;
// what the goroutine then does is maybeRePush's own, already-tested business.
func rePushArmed(s *Server) bool {
	s.lastPlayMu.Lock()
	defer s.lastPlayMu.Unlock()
	return s.rePushInFlight
}

func TestSpotifyDetachArmsTheRecovery(t *testing.T) {
	s := spotifyDropServer(t, "http://127.0.0.1:8888/spotify/stream-4.ogg")
	s.HandleSpotifyStreamDetach((2 * time.Hour).Milliseconds())
	if !rePushArmed(s) {
		t.Fatal("a two-hour Spotify stream vanished and nothing tried to bring it back")
	}
}

// The same latch the radio path uses. A stream that keeps dropping fires a
// detach every time it is re-pushed, and one goroutine per detach is the
// dozens-per-second runaway that starved :8888 in v0.7.5.
func TestSpotifyDetachDoesNotStackRecoveries(t *testing.T) {
	s := spotifyDropServer(t, "http://127.0.0.1:8888/spotify/stream-4.ogg")
	s.HandleSpotifyStreamDetach((2 * time.Hour).Milliseconds())
	s.lastPlayMu.Lock()
	armed := s.rePushInFlight
	s.lastPlayMu.Unlock()
	if !armed {
		t.Fatal("first detach did not arm the recovery")
	}
	// A second detach while the first recovery is still in flight must be a
	// no-op rather than a second goroutine. Observable as the flag staying set
	// without a second owner: if the guard were missing, both calls would pass
	// it and two goroutines would race on the same lastPlay.
	before := s.lastPlay.rePushes
	s.HandleSpotifyStreamDetach((2 * time.Hour).Milliseconds())
	s.lastPlayMu.Lock()
	after := s.lastPlay.rePushes
	s.lastPlayMu.Unlock()
	if after != before {
		t.Fatalf("a second detach started its own recovery: rePushes %d -> %d", before, after)
	}
}

// A stream STR already gave up on (maxRePushes spent, lp.failed set) must stay
// given up on. Otherwise every further detach re-arms the loop the cap exists
// to stop.
func TestSpotifyDetachRespectsAGivenUpStream(t *testing.T) {
	s := spotifyDropServer(t, "http://127.0.0.1:8888/spotify/stream-4.ogg")
	s.lastPlayMu.Lock()
	s.lastPlay.failed = true
	s.lastPlayMu.Unlock()
	s.HandleSpotifyStreamDetach((2 * time.Hour).Milliseconds())
	if rePushArmed(s) {
		t.Fatal("a stream STR had already given up on re-armed the recovery")
	}
}

// Nothing recorded to put back means nothing to do. The Spotify app owns a
// session STR never started.
func TestSpotifyDetachWithNothingRecordedDoesNothing(t *testing.T) {
	s := spotifyDropServer(t, "http://127.0.0.1:8888/spotify/stream-4.ogg")
	s.lastPlayMu.Lock()
	s.lastPlay = nil
	s.lastPlayMu.Unlock()
	s.HandleSpotifyStreamDetach((2 * time.Hour).Milliseconds())
	if rePushArmed(s) {
		t.Fatal("a detach with no recorded last play armed a recovery with nothing to push")
	}
}

// The slot parser is what decides between the clean preset recall and the bare
// re-point, and getting it wrong either wedges the box (re-pointing at a live
// Ogg mid-stream) or recalls the wrong preset.
func TestSpotifyStreamURLSlotParsing(t *testing.T) {
	cases := []struct {
		url  string
		want int
	}{
		{"http://127.0.0.1:8888/spotify/stream-4.ogg", 4},
		{"http://127.0.0.1:8888/spotify/stream-1.ogg", 1},
		{"http://127.0.0.1:8888/spotify/stream-6.ogg", 6},
		// App-driven Connect playback: a Spotify stream with no preset behind
		// it. Must NOT resolve to a slot; STR would be guessing which preset
		// the user meant.
		{"http://127.0.0.1:8888/spotify/stream.ogg", 0},
		// A radio slot is the other branch entirely.
		{"http://127.0.0.1:8888/stream/4", 0},
	}
	for _, c := range cases {
		if got := slotFromSpotifyStreamURL(c.url); got != c.want {
			t.Errorf("%s: got slot %d, want %d", c.url, got, c.want)
		}
	}
}
