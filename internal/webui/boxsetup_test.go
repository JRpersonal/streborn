package webui

// The setup-episode counters (#873): what the desktop app reads to say, after
// the fact, what the speaker was doing during a window in which it answered
// the PC nothing at all.

import (
	"testing"
	"time"
)

func TestBoxSetupStartsAtNothing(t *testing.T) {
	s := &Server{}
	state, episodes, lastSec := s.BoxSetup()
	if state != "no" || episodes != 0 || lastSec != 0 {
		t.Fatalf("a fresh agent reports %q/%d/%d, want no/0/0", state, episodes, lastSec)
	}
	if d := s.boxSetupDebug(); d["state"] != "no" || d["episodes"] != 0 {
		t.Errorf("the bundle section of a fresh agent reads %v", d)
	}
}

func TestBoxSetupCountsAnEpisodeAndReportsItActive(t *testing.T) {
	s := &Server{}
	s.NoteBoxSetupEpisode()
	state, episodes, _ := s.BoxSetup()
	if state != "active" || episodes != 1 {
		t.Fatalf("after one episode: %q/%d, want active/1", state, episodes)
	}
	// The firmware re-raises setup every couple of minutes while an episode
	// lasts. Counting each flip separately would turn one incident into
	// fifteen, and the number is what the app shows the user.
	s.NoteBoxSetupEpisode()
	s.NoteBoxSetupEpisode()
	if _, episodes, _ = s.BoxSetup(); episodes != 1 {
		t.Errorf("three flips inside the active window counted as %d episodes, want 1", episodes)
	}

	// A flip after the window is a NEW episode.
	s.boxSetup.mu.Lock()
	s.boxSetup.lastAt = time.Now().Add(-2 * boxSetupActiveWindow)
	s.boxSetup.mu.Unlock()
	s.NoteBoxSetupEpisode()
	if _, episodes, _ = s.BoxSetup(); episodes != 2 {
		t.Errorf("a flip after the active window counted as %d episodes, want 2", episodes)
	}
}

// Clearing the source does NOT end the episode. The whole point of #873 is
// that the firmware raises it again afterwards, and a counter that reset on
// every clear would report zero at exactly the moment the user is looking.
func TestBoxSetupClearDoesNotEndTheEpisode(t *testing.T) {
	s := &Server{}
	s.NoteBoxSetupEpisode()
	s.NoteBoxSetupCleared()
	s.NoteBoxSetupCleared()
	state, episodes, _ := s.BoxSetup()
	if state != "active" || episodes != 1 {
		t.Fatalf("after two clears: %q/%d, want active/1", state, episodes)
	}
	if d := s.boxSetupDebug(); d["clears"] != 2 {
		t.Errorf("the bundle section reports clears=%v", d["clears"])
	}
}

// The four words the desktop app needs, by age. "stale" is its own word: a
// consumer reading only the state must be able to tell a speaker that never
// did this from one that did it an hour ago.
func TestBoxSetupAgesFromActiveThroughRecentToStale(t *testing.T) {
	s := &Server{}
	s.NoteBoxSetupEpisode()
	for _, c := range []struct {
		age  time.Duration
		want string
	}{
		{time.Minute, "active"},
		{boxSetupActiveWindow + time.Minute, "recent"},
		{boxSetupRecentWindow + time.Minute, "stale"},
	} {
		s.boxSetup.mu.Lock()
		s.boxSetup.lastAt = time.Now().Add(-c.age)
		s.boxSetup.mu.Unlock()
		if state, _, _ := s.BoxSetup(); state != c.want {
			t.Errorf("an episode %s old reads %q, want %q", c.age, state, c.want)
		}
	}
	// The COUNT survives the state going quiet: the report still has to be
	// able to say the speaker did this at all.
	if _, episodes, _ := s.BoxSetup(); episodes != 1 {
		t.Errorf("the episode count was lost when the state aged out (%d)", episodes)
	}
}
