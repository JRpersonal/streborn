package webui

import (
	"testing"
	"time"
)

// Christopher Stark, 2026-09-15, SoundTouch 20 + SoundTouch 10: his Spotify
// playlist was running on the main speaker, he added the second speaker, and
// both jumped to the station he had last played through STR. The box was busy,
// so the old code re-pushed lastPlay without ever asking what the box was busy
// WITH.

func TestALiveSpotifySessionIsCarriedIntoTheGroupNotReplaced(t *testing.T) {
	ref := &lastPlayInfo{boxURL: "http://127.0.0.1:8888/stream/3", title: "R.SA", ts: time.Now().Add(-72 * time.Hour)}
	np := nowPlayingSnapshot{
		Source:     "UPNP",
		PlayStatus: "PLAY_STATE",
		ItemName:   "Discover Weekly",
		Location:   "http://127.0.0.1:8888/spotify/stream.ogg",
	}

	got, blocked, _ := masterResumeForZone(np, ref)
	if blocked {
		t.Fatal("STR's own Ogg proxy is pushable, blocking it leaves the group silent")
	}
	if got == nil || got.boxURL != np.Location {
		t.Fatalf("restart = %q, want the stream that is actually playing (%q)", lastPlayURL(got), np.Location)
	}
	if got.boxURL == ref.boxURL {
		t.Error("the three-day-old station was pushed over a live session, which is the reported bug")
	}
	if got.mime != "audio/ogg" {
		t.Errorf("mime = %q, want audio/ogg so the box does not decode the Ogg stream as MP3", got.mime)
	}
	if got.title != "Discover Weekly" {
		t.Errorf("title = %q, want the box's own item name", got.title)
	}
}

// The master playing exactly what STR put there is the case the re-push was
// written for, and it has to keep working: /setZone tears that session down.
func TestTheMastersOwnStreamIsStillRestarted(t *testing.T) {
	ref := &lastPlayInfo{boxURL: "http://127.0.0.1:8888/stream/3", title: "R.SA", mime: "audio/mpeg", ts: time.Now()}
	np := nowPlayingSnapshot{Source: "UPNP", PlayStatus: "PLAY_STATE", Location: ref.boxURL}

	got, blocked, _ := masterResumeForZone(np, ref)
	if blocked || got != ref {
		t.Fatalf("restart = %v (blocked=%v), want the captured entry itself", got, blocked)
	}
}

// Bluetooth, AUX and the box's own Spotify carry no URL this box could push.
// Substituting the recorded station there is how a live session got replaced,
// so the answer is to push nothing AND to stop the member fallback from
// putting some other speaker's station on the group instead.
func TestASourceWithNothingToPushBlocksTheSubstitute(t *testing.T) {
	ref := &lastPlayInfo{boxURL: "http://127.0.0.1:8888/stream/3", title: "R.SA", ts: time.Now()}
	for _, src := range []string{"BLUETOOTH", "AUX", "SPOTIFY"} {
		got, blocked, why := masterResumeForZone(nowPlayingSnapshot{Source: src, PlayStatus: "PLAY_STATE"}, ref)
		if got != nil {
			t.Errorf("%s: restart = %q, want nothing", src, lastPlayURL(got))
		}
		if !blocked {
			t.Errorf("%s: not blocked, so a member's station can still be pushed over it", src)
		}
		if why == "" {
			t.Errorf("%s: no reason logged, a bundle cannot say why the group came up silent", src)
		}
	}
}

// A native radio selection hides the stream URL inside the location. It is the
// master's own, so unlike the member and partner captures it must NOT be
// rewritten to a LAN address.
func TestANativeStationOnTheMasterStaysOnLoopback(t *testing.T) {
	loc := OrionStationLocation("http://127.0.0.1:8888/stream/6", "Exclusively Rush", "")
	np := nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", PlayStatus: "PLAY_STATE", Location: loc}

	got, blocked, _ := masterResumeForZone(np, nil)
	if blocked || got == nil {
		t.Fatalf("restart = %v (blocked=%v), want the station unpacked from the location", got, blocked)
	}
	if want := "http://127.0.0.1:8888/stream/6"; got.boxURL != want {
		t.Errorf("boxURL = %q, want %q", got.boxURL, want)
	}
	if got.title != "Exclusively Rush" {
		t.Errorf("title = %q, want the station name", got.title)
	}
}

// :8090 not answering must not cost the user their music: the caller only gets
// here because the box reported itself busy.
func TestAnUnreadableBoxFallsBackToTheRecordedStream(t *testing.T) {
	ref := &lastPlayInfo{boxURL: "http://127.0.0.1:8888/stream/3", ts: time.Now()}
	got, blocked, _ := masterResumeForZone(nowPlayingSnapshot{}, ref)
	if blocked || got != ref {
		t.Fatalf("restart = %v (blocked=%v), want the captured entry", got, blocked)
	}
}
