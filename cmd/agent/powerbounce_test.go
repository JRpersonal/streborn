package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxlog"
)

func TestPowerKeyBounced(t *testing.T) {
	t0 := time.Date(2026, 11, 9, 6, 28, 29, 0, time.UTC)
	cases := []struct {
		name    string
		release time.Time
		press   time.Time
		want    bool
	}{
		{"no release seen yet", time.Time{}, t0, false},
		// the #1798 trace: release, then the same push read as a new press
		{"84 ms after the release", t0, t0.Add(84 * time.Millisecond), true},
		{"162 ms after the release", t0, t0.Add(162 * time.Millisecond), true},
		// the reporter's real second press came 6 and 20 s later
		{"a deliberate press later", t0, t0.Add(6 * time.Second), false},
		{"a press before the release", t0, t0.Add(-time.Second), false},
	}
	for _, c := range cases {
		if _, got := powerKeyBounced(c.release, c.press); got != c.want {
			t.Errorf("%s: bounced=%v, want %v", c.name, got, c.want)
		}
	}
}

// The #1798 sequence from the trace: one push read as press, release, press,
// release. The second press is logged as the double read; a later deliberate
// press is not.
func TestNotePowerKeyWarnsOnTheDoubleRead(t *testing.T) {
	keyLogMu.Lock()
	powerReleaseAt = time.Time{}
	keyLogMu.Unlock()
	t.Cleanup(func() {
		keyLogMu.Lock()
		powerReleaseAt = time.Time{}
		keyLogMu.Unlock()
	})
	var buf bytes.Buffer
	h := &presetWsHandler{logger: slog.New(slog.NewTextHandler(&buf, nil))}
	at := time.Date(2026, 11, 9, 6, 28, 29, 47_000_000, time.UTC)
	ev := func(state boxlog.State, ms int) boxlog.KeyEvent {
		return boxlog.KeyEvent{Name: "POWER", Producer: boxlog.ProducerIRRemote, State: state,
			Origin: boxlog.OriginTrace, At: at.Add(time.Duration(ms) * time.Millisecond)}
	}
	h.notePowerKey(ev(boxlog.StatePressed, 0))
	h.notePowerKey(ev(boxlog.StateReleased, 99))
	if strings.Contains(buf.String(), "pressed again") {
		t.Fatalf("warned before the second press:\n%s", buf.String())
	}
	h.notePowerKey(ev(boxlog.StatePressed, 183))
	if !strings.Contains(buf.String(), "pressed again") || !strings.Contains(buf.String(), "gapMs=84") {
		t.Fatalf("no double-read warning:\n%s", buf.String())
	}
	h.notePowerKey(ev(boxlog.StateReleased, 280))
	buf.Reset()
	h.notePowerKey(ev(boxlog.StatePressed, 20_000))
	if strings.Contains(buf.String(), "pressed again") {
		t.Fatalf("a deliberate press 20 s later was read as a double read:\n%s", buf.String())
	}
}
