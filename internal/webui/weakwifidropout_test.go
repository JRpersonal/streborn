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

// withDropoutSignal stands in for the speaker's signal reading and counts how
// often a dropout asked for it.
func withDropoutSignal(t *testing.T, sig *atomic.Value, reads *atomic.Int32) {
	t.Helper()
	prev := readDropoutWifiSignal
	readDropoutWifiSignal = func(*Server, context.Context) string {
		reads.Add(1)
		v, _ := sig.Load().(string)
		return v
	}
	t.Cleanup(func() { readDropoutWifiSignal = prev })
}

func TestADropoutOnAWeakSignalSetsTheMarker(t *testing.T) {
	var sig atomic.Value
	var reads atomic.Int32
	sig.Store("POOR_SIGNAL")
	withDropoutSignal(t, &sig, &reads)
	s := quietServer("127.0.0.1")

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
	withDropoutSignal(t, &sig, &reads)
	s := quietServer("127.0.0.1")
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
	withDropoutSignal(t, &sig, &reads)
	s := quietServer("127.0.0.1")
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
	withDropoutSignal(t, &sig, &reads)
	s := quietServer("127.0.0.1")

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
	withDropoutSignal(t, &sig, &reads)
	s := quietServer("127.0.0.1")
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
