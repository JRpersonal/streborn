package webui

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// #1074, two SoundTouch 10s on v1.0.10: a stereo pair created from two
// speakers in standby correctly started no audio, but the master stayed on
// with no source selected, solid amber, and never went back to sleep.

// withPairStandbySend records the standby calls instead of reaching :8090.
func withPairStandbySend(t *testing.T, err error) *[]string {
	t.Helper()
	prev := pairStandbySend
	t.Cleanup(func() { pairStandbySend = prev })
	var calls []string
	pairStandbySend = func(_ context.Context, host string) error {
		calls = append(calls, host)
		return err
	}
	return &calls
}

func TestPairStandbyDecision(t *testing.T) {
	cases := []struct {
		name       string
		np         nowPlayingSnapshot
		userPlayed bool
		want       bool
	}{
		{"woken, no source selected (the #1074 amber state)", nowPlayingSnapshot{Source: "INVALID_SOURCE"}, false, true},
		{"woken, self-resumed station stopped again", nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", PlayStatus: "STOP_STATE"}, false, true},
		{"the user started something through STR", nowPlayingSnapshot{Source: "INVALID_SOURCE"}, true, false},
		{"playing", nowPlayingSnapshot{Source: "UPNP", PlayStatus: "PLAY_STATE"}, false, false},
		{"buffering", nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", PlayStatus: "BUFFERING_STATE"}, false, false},
		{"paused", nowPlayingSnapshot{Source: "STORED_MUSIC", PlayStatus: "PAUSE_STATE"}, false, false},
		{"Bluetooth selected on the speaker", nowPlayingSnapshot{Source: "BLUETOOTH", PlayStatus: "STOP_STATE"}, false, false},
		{"AUX selected", nowPlayingSnapshot{Source: "AUX"}, false, false},
		{"already in standby", nowPlayingSnapshot{Source: "STANDBY"}, false, false},
		{"unreadable", nowPlayingSnapshot{}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, why := pairStandbyDecision(tc.np, tc.userPlayed); got != tc.want {
				t.Fatalf("pairStandbyDecision = %v (%s), want %v", got, why, tc.want)
			}
		})
	}
}

// The #1074 sequence: the pairing woke the master quietly, the pair formed
// silent, and the master is idle with no source. It goes back to standby, and
// the self-wake guard stays armed so the next power-on resumes nothing.
func TestSilentPairFromStandbyGoesBackToStandby(t *testing.T) {
	s, stops := selfResumeTestServer(t, nowPlayingSnapshot{Source: "INVALID_SOURCE", PlayStatus: "STOP_STATE"})
	calls := withPairStandbySend(t, nil)
	start := time.Now()
	s.noteQuietWakeEpisode()

	if !s.returnSilentPairToStandby(context.Background(), start) {
		t.Fatal("the idle master was left on after pairing it out of standby")
	}
	if len(*calls) != 1 || (*calls)[0] != s.boxHost {
		t.Fatalf("standby calls = %v, want exactly one to the master", *calls)
	}
	if *stops != 0 {
		t.Fatalf("stops = %d, an idle master needs no STOP", *stops)
	}
	if !s.wokenForGroup() {
		t.Error("the self-wake guard was disarmed; the next power-on could auto-resume")
	}
	if !s.userStoppedRecently() {
		t.Error("the standby was not noted as deliberate, so its source drop reads as a spontaneous one (#419)")
	}
}

// The user started a station through STR while the pair was forming: the pair
// stays on, whatever the speaker reports at that instant.
func TestPairStaysOnWhenTheUserPlayedSinceTheWake(t *testing.T) {
	s, _ := selfResumeTestServer(t, nowPlayingSnapshot{Source: "INVALID_SOURCE"})
	calls := withPairStandbySend(t, nil)
	start := time.Now()
	s.noteQuietWakeEpisode()
	time.Sleep(5 * time.Millisecond)
	s.setLastPlay("http://127.0.0.1:8888/stream/1", "Station", "", "")

	if s.returnSilentPairToStandby(context.Background(), start) || len(*calls) != 0 {
		t.Fatalf("standby sent over a play the user started, calls=%v", *calls)
	}
}

// Something started on the pair by other means (the phone's Spotify app, a
// hardware preset): the transport reads playing right before, so no standby.
func TestPairStaysOnWhenSomethingPlays(t *testing.T) {
	s, _ := selfResumeTestServer(t, nowPlayingSnapshot{Source: "UPNP", PlayStatus: "PLAY_STATE"})
	calls := withPairStandbySend(t, nil)
	if s.returnSilentPairToStandby(context.Background(), time.Now()) || len(*calls) != 0 {
		t.Fatalf("standby sent into playing audio, calls=%v", *calls)
	}
}

// A failing standby call is reported, not retried.
func TestPairStandbyFailureIsReported(t *testing.T) {
	s, _ := selfResumeTestServer(t, nowPlayingSnapshot{Source: "INVALID_SOURCE"})
	calls := withPairStandbySend(t, errors.New("box unreachable"))
	if s.returnSilentPairToStandby(context.Background(), time.Now()) {
		t.Fatal("a failed standby was reported as sent")
	}
	if len(*calls) != 1 {
		t.Fatalf("standby calls = %v, want exactly one attempt", *calls)
	}
}

// formStereoPair has to remember whether STR woke each half out of standby and
// hand that to afterSilentPair on the confirmed, silent path only. The group
// form path must NOT put its master back to standby: a native zone dissolves
// when its master drops to STANDBY.
func TestOnlyTheSilentPairFromStandbyIsPutBackToSleep(t *testing.T) {
	src, err := os.ReadFile("zones_stereo.go")
	if err != nil {
		t.Fatal(err)
	}
	pair := funcBody(t, string(src), "formStereoPair")
	if pair == "" {
		t.Fatal("formStereoPair not found")
	}
	for _, want := range []string{
		"masterWasAsleep = true",
		"partnerWasAsleep = true",
		"s.afterSilentPair(masterWasAsleep && partnerWasAsleep, pairStart)",
	} {
		if !strings.Contains(pair, want) {
			t.Errorf("formStereoPair lost %q", want)
		}
	}
	verify := strings.Index(pair, "verifyStereoPair(")
	after := strings.Index(pair, "s.afterSilentPair(")
	if verify < 0 || after < verify {
		t.Error("the pair may only go back to standby after it was verified on both speakers")
	}
	zone := funcBody(t, string(src), "handleZoneForm")
	if strings.Contains(zone, "s.afterSilentPair(") || strings.Contains(zone, "s.returnSilentPairToStandby(") {
		t.Error("handleZoneForm puts its master to standby, which dissolves the group it just formed")
	}
}
