package webui

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxurl"
	"github.com/JRpersonal/streborn/internal/upnp"
)

// tracePressAsync runs one traced press with a short wait and reports whether
// it skipped by itself.
func tracePressAsync(st *traceSkipState, at time.Time) <-chan bool {
	out := make(chan bool, 1)
	go func() { out <- st.tracePress(at, 150*time.Millisecond) }()
	return out
}

// The live double press of Back on the ST30 (2026-11-21): the first press
// brought a frame and a trace line, the second only a trace line, because the
// speaker was tearing its stream down. The first press must not skip twice,
// the second must skip once, whatever order the first press's reports come in.
func TestTraceSkipTakesTheSecondPressOfADoublePress(t *testing.T) {
	t0 := time.Now()
	for name, frameFirst := range map[string]bool{"frame first": true, "trace first": false} {
		var st traceSkipState
		var first <-chan bool
		if frameFirst {
			st.noteFrame(t0)
			first = tracePressAsync(&st, t0.Add(300*time.Millisecond))
		} else {
			first = tracePressAsync(&st, t0)
			time.Sleep(20 * time.Millisecond)
			st.noteFrame(t0.Add(60 * time.Millisecond))
		}
		if <-first {
			t.Errorf("%s: the first press skipped from the trace as well as from its frame", name)
		}
		if !<-tracePressAsync(&st, t0.Add(800*time.Millisecond)) {
			t.Errorf("%s: the second press, which had no frame, did not skip", name)
		}
	}
}

// A frame from long before is not this press's frame.
func TestTraceSkipIgnoresAnOldFrame(t *testing.T) {
	var st traceSkipState
	t0 := time.Now()
	st.noteFrame(t0)
	if !<-tracePressAsync(&st, t0.Add(3*time.Second)) {
		t.Fatal("a press three seconds after an unpaired frame was taken for that frame's press")
	}
}

type traceSkipRig struct {
	s     *Server
	mu    sync.Mutex
	skips []bool
	steps []int
}

func (r *traceSkipRig) skipCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.skips)
}

func newTraceSkipRig(t *testing.T, lastPlay, source, location string, follower bool) *traceSkipRig {
	t.Helper()
	r := &traceSkipRig{}
	s, _ := newRadioStepServer(t, 0, source, location)
	s.spotifySkip = func(_ context.Context, forward bool) error {
		r.mu.Lock()
		r.skips = append(r.skips, forward)
		r.mu.Unlock()
		return nil
	}
	s.radioStepPlayFn = func(_ context.Context, slot int) error {
		r.mu.Lock()
		r.steps = append(r.steps, slot)
		r.mu.Unlock()
		return nil
	}
	s.traceSkipWait = 100 * time.Millisecond
	s.groupFollowerFn = func() (bool, string) { return follower, "" }
	if lastPlay != "" {
		s.play.record(&lastPlayInfo{boxURL: lastPlay})
	}
	r.s = s
	return r
}

func waitSkips(t *testing.T, r *traceSkipRig, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && r.skipCount() < want {
		time.Sleep(10 * time.Millisecond)
	}
	// Give a wrong extra skip the chance to show up.
	time.Sleep(150 * time.Millisecond)
	if got := r.skipCount(); got != want {
		t.Fatalf("engine skipped %d times, want %d", got, want)
	}
}

// A traced Back on a Spotify key with no frame of its own skips the engine.
func TestRemoteTrackKeySkipsSpotifyWhenTheFrameMissedThePress(t *testing.T) {
	r := newTraceSkipRig(t, boxurl.SpotifySlot(2), "INVALID_SOURCE", "", false)
	r.s.RemoteTrackKey(false)
	waitSkips(t, r, 1)
}

// The frame and the trace line of one press skip once.
func TestRemoteTrackKeyAndFrameOfOnePressSkipOnce(t *testing.T) {
	r := newTraceSkipRig(t, boxurl.SpotifySlot(2), "UPNP", boxurl.SpotifySlot(2), false)
	if src, err := r.s.HardwareSkip(context.Background(), false); err != nil || src != "spotify-skip" {
		t.Fatalf("HardwareSkip = %q, %v", src, err)
	}
	r.s.RemoteTrackKey(false)
	waitSkips(t, r, 1)
}

// A press on a source the speaker plays itself is not STR's to skip.
func TestRemoteTrackKeyLeavesOtherSourcesAlone(t *testing.T) {
	for name, c := range map[string]struct{ lastPlay, source, location string }{
		"bluetooth":       {boxurl.SpotifySlot(2), "BLUETOOTH", ""},
		"another stream":  {boxurl.SpotifySlot(2), "UPNP", "http://other.example/x.mp3"},
		"nothing of STRs": {"", "INVALID_SOURCE", ""},
	} {
		r := newTraceSkipRig(t, c.lastPlay, c.source, c.location, false)
		r.s.RemoteTrackKey(true)
		if got := r.skipCount(); got != 0 {
			t.Errorf("%s: skipped %d times", name, got)
		}
	}
}

// The kitchen ST10 following the ST30 read the ST30's Back as a radio step
// from the station it played before it joined, and its recall put the whole
// group on that station. A follower leaves Next and Previous to its main
// speaker, from either signal.
func TestGroupFollowerLeavesRemoteTrackKeysToItsMainSpeaker(t *testing.T) {
	r := newTraceSkipRig(t, boxurl.StreamSlot(1), "INVALID_SOURCE", "", true)
	r.s.RemoteTrackKey(false)
	if src, _ := r.s.HardwareSkip(context.Background(), false); src != "group-follower" {
		t.Errorf("HardwareSkip on a follower answered %q", src)
	}
	time.Sleep(200 * time.Millisecond)
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.steps) != 0 || len(r.skips) != 0 {
		t.Fatalf("a follower acted on the remote: steps %v, skips %v", r.steps, r.skips)
	}
	// The same speaker standing alone steps as before.
	alone := newTraceSkipRig(t, boxurl.StreamSlot(1), "INVALID_SOURCE", "", false)
	alone.s.RemoteTrackKey(false)
	alone.mu.Lock()
	defer alone.mu.Unlock()
	if len(alone.steps) != 1 {
		t.Fatalf("a speaker on its own did not step: %v", alone.steps)
	}
}

// The check that a skip left the speaker playing stands down once a newer
// press came in: on the ST30 the check of a Next fired one second into the
// Back that followed it, read the Back's teardown as "not playing", and its
// recall moved the playlist on (2026-11-21).
func TestSkipRecoveryStandsDownForANewerSkip(t *testing.T) {
	var buf syncBuffer
	s := &Server{
		logger:           slog.New(slog.NewTextHandler(&buf, nil)),
		renderer:         &upnp.Renderer{},
		boxNowReadFn:     scriptNowPlaying("INVALID_SOURCE", "", ""),
		skipRecoverWaits: [2]time.Duration{50 * time.Millisecond, 50 * time.Millisecond},
	}
	s.play.record(&lastPlayInfo{boxURL: boxurl.SpotifySlot(2)})
	s.spotifyRecoverAfterSkip()
	s.spotifySkipSeq.Add(1) // the newer press
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(buf.String(), "a newer skip came in") {
		time.Sleep(10 * time.Millisecond)
	}
	if got := buf.String(); !strings.Contains(got, "a newer skip came in") || strings.Contains(got, "clean slot recall") {
		t.Fatalf("the recovery did not stand down for the newer skip:\n%s", got)
	}
}
