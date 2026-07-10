package webui

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// verifyStandDownReason is the single policy behind verifyRecall's abort
// decision. It must stop a verify that was superseded by a newer play (two
// rapid preset presses used to spawn dueling verifies that ping-ponged the
// stations for ~15s) and one the user overrode with a stop/pause/power-off
// after the recall started - while never stopping a verify because of a stop
// that PRECEDED the recall (stop, then recall something else, is normal).
func TestVerifyStandDownReason(t *testing.T) {
	start := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)
	after := start.Add(3 * time.Second)
	before := start.Add(-3 * time.Second)
	none := time.Time{}

	cases := []struct {
		name                 string
		recallGen, curGen    uint64
		userStop, standbyOff time.Time
		wantContains         string // "" = keep verifying
	}{
		{"current recall keeps verifying", 4, 4, none, none, ""},
		{"newer play supersedes", 4, 5, none, none, "superseded"},
		{"user stop after recall stands down", 4, 4, after, none, "stopped"},
		{"user stop before recall is ignored", 4, 4, before, none, ""},
		{"power-off after recall stands down", 4, 4, none, after, "powered off"},
		{"power-off before recall is ignored", 4, 4, none, before, ""},
		// noteStandbyStop stamps BOTH; the power-off must win the log reason.
		{"power-off wins over the paired user stop", 4, 4, after, after, "powered off"},
		{"supersession wins over everything", 4, 6, after, after, "superseded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := verifyStandDownReason(tc.recallGen, tc.curGen, start, tc.userStop, tc.standbyOff)
			if tc.wantContains == "" {
				if got != "" {
					t.Errorf("verifyStandDownReason() = %q, want empty (keep verifying)", got)
				}
				return
			}
			if !strings.Contains(got, tc.wantContains) {
				t.Errorf("verifyStandDownReason() = %q, want reason containing %q", got, tc.wantContains)
			}
		})
	}
}

// Every setLastPlay must bump the recall generation, and the live stand-down
// read must see it: that is what makes an older recall's verify abort the
// moment a newer play lands.
func TestSetLastPlayBumpsRecallGeneration(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	start := time.Now()

	g1 := s.setLastPlay("http://127.0.0.1:8888/stream/4", "A", "", "")
	if reason := s.recallStandDownReason(g1, start); reason != "" {
		t.Fatalf("fresh recall: stand-down reason %q, want none", reason)
	}

	g2 := s.setLastPlay("http://127.0.0.1:8888/stream/5", "B", "", "")
	if g2 != g1+1 {
		t.Fatalf("generations: got %d then %d, want a +1 bump", g1, g2)
	}
	if reason := s.recallStandDownReason(g1, start); !strings.Contains(reason, "superseded") {
		t.Errorf("old recall after a newer play: reason %q, want superseded", reason)
	}
	if reason := s.recallStandDownReason(g2, start); reason != "" {
		t.Errorf("newest recall: stand-down reason %q, want none", reason)
	}
}
