package spotify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePlayModeEngine answers /status with a position and the play modes, and
// records every POST with its body.
type fakePlayModeEngine struct {
	mu    sync.Mutex
	posMs int64
	noPos bool
	modes playModes
	posts []string
}

func (f *fakePlayModeEngine) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodGet && r.URL.Path == "/status" {
		st := map[string]any{
			"shuffle_context": f.modes.Shuffle,
			"repeat_context":  f.modes.RepeatContext,
			"repeat_track":    f.modes.RepeatTrack,
		}
		if !f.noPos {
			st["track"] = map[string]any{"uri": "spotify:track:x", "position": f.posMs}
		}
		_ = json.NewEncoder(w).Encode(st)
		return
	}
	b, _ := io.ReadAll(r.Body)
	f.posts = append(f.posts, r.URL.Path+" "+strings.TrimSpace(string(b)))
	var v map[string]bool
	_ = json.Unmarshal(b, &v)
	switch r.URL.Path {
	case "/player/shuffle_context":
		f.modes.Shuffle = v["shuffle_context"]
	case "/player/repeat_context":
		f.modes.RepeatContext = v["repeat_context"]
	case "/player/repeat_track":
		f.modes.RepeatTrack = v["repeat_track"]
	}
}

func (f *fakePlayModeEngine) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.posts...)
}

func newFakePlayModeManager(t *testing.T, f *fakePlayModeEngine) *Manager {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(ts.Close)
	m := newTestManagerAt(t, ts.URL)
	m.client = ts.Client()
	return m
}

func TestHeardPrevGoesBackOnWhatTheRoomHears(t *testing.T) {
	for _, c := range []struct {
		pos, lag time.Duration
		back     bool
	}{
		// The engine is ten seconds of lead ahead: 15 s produced is 5 s heard.
		{15 * time.Second, 10 * time.Second, true},
		{2 * time.Second, 10 * time.Second, true},
		{19900 * time.Millisecond, 10 * time.Second, true},
		{20 * time.Second, 10 * time.Second, false},
		{45 * time.Second, 10 * time.Second, false},
		// A box that plays nearly live: the window is the song itself.
		{9 * time.Second, 0, true},
		{11 * time.Second, 0, false},
	} {
		if got := heardPrevGoesBack(c.pos, c.lag); got != c.back {
			t.Errorf("pos %v lag %v: back = %v, want %v", c.pos, c.lag, got, c.back)
		}
	}
}

// attachedLagManager is a manager whose box attached `ago` and has been handed
// `delivered` of audio since, with the attach zero check as the newest reading,
// exactly as the drain leaves it.
func attachedLagManager(t *testing.T, ago, delivered time.Duration) *Manager {
	t.Helper()
	m := newTestManagerAt(t, "http://127.0.0.1:1")
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sink = io.Discard
	m.lagBaseGran = 500 * vorbisRate
	m.deliveredGran = m.lagBaseGran + int64(delivered.Seconds()*vorbisRate)
	m.lagBaseAt = time.Now().Add(-ago)
	m.lagSeq, m.lagBaseSeq = 4, 3
	m.lastLag = boxLagMeasurement{At: m.lagBaseAt, Reason: boxLagReasonAttach}
	m.lastLagSeq = 4
	return m
}

func TestHeardLagFallsBackToTheLeadCap(t *testing.T) {
	m := newTestManagerAt(t, "http://127.0.0.1:1")
	if lag, src := m.heardLag(); src != "lead-cap" || lag != heardPrevDefaultLag {
		t.Fatalf("nothing attached: lag %v from %q, want the default lead cap", lag, src)
	}
	// The drain's configured lead (NAND override) is the better fallback.
	m.mu.Lock()
	m.leadCap = 6 * time.Second
	m.mu.Unlock()
	if lag, src := m.heardLag(); src != "lead-cap" || lag != 6*time.Second {
		t.Fatalf("lead cap 6 s: lag %v from %q", lag, src)
	}
}

// The live Portable case (#1797): the only reading was the attach zero check,
// and HeardPrev took its 0 as the lag. Box re-attached 15.2 s before the press
// and has been handed 23 s since: about 7.8 s are still in flight.
func TestHeardLagIgnoresTheAttachZeroCheckAndUsesTheWallClock(t *testing.T) {
	m := attachedLagManager(t, 15200*time.Millisecond, 23*time.Second)
	lag, src := m.heardLag()
	if src != "wall-clock" || lag < 7600*time.Millisecond || lag > 8*time.Second {
		t.Fatalf("lag %v from %q, want about 7.8 s from the wall clock", lag, src)
	}
	// Starved box (delivered less than wall time): no negative lag.
	m = attachedLagManager(t, 20*time.Second, 5*time.Second)
	if lag, src := m.heardLag(); src != "wall-clock" || lag != 0 {
		t.Fatalf("starved attachment: lag %v from %q, want 0", lag, src)
	}
	// An unbelievable estimate falls back to the lead cap.
	m = attachedLagManager(t, time.Second, 200*time.Second)
	if lag, src := m.heardLag(); src != "lead-cap" || lag != heardPrevDefaultLag {
		t.Fatalf("200 s in flight: lag %v from %q, want the lead cap", lag, src)
	}
}

func TestHeardLagPrefersAUsableBoxReadingOfThisAttachment(t *testing.T) {
	m := attachedLagManager(t, 20*time.Second, 28*time.Second)
	settled := boxLagMeasurement{At: time.Now(), Reason: boxLagReasonSettled, DeliveredSec: 25, BoxRelSec: 17.5, DiffSec: 7.5}
	m.mu.Lock()
	m.lastLag, m.lastLagSeq, m.lagSeq = settled, 5, 5
	m.mu.Unlock()
	if lag, src := m.heardLag(); src != "box" || lag != 7500*time.Millisecond {
		t.Fatalf("settled reading 7.5 s: lag %v from %q", lag, src)
	}
	// The same reading sampled before this attachment's baseline is not its lag.
	m.mu.Lock()
	m.lagBaseSeq = 5
	m.mu.Unlock()
	if _, src := m.heardLag(); src != "wall-clock" {
		t.Fatalf("a reading from an earlier attachment was used (%q)", src)
	}
	// A reading at a skip's boundary is mid-jump, never the lag.
	m.mu.Lock()
	m.lagBaseSeq = 3
	m.lastLag.Reason = boxLagReasonAfterSkip
	m.mu.Unlock()
	if _, src := m.heardLag(); src != "wall-clock" {
		t.Fatalf("a reading at a skip boundary was used (%q)", src)
	}
}

func TestLagUsable(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		name string
		l    boxLagMeasurement
		want bool
	}{
		{"none", boxLagMeasurement{}, false},
		{"attach zero check", boxLagMeasurement{At: now, Reason: boxLagReasonAttach}, false},
		{"nothing delivered", boxLagMeasurement{At: now, Reason: "track-boundary", BoxRelSec: 5, DiffSec: 0}, false},
		{"box played nothing", boxLagMeasurement{At: now, Reason: "track-boundary", DeliveredSec: 9, DiffSec: 9}, false},
		{"mid-jump at a skip", boxLagMeasurement{At: now, Reason: boxLagReasonAfterSkip, DeliveredSec: 30, BoxRelSec: 25, DiffSec: 5}, false},
		{"negative", boxLagMeasurement{At: now, Reason: "track-boundary", DeliveredSec: 20, BoxRelSec: 25, DiffSec: -5}, false},
		{"track boundary", boxLagMeasurement{At: now, Reason: "track-boundary", DeliveredSec: 200, BoxRelSec: 191.5, DiffSec: 8.5}, true},
		{"settled", boxLagMeasurement{At: now, Reason: boxLagReasonSettled, DeliveredSec: 25, BoxRelSec: 17, DiffSec: 8}, true},
	} {
		if got := lagUsable(c.l); got != c.want {
			t.Errorf("%s: lagUsable = %v, want %v", c.name, got, c.want)
		}
	}
}

// End to end on the live numbers: 5 s after a re-attach the engine is 13 s
// into the song. Taking the attach reading's 0 as the lag read that as 13 s
// heard and restarted; the room has heard about 5 s, so Back goes to the song
// before.
func TestHeardPrevAfterAReattachGoesBackOnTheWallClockLag(t *testing.T) {
	f := &fakePlayModeEngine{posMs: 13000}
	m := newFakePlayModeManager(t, f)
	m.mu.Lock()
	m.sink = io.Discard
	m.lagBaseGran = 0
	m.deliveredGran = 13 * vorbisRate
	m.lagBaseAt = time.Now().Add(-5 * time.Second)
	m.lastLag = boxLagMeasurement{At: m.lagBaseAt, Reason: boxLagReasonAttach}
	m.lagSeq, m.lastLagSeq, m.lagBaseSeq = 1, 1, 0
	m.mu.Unlock()
	back, err := m.HeardPrev(context.Background())
	if err != nil || !back {
		t.Fatalf("back=%v err=%v, want the song before", back, err)
	}
}

func TestHeardPrevEarlyGoesToTheSongBefore(t *testing.T) {
	f := &fakePlayModeEngine{posMs: 14000} // 4 s heard behind the 10 s lead
	m := newFakePlayModeManager(t, f)
	back, err := m.HeardPrev(context.Background())
	if err != nil || !back {
		t.Fatalf("back=%v err=%v, want the song before", back, err)
	}
	got := f.calls()
	if len(got) != 2 || got[0] != `/player/seek {"position":0}` || !strings.HasPrefix(got[1], "/player/prev") {
		t.Fatalf("engine calls = %q, want a seek to 0 then prev", got)
	}
}

func TestHeardPrevLateRestartsTheSong(t *testing.T) {
	f := &fakePlayModeEngine{posMs: 95000}
	m := newFakePlayModeManager(t, f)
	back, err := m.HeardPrev(context.Background())
	if err != nil || back {
		t.Fatalf("back=%v err=%v, want a restart", back, err)
	}
	if got := f.calls(); len(got) != 1 || !strings.HasPrefix(got[0], "/player/prev") {
		t.Fatalf("engine calls = %q, want one plain prev", got)
	}
}

func TestHeardPrevWithoutPositionLeavesItToTheEngine(t *testing.T) {
	f := &fakePlayModeEngine{noPos: true}
	m := newFakePlayModeManager(t, f)
	if back, _ := m.HeardPrev(context.Background()); back {
		t.Fatal("an unknown position must not seek")
	}
	if got := f.calls(); len(got) != 1 || !strings.HasPrefix(got[0], "/player/prev") {
		t.Fatalf("engine calls = %q, want one plain prev", got)
	}
}

func TestToggleShuffleFlipsTheLiveState(t *testing.T) {
	f := &fakePlayModeEngine{}
	m := newFakePlayModeManager(t, f)
	on, err := m.ToggleShuffle(context.Background())
	if err != nil || !on {
		t.Fatalf("first press: on=%v err=%v", on, err)
	}
	on, err = m.ToggleShuffle(context.Background())
	if err != nil || on {
		t.Fatalf("second press: on=%v err=%v", on, err)
	}
}

func TestCycleRepeatStepsLikeTheSpotifyApp(t *testing.T) {
	f := &fakePlayModeEngine{}
	m := newFakePlayModeManager(t, f)
	for _, want := range []string{RepeatContext, RepeatTrack, RepeatOff, RepeatContext} {
		mode, err := m.CycleRepeat(context.Background())
		if err != nil {
			t.Fatalf("press: %v", err)
		}
		if mode != want {
			t.Fatalf("mode = %q, want %q", mode, want)
		}
		f.mu.Lock()
		if got := f.modes.repeat(); got != want {
			f.mu.Unlock()
			t.Fatalf("engine state = %q, want %q", got, want)
		}
		f.mu.Unlock()
	}
}

func TestCycleRepeatFromSongRepeatGoesOff(t *testing.T) {
	f := &fakePlayModeEngine{modes: playModes{RepeatContext: true, RepeatTrack: true}}
	m := newFakePlayModeManager(t, f)
	mode, err := m.CycleRepeat(context.Background())
	if err != nil || mode != RepeatOff {
		t.Fatalf("mode=%q err=%v, want off", mode, err)
	}
	if got := f.calls(); got[0] != `/player/repeat_track {"repeat_track":false}` {
		t.Fatalf("the song repeat must go off first, calls = %q", got)
	}
}
