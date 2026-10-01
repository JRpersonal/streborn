package spotify

import (
	"testing"
	"time"
)

// A speaker that is a member of a group refuses transport control, so picking
// it in Spotify starts nothing and the app moves playback elsewhere. The apps
// can only say that if the speaker records it.
func TestGroupedRefusalIsSilentUntilItHappens(t *testing.T) {
	m := &Manager{}
	if m.GroupedRefusal() {
		t.Error("a speaker that has refused nothing reports a refusal")
	}
}

func TestGroupedRefusalShowsAfterARefusal(t *testing.T) {
	m := &Manager{}
	m.NoteGroupedRefusal()
	if !m.GroupedRefusal() {
		t.Error("the refusal was not recorded, so the apps cannot explain the silence")
	}
}

// The speaker becomes usable the moment it leaves the group, so a notice that
// outlived the group would be worse than none.
func TestGroupedRefusalFadesOnItsOwn(t *testing.T) {
	m := &Manager{}
	m.NoteGroupedRefusal()
	m.mu.Lock()
	m.lastGroupedRefusalAt = time.Now().Add(-groupedRefusalWindow - time.Second)
	m.mu.Unlock()
	if m.GroupedRefusal() {
		t.Error("a stale refusal is still being shown")
	}
}

// A successful drive proves the speaker is not a group member any more.
func TestASuccessfulDriveClearsIt(t *testing.T) {
	m := &Manager{}
	m.NoteGroupedRefusal()
	m.ClearGroupedRefusal()
	if m.GroupedRefusal() {
		t.Error("the refusal survived a successful drive")
	}
}

// The window has to be long enough to switch to the Spotify app, see the
// speaker give up, and come back to STR to look.
func TestTheWindowIsLongEnoughToGoAndLook(t *testing.T) {
	if groupedRefusalWindow < 2*time.Minute {
		t.Errorf("window %v is too short to be seen", groupedRefusalWindow)
	}
	if groupedRefusalWindow > 15*time.Minute {
		t.Errorf("window %v outlives the group it describes", groupedRefusalWindow)
	}
}
