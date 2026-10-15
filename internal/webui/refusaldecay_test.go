package webui

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

// #1314: an ST10 stereo master refused every UPnP push while its native radio
// played all day, and the "restart the speaker" banner never went away because
// only an observed UPnP success cleared it. The latch now decays once no new
// refusal has landed for refusalDecay.
func TestRefusalLatchDecays(t *testing.T) {
	disc := slog.New(slog.NewTextHandler(io.Discard, nil))
	cases := []struct {
		name       string
		strikeAges []time.Duration // how long ago each strike landed, oldest first
		wantActive bool
	}{
		{"one strike stays quiet", []time.Duration{time.Minute}, false},
		{"two recent strikes latch", []time.Duration{2 * time.Minute, time.Minute}, true},
		{"latched, last strike just inside the window", []time.Duration{9*time.Minute + 30*time.Second, 9 * time.Minute}, true},
		{"two strikes far apart are not a pattern", []time.Duration{3 * time.Hour, time.Minute}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Server{logger: disc}
			for _, age := range c.strikeAges {
				s.noteRefusalStrike("login")
				// Backdate the strike just recorded.
				s.refusal.mu.Lock()
				s.refusal.lastStrike = time.Now().Add(-age)
				if s.refusal.latched {
					s.refusal.since = s.refusal.lastStrike
				}
				s.refusal.mu.Unlock()
			}
			if active, _ := s.RecallRefusal(); active != c.wantActive {
				t.Errorf("RecallRefusal() = %v, want %v", active, c.wantActive)
			}
		})
	}
}

// A latch whose most recent strike is older than refusalDecay reads inactive,
// and an expired latch is reset, not just hidden: the next single refusal must not
// re-latch it on the strength of strikes that have already aged out.
func TestRefusalLatchExpiryResetsStrikes(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.noteRefusalStrike("silent")
	s.noteRefusalStrike("silent")
	s.refusal.mu.Lock()
	s.refusal.lastStrike = time.Now().Add(-refusalDecay - time.Second)
	s.refusal.mu.Unlock()
	if active, _ := s.RecallRefusal(); active {
		t.Fatal("the latch must have expired")
	}
	s.noteRefusalStrike("silent")
	if active, _ := s.RecallRefusal(); active {
		t.Fatal("one fresh refusal after an expired latch must stay quiet")
	}
	s.noteRefusalStrike("silent")
	if active, _ := s.RecallRefusal(); !active {
		t.Fatal("two fresh refusals must latch again")
	}
}
