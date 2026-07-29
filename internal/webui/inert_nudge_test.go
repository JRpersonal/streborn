package webui

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

// The nudge exists for exactly one state: no source selected AND nothing
// fetched since the recall started. `sys power` is a toggle, so every other
// state must be left alone or a healthy speaker gets switched off.
func TestRecallBoxInert(t *testing.T) {
	started := time.Now()
	cases := []struct {
		name      string
		source    string
		lastFetch time.Time
		activity  bool
		want      bool
	}{
		{"dead source, nothing fetched", "INVALID_SOURCE", started.Add(-time.Minute), true, true},
		{"dead source but the stream is being pulled", "INVALID_SOURCE", started.Add(2 * time.Second), true, false},
		{"playing normally", "UPNP", started.Add(-time.Minute), true, false},
		{"genuinely in standby", "STANDBY", started.Add(-time.Minute), true, false},
		{"source unreadable", "", started.Add(-time.Minute), true, false},
		{"no activity signal wired", "INVALID_SOURCE", time.Time{}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			src := c.source
			s.boxSourceFn = func() string { return src }
			if c.activity {
				lf := c.lastFetch
				s.streamActivityFn = func() (time.Time, time.Time) { return lf, time.Time{} }
			}
			if got := s.recallBoxInert(started); got != c.want {
				t.Errorf("recallBoxInert = %v, want %v", got, c.want)
			}
		})
	}
}
