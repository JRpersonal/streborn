package oled

import (
	"testing"
	"time"
)

// A tap followed by a hold saves onto the key's second row; the screens name
// the slot as the key pressed twice, within the big line's width, and the
// outcome reaches the held-key session of that key.
func TestRowTwoNotices(t *testing.T) {
	held := KeyHeldRowTwo(1)
	if held.Kind != NoticeKeyHeld || held.Key != 1 || held.Line1 != "KEY 2X 1" || held.Line2 != "SAVING..." {
		t.Fatalf("held screen = %+v", held)
	}
	saved := SavedOnRowTwo(6, "Jens Chill")
	if saved.Kind != NoticeSavedByKey || saved.Key != 6 || saved.Line1 != "SAVED 2X 6" || saved.Line2 != "JENS CHILL" {
		t.Fatalf("saved notice = %+v", saved)
	}
	dup := AlreadyOnKey(8)
	if dup.Key != 2 || dup.Line1 != "ON 2X 2" {
		t.Fatalf("row-2 duplicate notice = %+v", dup)
	}
	if n := AlreadyOnKey(3); n.Key != 3 || n.Line1 != "ON KEY 3" {
		t.Fatalf("row-1 duplicate notice changed: %+v", n)
	}
	for _, l := range []string{held.Line1, saved.Line1, dup.Line1} {
		if len(l) > line1Max {
			t.Errorf("%q is longer than %d characters", l, line1Max)
		}
	}
	s := &heldSession{key: 6, screen: KeyHeldRowTwo(6)}
	if !s.takes(saved) || !s.takes(NotSaved(6)) || !s.takes(dup) {
		t.Fatal("the row-2 outcomes must reach the held-key session of the key")
	}
}

// The row-2 held screen runs as the same session a plain held key does.
func TestKeyHoldRowTwoSession(t *testing.T) {
	r := newSessionRig()
	r.tick = once(600*time.Millisecond, func() { r.post(SavedOnRowTwo(1, "Jens Chill")) })
	r.holdStartScreen(1, r.now(), KeyHeldRowTwo(1))
	r.drain()
	if r.sessions != 1 || len(r.kinds) != 2 || r.kinds[0] != NoticeKeyHeld || r.kinds[1] != NoticeSavedByKey {
		t.Fatalf("sessions %d, panel showed %v", r.sessions, r.kinds)
	}
	if len(r.shown) != 0 {
		t.Fatalf("the outcome also showed as a separate notice: %v", r.kinds)
	}
}
