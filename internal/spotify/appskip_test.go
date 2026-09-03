package spotify

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func newAppSkipTestManager() *Manager {
	return &Manager{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestParseLoadedTrackDurMs(t *testing.T) {
	line := strings.ToLower(`time="2026-08-29T14:19:00+02:00" level=info msg="loaded track \"Rain\" (paused: false, position: 9ms, duration: 236400ms, prefetched: true)" uri="spotify:track:x"`)
	if got := parseLoadedTrackDurMs(line); got != 236400 {
		t.Fatalf("duration = %d, want 236400", got)
	}
	if got := parseLoadedTrackDurMs("no duration here"); got != 0 {
		t.Fatalf("lines without a duration must parse to 0, got %d", got)
	}
}

func TestCutShortOfDuration(t *testing.T) {
	full := int64(236400) // ms
	fullGran := full * vorbisRate / 1000
	cases := []struct {
		name            string
		gran, body, dur int64
		want            bool
	}{
		// A natural end's granule matches the duration within fractions of a
		// second (live 2026-08-29: 11855256 samples vs 268826 ms).
		{"natural end", fullGran + 1176, 4096, full, false},
		// The observed app skip: La Grange cut at 207.5 s of 230.5 s.
		{"mid-track cut", 9150400, 4096, 230480, true},
		{"cut in the final seconds is natural", fullGran - 4*vorbisRate, 4096, full, false},
		{"unknown duration never fires", 100000, 4096, 0, false},
		{"empty previous track never fires", 100000, 0, full, false},
	}
	for _, c := range cases {
		if got := cutShortOfDuration(c.gran, c.body, c.dur); got != c.want {
			t.Errorf("%s: cutShortOfDuration(%d, %d, %d) = %v, want %v", c.name, c.gran, c.body, c.dur, got, c.want)
		}
	}
}

// The full detector path: durations queue up from engine log lines, each
// boundary shifts the queue, and only a mid-track cut with no STR cut armed
// re-points an attached box.
func TestNoteTrackBoundaryCutFiresOnForeignSkip(t *testing.T) {
	m := newAppSkipTestManager()
	fired := make(chan struct{}, 2)
	m.SetOnActivate(func(context.Context) { fired <- struct{}{} })
	m.sink = io.Discard // a box is attached

	// Track A loads (269 s), its BOS arrives: the queue shifts, but the
	// PREVIOUS stream track has no known duration (agent saw no load for
	// it), so nothing may fire.
	m.noteLibrespotLine(`level=info msg="loaded track \"a\" (position: 0ms, duration: 268826ms, prefetched: true)"`)
	m.noteTrackBoundaryCut(0, 0)
	select {
	case <-fired:
		t.Fatal("boundary with unknown previous duration must not re-point")
	case <-time.After(50 * time.Millisecond):
	}

	// Track B loads, and A's boundary arrives 23 s early: an app skip.
	m.noteLibrespotLine(`level=info msg="loaded track \"b\" (position: 0ms, duration: 230480ms, prefetched: true)"`)
	m.noteTrackBoundaryCut((268826-23000)*vorbisRate/1000, 4096)
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("mid-track cut with no STR cut armed must re-point the box")
	}
	if m.LastSkipBoundary().IsZero() {
		t.Fatal("a foreign cut must stamp the skip boundary")
	}
}

func TestNoteTrackBoundaryCutStandsDown(t *testing.T) {
	t.Run("STR skip armed the cut", func(t *testing.T) {
		m := newAppSkipTestManager()
		fired := make(chan struct{}, 1)
		m.SetOnActivate(func(context.Context) { fired <- struct{}{} })
		m.sink = io.Discard
		m.noteLibrespotLine(`level=info msg="loaded track \"a\" (duration: 268826ms)"`)
		m.noteTrackBoundaryCut(0, 0) // seed streamTrackDurMs
		m.NoteSkip()
		m.noteTrackBoundaryCut(100*vorbisRate, 4096) // cut at 100 s of 269 s
		select {
		case <-fired:
			t.Fatal("an armed STR cut must keep the detector silent")
		case <-time.After(50 * time.Millisecond):
		}
	})
	t.Run("no box attached", func(t *testing.T) {
		m := newAppSkipTestManager()
		fired := make(chan struct{}, 1)
		m.SetOnActivate(func(context.Context) { fired <- struct{}{} })
		m.noteLibrespotLine(`level=info msg="loaded track \"a\" (duration: 268826ms)"`)
		m.noteTrackBoundaryCut(0, 0)
		m.noteTrackBoundaryCut(100*vorbisRate, 4096)
		select {
		case <-fired:
			t.Fatal("a detached or paused box must not be forced back into playing")
		case <-time.After(50 * time.Millisecond):
		}
	})
}
