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
