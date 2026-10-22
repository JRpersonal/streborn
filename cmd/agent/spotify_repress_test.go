package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/upnp"
)

// spotifyRePointRig is a handler whose renderer records every SOAP action it
// receives, so a test can see whether the Spotify wrong-state repair pushed.
type spotifyRePointRig struct {
	h       *presetWsHandler
	mu      sync.Mutex
	actions []string
	bodies  []string
}

func newSpotifyRePointRig(t *testing.T, onStream bool) *spotifyRePointRig {
	t.Helper()
	old := sourceRejectProbeDelay
	sourceRejectProbeDelay = 20 * time.Millisecond
	t.Cleanup(func() { sourceRejectProbeDelay = old })
	rig := &spotifyRePointRig{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rig.mu.Lock()
		rig.actions = append(rig.actions, r.Header.Get("SOAPACTION"))
		rig.bodies = append(rig.bodies, string(b))
		rig.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	rig.h = &presetWsHandler{
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		boxHost:        "192.0.2.9",
		renderer:       &upnp.Renderer{ControlURL: srv.URL, Client: srv.Client()},
		boxOnSpotifyFn: func() bool { return onStream },
	}
	return rig
}

func (r *spotifyRePointRig) pushed() (bool, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, a := range r.actions {
		if strings.Contains(a, "SetAVTransportURI") {
			return true, r.bodies[i]
		}
	}
	return false, ""
}

var repressPreset = presets.Preset{Slot: 4, Name: "Mix", Type: "spotify", URI: "spotify:playlist:abc"}

// Pressing the key of the Spotify playlist that already plays: the box refuses
// its own copy of the key (1036), drops to INVALID_SOURCE and shows its "not
// available" message until something puts it back on the stream. The repair
// must do that right away, on the key's own per-slot stream, instead of
// leaving it to the first verify tick five seconds later (live Portable
// 2026-10-22: seven seconds of the firmware's message, then twenty seconds of
// buffering because the next track started while nobody listened).
func TestSpotifyRePressRefusalRePointsTheBox(t *testing.T) {
	rig := newSpotifyRePointRig(t, false)
	seq := rig.h.pressSeq.Add(1)
	pressAt := time.Now().Add(-time.Second)
	rig.h.OnSourceRejected(context.Background())

	rig.h.rePointSpotifyAfterSourceReject(seq, 0, pressAt, 4, repressPreset)

	ok, body := rig.pushed()
	if !ok {
		t.Fatal("the box dropped the stream after refusing the key, and the repair did not put it back")
	}
	if !strings.Contains(body, "spotify/stream-4.ogg") {
		t.Fatalf("the repair must re-point at the key's own stream, got %q", body)
	}
}

// A box that is on the stream (playing or buffering) is left alone: a re-point
// would only restart its buffering. The verify loop owns it from there.
func TestSpotifyRePressRefusalLeavesABoxOnTheStream(t *testing.T) {
	rig := newSpotifyRePointRig(t, true)
	seq := rig.h.pressSeq.Add(1)
	pressAt := time.Now().Add(-time.Second)
	rig.h.OnSourceRejected(context.Background())

	rig.h.rePointSpotifyAfterSourceReject(seq, 0, pressAt, 4, repressPreset)

	if ok, _ := rig.pushed(); ok {
		t.Fatal("re-pointed a box that is already on the Spotify stream")
	}
}

// No refusal since the press: nothing to repair.
func TestSpotifyRePressWithoutRefusalDoesNothing(t *testing.T) {
	rig := newSpotifyRePointRig(t, false)
	rig.h.OnSourceRejected(context.Background()) // an older refusal
	time.Sleep(5 * time.Millisecond)
	seq := rig.h.pressSeq.Add(1)
	pressAt := time.Now()

	rig.h.rePointSpotifyAfterSourceReject(seq, 0, pressAt, 4, repressPreset)

	if ok, _ := rig.pushed(); ok {
		t.Fatal("re-pointed although the box did not refuse this press")
	}
}

// A newer press owns the transport: re-pointing at this key would undo it.
func TestSpotifyRePressRefusalStandsDownForANewerPress(t *testing.T) {
	rig := newSpotifyRePointRig(t, false)
	seq := rig.h.pressSeq.Add(1)
	pressAt := time.Now().Add(-time.Second)
	rig.h.OnSourceRejected(context.Background())
	rig.h.pressSeq.Add(1)

	rig.h.rePointSpotifyAfterSourceReject(seq, 0, pressAt, 4, repressPreset)

	if ok, _ := rig.pushed(); ok {
		t.Fatal("re-pointed over a newer press")
	}
}
