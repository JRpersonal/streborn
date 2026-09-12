package webui

import (
	"testing"
	"time"
)

// The group-wake settling window and the mute are two different clocks, and
// #900 was misdiagnosed once because they were treated as one. The zone join
// lifts the mute, and it lands roughly a second BEFORE the preset reconcile
// asks whether it may write. A window derived from the mute is therefore
// already gone at the only moment it is read.
func TestQuietWakeEpisodeOutlivesTheMute(t *testing.T) {
	s := &Server{}

	if s.QuietWakeEpisodeActive() {
		t.Fatal("a speaker nobody woke reports a group wake in progress")
	}

	s.noteQuietWakeEpisode()
	if !s.QuietWakeEpisodeActive() {
		t.Fatal("the window is not open right after a quiet wake")
	}

	// What the zone join does: arm, then restore. Both touch quietWakeUntil.
	s.armQuietWakeRestore(26)
	s.quietWakeMu.Lock()
	s.quietWakeUntil = time.Time{}
	s.quietWakeMu.Unlock()

	if s.quietWakeActive() {
		t.Fatal("the mute is meant to be gone after the zone join restored it")
	}
	if !s.QuietWakeEpisodeActive() {
		t.Error("the zone join cleared the settling window, which is the #900 race")
	}
}

// It is a deadline, so a stuck flag can never block hardware-key registration.
func TestQuietWakeEpisodeExpires(t *testing.T) {
	s := &Server{}
	s.quietWakeMu.Lock()
	s.quietWakeEpisodeUntil = time.Now().Add(-time.Second)
	s.quietWakeMu.Unlock()
	if s.QuietWakeEpisodeActive() {
		t.Error("a window whose deadline has passed still reports active")
	}
}

// The window has to outlast the whole sequence it exists for: the wake takes
// about four seconds, the zone forms two or three behind it, and the re-sync
// the wake scheduled writes after that. Measured from her bundle: power sent
// at 06:26:50.343, the preset write at 06:26:55.857.
func TestQuietWakeEpisodeWindowCoversTheSequence(t *testing.T) {
	if quietWakeEpisodeWindow < 15*time.Second {
		t.Errorf("window %s is shorter than the wake-plus-zone-plus-resync sequence it guards", quietWakeEpisodeWindow)
	}
}
