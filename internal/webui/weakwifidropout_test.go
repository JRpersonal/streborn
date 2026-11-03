package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/wlanlive"
)

// withDropoutSignal stands in for s's signal reading and counts how often a
// dropout asked for it.
func withDropoutSignal(s *Server, sig *atomic.Value, reads *atomic.Int32) {
	s.weakWifi.mu.Lock()
	s.weakWifi.readSignal = func(context.Context) string {
		reads.Add(1)
		v, _ := sig.Load().(string)
		return v
	}
	s.weakWifi.mu.Unlock()
}

func TestADropoutOnAWeakSignalSetsTheMarker(t *testing.T) {
	var sig atomic.Value
	var reads atomic.Int32
	sig.Store("POOR_SIGNAL")
	s := quietServer("127.0.0.1")
	withDropoutSignal(s, &sig, &reads)

	t0 := time.Date(2026, 11, 3, 20, 0, 0, 0, time.UTC)
	s.noteDropoutAt("radio", t0, false)
	got, src, age, ok := s.weakWifiDropoutAt(t0.Add(5 * time.Minute))
	if !ok || got != "POOR_SIGNAL" || src != "radio" || age != 5*time.Minute {
		t.Fatalf("marker = %q %q %s %v, want POOR_SIGNAL radio 5m true", got, src, age, ok)
	}
	// Time-bound: an hour after the last dropout it is gone.
	if _, _, _, ok := s.weakWifiDropoutAt(t0.Add(weakWifiDropoutWindow + time.Second)); ok {
		t.Fatal("the marker outlived its window")
	}
}

func TestADropoutOnAGoodSignalSetsNothing(t *testing.T) {
	var sig atomic.Value
	var reads atomic.Int32
	sig.Store("GOOD_SIGNAL")
	s := quietServer("127.0.0.1")
	withDropoutSignal(s, &sig, &reads)
	s.noteDropoutAt("radio", time.Now(), false)
	if _, _, _, ok := s.WeakWifiDropout(); ok {
		t.Fatal("a dropout on a good signal blamed the Wi-Fi")
	}
}

// No reading at all (a cable, a coprocessor chassis that reports no live
// grade) is no evidence, so it never sets the marker.
func TestNoReadingIsNoEvidence(t *testing.T) {
	var sig atomic.Value
	var reads atomic.Int32
	sig.Store("")
	s := quietServer("127.0.0.1")
	withDropoutSignal(s, &sig, &reads)
	s.noteDropoutAt("spotify", time.Now(), false)
	if _, _, _, ok := s.WeakWifiDropout(); ok {
		t.Fatal("a dropout with no signal reading set the marker")
	}
}

// A station that drops every few seconds must not run a signal reading per
// drop, but each of those drops keeps the marker fresh while the last reading
// was weak, and a steady run still re-reads once the minute is up.
func TestReadingsAreRateLimitedButDropsKeepTheMarkerFresh(t *testing.T) {
	var sig atomic.Value
	var reads atomic.Int32
	sig.Store("MARGINAL_SIGNAL")
	s := quietServer("127.0.0.1")
	withDropoutSignal(s, &sig, &reads)

	t0 := time.Date(2026, 11, 3, 20, 0, 0, 0, time.UTC)
	s.noteDropoutAt("radio", t0, false)
	for i := 1; i <= 5; i++ {
		s.noteDropoutAt("radio", t0.Add(time.Duration(i)*10*time.Second), false)
	}
	if n := reads.Load(); n != 1 {
		t.Fatalf("six drops inside a minute read the signal %d times, want 1", n)
	}
	_, _, age, ok := s.weakWifiDropoutAt(t0.Add(50 * time.Second))
	if !ok || age != 0 {
		t.Fatalf("the last drop did not refresh the marker: age %s ok %v", age, ok)
	}
	// Past the minute the next drop reads again, and a recovered signal
	// clears the marker: the owner moved the speaker.
	sig.Store("EXCELLENT_SIGNAL")
	s.noteDropoutAt("radio", t0.Add(70*time.Second), false)
	if n := reads.Load(); n != 2 {
		t.Fatalf("a drop past the minute read the signal %d times in all, want 2", n)
	}
	if _, _, _, ok := s.weakWifiDropoutAt(t0.Add(71 * time.Second)); ok {
		t.Fatal("a good reading left the old weak marker standing")
	}
	// And with the last reading good, drops inside the minute do not bring it
	// back.
	s.noteDropoutAt("radio", t0.Add(80*time.Second), false)
	if _, _, _, ok := s.weakWifiDropoutAt(t0.Add(81 * time.Second)); ok {
		t.Fatal("a rate-limited drop revived the marker after a good reading")
	}
}

func TestTheVersionEnvelopeCarriesTheMarker(t *testing.T) {
	var sig atomic.Value
	var reads atomic.Int32
	sig.Store("POOR_SIGNAL")
	s := quietServer("127.0.0.1")
	withDropoutSignal(s, &sig, &reads)
	get := func() map[string]string {
		w := httptest.NewRecorder()
		s.handleAgentVersion(w, httptest.NewRequest(http.MethodGet, "/api/agent/version", nil))
		var out map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if _, ok := get()["weakWifiDropout"]; ok {
		t.Fatal("a speaker without a dropout reported one")
	}
	s.noteDropoutAt("spotify", time.Now(), false)
	out := get()
	if out["weakWifiDropout"] != "POOR_SIGNAL" || out["weakWifiDropoutSource"] != "spotify" || out["weakWifiDropoutAgeSec"] == "" {
		t.Fatalf("envelope = %v, want the marker", out)
	}
}

// The reading follows the update pre-flight's rule: only a connected
// WIFI_INTERFACE counts. A coprocessor chassis' ETHERNET-presented Wi-Fi with
// a relayed grade, or a cable, is never read as weak Wi-Fi.
func TestLiveWifiSignalTakesOnlyTheConnectedWifiInterface(t *testing.T) {
	cases := []struct {
		name   string
		ifaces []boxapi.NetworkInterface
		want   string
	}{
		{"wifi", []boxapi.NetworkInterface{{Type: "WIFI_INTERFACE", State: "NETWORK_WIFI_CONNECTED", Signal: "POOR_SIGNAL"}}, "POOR_SIGNAL"},
		{"disconnected", []boxapi.NetworkInterface{{Type: "WIFI_INTERFACE", State: "NETWORK_WIFI_DISCONNECTED", Signal: "POOR_SIGNAL"}}, ""},
		{"coprocessor", []boxapi.NetworkInterface{{Type: "ETHERNET_INTERFACE", State: "NETWORK_ETHERNET_CONNECTED", Signal: "POOR_SIGNAL"}}, ""},
		{"none", nil, ""},
	}
	for _, c := range cases {
		if got := liveWifiSignal(c.ifaces); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	for sig, weak := range map[string]bool{"MARGINAL_SIGNAL": true, "POOR_SIGNAL": true, "GOOD_SIGNAL": false, "EXCELLENT_SIGNAL": false, "NO_SIGNAL": false, "": false} {
		if weakSignalClass(sig) != weak {
			t.Errorf("weakSignalClass(%q) = %v, want %v", sig, !weak, weak)
		}
	}
}

// On a wpa chassis wpa_supplicant's live grade wins over the firmware's
// /networkInfo, which keeps the old network's reading after STR switched
// networks behind its back.
func TestTheLiveWpaGradeWinsOverTheFirmwaresStaleOne(t *testing.T) {
	ifaces := func() []boxapi.NetworkInterface {
		return []boxapi.NetworkInterface{{Type: "WIFI_INTERFACE", Name: "wlan0", State: "NETWORK_WIFI_CONNECTED", SSID: "OldNet", Signal: "GOOD_SIGNAL"}}
	}
	live := &wlanlive.Status{SSID: "TestNet", Signal: "POOR_SIGNAL", Associated: true}
	if got := dropoutSignalFrom(ifaces(), live); got != "POOR_SIGNAL" {
		t.Fatalf("with a live reading: %q, want POOR_SIGNAL", got)
	}
	if got := dropoutSignalFrom(ifaces(), nil); got != "GOOD_SIGNAL" {
		t.Fatalf("firmware only: %q, want GOOD_SIGNAL", got)
	}
	// A coprocessor chassis: the interface carries no grade, and the gabbo
	// snapshot is deliberately not filled in, so there is no reading.
	bco := []boxapi.NetworkInterface{{Type: "ETHERNET_INTERFACE", Name: "eth0", State: "NETWORK_ETHERNET_CONNECTED"}}
	if got := dropoutSignalFrom(bco, nil); got != "" {
		t.Fatalf("coprocessor chassis: %q, want no reading", got)
	}
}

// waitRePushDone waits for the re-push goroutine HandleSpotifyStreamDetach
// started to finish (its 2 s backoff, then the guards).
func waitRePushDone(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if !rePushArmed(s) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the re-push goroutine did not finish")
}

// A Spotify stream the speaker let go of while it stayed on and idle is a real
// dropout, and on a weak signal it sets the marker. App-driven Connect
// playback (no preset behind the stream) is used so the recovery itself ends
// without a recall.
func TestASpotifyDropWhileTheSpeakerStaysOnSetsTheMarker(t *testing.T) {
	t.Parallel()
	s := spotifyDropServer(t, "http://127.0.0.1:8888/spotify/stream.ogg")
	var sig atomic.Value
	var reads atomic.Int32
	sig.Store("MARGINAL_SIGNAL")
	withDropoutSignal(s, &sig, &reads)

	s.HandleSpotifyStreamDetach((40 * time.Minute).Milliseconds())
	waitRePushDone(t, s)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got, src, _, ok := s.WeakWifiDropout(); ok {
			if got != "MARGINAL_SIGNAL" || src != "spotify" {
				t.Fatalf("marker = %q %q, want MARGINAL_SIGNAL spotify", got, src)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("a real Spotify dropout on a weak signal left no marker")
}

// Everything that only LOOKS like a dropout must leave the marker alone, even
// on a weak signal: an owner who switched a MARGINAL_SIGNAL speaker off for
// the night must not be told for an hour that the music dropped out. Each case
// is the detach followed by what the owner actually did.
func TestADeliberateEndOfSpotifyIsNotADropout(t *testing.T) {
	t.Parallel()
	idle := `<nowPlaying source="INVALID_SOURCE"><playStatus>STOP_STATE</playStatus></nowPlaying>`
	cases := []struct {
		name       string
		nowPlaying string
		after      func(s *Server)
	}{
		{"power button", idle, func(s *Server) { s.noteStandbyStop() }},
		{"standby bounce", idle, func(s *Server) { s.standbyStop.noteStop() }},
		{"stop on the speaker or remote", idle, func(s *Server) { s.NoteUserStop() }},
		{"speaker in standby", idle, nil},
		{"switched to AUX", `<nowPlaying source="AUX"><playStatus>STOP_STATE</playStatus></nowPlaying>`, nil},
		{"switched to Bluetooth", `<nowPlaying source="BLUETOOTH"><playStatus>STOP_STATE</playStatus></nowPlaying>`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s, _ := resumeTestServer(t, c.nowPlaying)
			if c.name == "speaker in standby" {
				s.playStateFn = func() (bool, bool) { return true, false }
			}
			s.play.mu.Lock()
			s.play.last = &lastPlayInfo{boxURL: "http://127.0.0.1:8888/spotify/stream-4.ogg", title: "Regen", ts: time.Now()}
			s.play.mu.Unlock()
			var sig atomic.Value
			var reads atomic.Int32
			sig.Store("MARGINAL_SIGNAL")
			withDropoutSignal(s, &sig, &reads)

			s.HandleSpotifyStreamDetach((40 * time.Minute).Milliseconds())
			if c.after != nil {
				c.after(s)
			}
			waitRePushDone(t, s)
			if n := reads.Load(); n != 0 {
				t.Fatalf("a deliberate end read the signal %d times", n)
			}
			if _, _, _, ok := s.WeakWifiDropout(); ok {
				t.Fatal("a deliberate end of playback was reported as a dropout")
			}
		})
	}
}
