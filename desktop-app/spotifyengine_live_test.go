package main

// The hardware verification the engine build demands of itself.
//
// go-librespot.yml says it in its own header: an engine built from a materially
// changed source has to be heard on a real speaker before it ships. The reason
// is on the record twice. An engine built from the wrong branch started,
// authenticated, and then refused every track; and a prefetch-seek regression
// played exactly one song and stopped, which no unit test noticed because both
// the build and the tests were green while the audio was wrong.
//
// So this plays a real Spotify preset on a real speaker and watches it through
// TWO track changes, which is the point where both of those failures showed.
// It also reads the account plan, which since 2026-09-26 no longer comes from
// the engine's Web API proxy but from the engine's token plus the speaker's own
// call to Spotify.
//
//	STR_LIVE_BOX=192.168.178.79 STR_LIVE_PORT=17008 STR_LIVE_SPOTIFY_SLOT=4 \
//	  go test ./... -run LiveSpotifyEngine -v -timeout 15m
//
// Audible, so: the Portable only, and volume 5.

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestLiveSpotifyEngine(t *testing.T) {
	host := os.Getenv("STR_LIVE_BOX")
	if host == "" {
		t.Skip("set STR_LIVE_BOX to run this against a speaker")
	}
	port, _ := strconv.Atoi(os.Getenv("STR_LIVE_PORT"))
	if port == 0 {
		port = 17008
	}
	slot, _ := strconv.Atoi(os.Getenv("STR_LIVE_SPOTIFY_SLOT"))
	if slot == 0 {
		slot = 4
	}
	a := NewApp()

	if err := a.SetBoxVolume(host, port, 5); err != nil {
		t.Fatalf("volume: %v", err)
	}

	before := liveSpotifyInfo(t, a, host, port)
	t.Logf("engine before: ready=%v account=%q canRecall=%v premiumRequired=%v",
		before.Ready, before.Account, before.CanRecall, before.PremiumRequired)
	if !before.Ready {
		t.Fatal("the engine is not ready on this speaker")
	}

	// The account plan travels the new way now: the agent asks the engine for a
	// token and calls Spotify itself. A speaker that cannot make that call
	// reports "unknown", which surfaces here as premiumRequired false with a
	// Premium account, so the log line is what tells the two apart.
	if before.PremiumRequired {
		t.Logf("NOTE: this speaker reports a free Spotify account, so a preset recall is expected to be refused")
	}

	if err := a.PlaySlot(host, port, slot); err != nil {
		t.Fatalf("PlaySlot %d: %v", slot, err)
	}

	// Watch. Two DIFFERENT tracks after the first is what proves the advance,
	// which is where the prefetch-seek regression bit.
	seen := []string{}
	deadline := time.Now().Add(9 * time.Minute)
	lastSkip := time.Time{}
	for time.Now().Before(deadline) && len(seen) < 3 {
		time.Sleep(6 * time.Second)
		_ = a.SetBoxVolume(host, port, 5) // re-assert, per the volume protocol
		info := liveSpotifyInfo(t, a, host, port)
		if info.Track != "" && (len(seen) == 0 || seen[len(seen)-1] != info.Track) {
			seen = append(seen, info.Track)
			t.Logf("track %d: %q (%s)", len(seen), info.Track, info.Artist)
			lastSkip = time.Time{}
		}
		// Nudge the advance rather than waiting out a whole song: a skip goes
		// through the same load path an auto-advance does.
		if len(seen) > 0 && lastSkip.IsZero() {
			time.Sleep(8 * time.Second)
			if err := a.Next(host, port); err != nil {
				t.Logf("next: %v", err)
			}
			lastSkip = time.Now()
		}
	}

	// Teardown, and it takes all three steps: a box "stop" alone left the
	// engine feeding its stream, so the speaker was still playing after the
	// test said it was done (measured 2026-09-26). Pause the engine, stop the
	// box, then prove it went idle.
	_ = a.Pause(host, port)
	_ = a.Stop(host, port)
	idle := false
	for i := 0; i < 6 && !idle; i++ {
		time.Sleep(2 * time.Second)
		if st := liveSpotifyInfo(t, a, host, port); st.Track == "" {
			idle = true
		}
		_ = a.Stop(host, port)
	}
	if !idle {
		t.Errorf("the speaker was still playing after the test finished")
	}

	if len(seen) < 3 {
		t.Fatalf("only %d distinct tracks played (%v); the engine has to survive two track changes", len(seen), seen)
	}
	t.Logf("three distinct tracks played through two changes: %v", seen)
}

type liveSpotifyState struct {
	Ready           bool   `json:"ready"`
	Account         string `json:"account"`
	Track           string `json:"track"`
	Artist          string `json:"artist"`
	Context         string `json:"context"`
	CanRecall       bool   `json:"canRecall"`
	PremiumRequired bool   `json:"premiumRequired"`
}

func liveSpotifyInfo(t *testing.T, a *App, host string, port int) liveSpotifyState {
	t.Helper()
	resp, err := a.boxDoTimeout(host, port, http.MethodGet, "/spotify/info", "", "", 8*time.Second)
	if err != nil {
		t.Fatalf("spotify/info: %v", err)
	}
	defer resp.Body.Close()
	var st liveSpotifyState
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatalf("spotify/info decode: %v", err)
	}
	return st
}
