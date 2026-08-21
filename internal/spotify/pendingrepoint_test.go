package spotify

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func repointRecorder(t *testing.T) (*Manager, *atomic.Int32) {
	t.Helper()
	var fired atomic.Int32
	m := &Manager{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	m.onActivate = func(context.Context) { fired.Add(1) }
	return m, &fired
}

// will_play announces the NEXT track. On a generated radio playlist that
// arrives while the current song still has time to run, and re-pointing there
// tore the box off the stream and made the engine start the announced track
// early: the speaker appeared to skip on its own while the previous track was
// not over (live 2026-08-15 17:40:52). Holding the re-point until that track
// actually starts is the whole fix, so the held state must not act on its own.
func TestAHeldRepointDoesNothingUntilTheTrackStarts(t *testing.T) {
	m, fired := repointRecorder(t)
	m.pendingRepointFrom, m.pendingRepointTo = "spotify:playlist:old", "spotify:playlist:new"

	if fired.Load() != 0 {
		t.Fatal("holding a re-point must not fire it")
	}
	m.repointForPendingContext()
	waitFor(t, fired, 1, "the held re-point fires once the track starts")

	// The second event of the same track (playing then metadata) must not
	// re-point again.
	m.repointForPendingContext()
	if got := fired.Load(); got != 1 {
		t.Errorf("re-points = %d, want exactly 1: the second event of the same track must find nothing held", got)
	}
	if m.pendingRepointTo != "" {
		t.Errorf("the held re-point was not cleared: %q", m.pendingRepointTo)
	}
}

// Called on every playing/metadata event, so the common case is that nothing
// is held and nothing must happen.
func TestNothingHeldMeansNoRepoint(t *testing.T) {
	m, fired := repointRecorder(t)
	m.repointForPendingContext()
	m.repointForPendingContext()
	if got := fired.Load(); got != 0 {
		t.Errorf("re-points = %d, want 0 when no context change was announced", got)
	}
}

func waitFor(t *testing.T, got *atomic.Int32, want int32, what string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if got.Load() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("%s: got %d, want %d", what, got.Load(), want)
}

// The first track of every recall was cut off, and only the first: Play stores
// the unwrapped playlist while the engine announces the station wrapper for
// the same context, so the two strings differed and a re-point was armed.
func TestTheStationWrapperIsNotAPlaylistChange(t *testing.T) {
	cases := []struct {
		stored, announced string
		same              bool
	}{
		{"spotify:playlist:6xKB", "spotify:station:playlist:6xKB", true},
		{"spotify:station:playlist:6xKB", "spotify:playlist:6xKB", true},
		{"spotify:playlist:6xKB", "spotify:playlist:6xKB", true},
		{"spotify:playlist:6xKB", "spotify:playlist:OTHER", false},
		{"spotify:album:1", "spotify:playlist:1", false},
	}
	for _, c := range cases {
		got := normalizeContextURI(c.stored) == normalizeContextURI(c.announced)
		if got != c.same {
			t.Errorf("%q vs %q: same=%v, want %v", c.stored, c.announced, got, c.same)
		}
	}
}

// Twice in two days the music stopped on every speaker of a group at once, and
// twice it was reported as something else: a preset press the first time, a
// volume change the second. Both times the log said the same thing, that the
// engine could not resolve what follows the playlist and stopped, and STR read
// that stop as the listener stopping it and parked its recovery.
func TestAPlaylistRunningOutIsNotAUserStop(t *testing.T) {
	var latched atomic.Int32
	m := &Manager{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	m.connectPauseFn = func(string) { latched.Add(1) }

	m.lastCtxResolveFailAt = time.Now()
	m.handleEnginePlaybackEnd("stopped")
	if latched.Load() != 0 {
		t.Error("a stop right after the engine gave up on the continuation must not arm the deliberate-stop latch")
	}

	// A stop long after that failure IS the listener, and must still latch.
	m.lastCtxResolveFailAt = time.Now().Add(-time.Hour)
	m.handleEnginePlaybackEnd("stopped")
	if latched.Load() != 1 {
		t.Errorf("a genuine stop must still latch, got %d", latched.Load())
	}

	// And so is a stop with no such failure recorded at all.
	m.lastCtxResolveFailAt = time.Time{}
	m.handleEnginePlaybackEnd("paused")
	if latched.Load() != 2 {
		t.Errorf("a pause with no resolve failure must latch, got %d", latched.Load())
	}
}

// The sibling false signal, caught live 2026-08-21: the engine failed to LOAD
// the next track mid-playlist ("failed advancing to next track"), stopped, and
// STR read that stop as the listener stopping playback in the Spotify app. A
// six-speaker group fell silent after six songs with no recovery.
func TestATrackLoadFailureIsNotAUserStop(t *testing.T) {
	var latched atomic.Int32
	m := &Manager{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	m.connectPauseFn = func(string) { latched.Add(1) }

	m.lastTrackLoadFailAt = time.Now()
	m.handleEnginePlaybackEnd("stopped")
	if latched.Load() != 0 {
		t.Error("a stop right after a track-load failure must not arm the deliberate-stop latch")
	}
	if m.lastAutoAdvanceAt.IsZero() {
		t.Error("the load-fail stop must attempt one auto-advance (stamp missing)")
	}

	// A second load-fail stop inside the rate window must not advance again.
	prev := m.lastAutoAdvanceAt
	m.lastTrackLoadFailAt = time.Now()
	m.handleEnginePlaybackEnd("stopped")
	if !m.lastAutoAdvanceAt.Equal(prev) {
		t.Error("auto-advance must be rate-limited to one per window")
	}
	if latched.Load() != 0 {
		t.Error("the rate-limited second stop must still not latch")
	}

	// A stop long after the failure IS the listener, and must still latch.
	m.lastTrackLoadFailAt = time.Now().Add(-time.Hour)
	m.handleEnginePlaybackEnd("stopped")
	if latched.Load() != 1 {
		t.Errorf("a genuine stop must still latch, got %d", latched.Load())
	}
}

// The engine frequently recovers from a load failure on its own a few seconds
// later. An immediate auto-advance then skips the very track it just loaded:
// two seconds of the next song, then the one after (live 2026-08-21, three
// times in one playlist). The advance must wait and stand down on recovery.
func TestAutoAdvanceStandsDownWhenTheEngineRecovers(t *testing.T) {
	m, calls, cleanup := mockLibrespot(t)
	defer cleanup()
	m.selfRecoveryWait = 50 * time.Millisecond

	m.lastTrackLoadFailAt = time.Now()
	m.handleEnginePlaybackEnd("stopped")
	// The engine loads the next track by itself before the wait ends. The
	// small sleep keeps the two stamps in distinct clock ticks (Windows'
	// timer granularity); in production the recovery arrives seconds later.
	time.Sleep(20 * time.Millisecond)
	m.handleEnginePlaybackStart()

	time.Sleep(250 * time.Millisecond)
	for _, p := range pathsOf(*calls) {
		if p == "/player/next" {
			t.Fatal("the engine recovered on its own, the auto-advance must not skip")
		}
	}
}

func TestAutoAdvanceFiresWhenTheEngineStaysDown(t *testing.T) {
	m, calls, cleanup := mockLibrespot(t)
	defer cleanup()
	m.selfRecoveryWait = 50 * time.Millisecond

	m.lastTrackLoadFailAt = time.Now()
	m.handleEnginePlaybackEnd("stopped")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range pathsOf(*calls) {
			if p == "/player/next" {
				return // advanced, as it must
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("engine stayed down but the auto-advance never skipped")
}
