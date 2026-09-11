// #900, and the three wrong answers it drew before this one.
//
// Forming a group of idle speakers made music come out of the one that ended up
// leading it. Traced on shorty310's v0.9.79 bundle: forming the group wakes the
// master, the quiet wake's own STOP tears down what the firmware had just
// restored, that disconnect arms the re-push watchdog, and 0.14 s after
// "zone: formed" it pushes back a station the user had switched off two minutes
// earlier. It went out over UPnP, which only STR drives, so it was not the
// speaker doing it.
//
// The guard that looks right and is not: quietWakeActive() is CLEARED BY the
// zone join, 0.31 s before the push lands. These tests pin the timing so that
// mistake cannot be made a fourth time.

package webui

import (
	"testing"
	"time"
)

func TestAZoneChangeHoldsTheRePushWatchdog(t *testing.T) {
	s := quietServer("192.0.2.10")

	// Nothing recorded yet: a speaker that has never been in a group must not be
	// held, or the recovery this watchdog exists for never runs at all.
	if held, _ := s.zoneJustChanged(); held {
		t.Fatal("a speaker with no zone history was held")
	}

	s.noteZoneChanged()
	held, age := s.zoneJustChanged()
	if !held {
		t.Fatal("a zone change that just happened did not hold the watchdog")
	}
	if age > time.Second {
		t.Fatalf("age reads %s for a change made just now", age)
	}
}

// The window has to outlast the whole form handshake. In the bundle the quiet
// wake's STOP lands 2.7 s BEFORE "zone: formed", and the firmware's zoneUpdated
// can trail the drive by a second more, so anything tight enough to expire
// inside a normal group formation would let the push through again.
func TestTheWindowOutlastsAGroupFormation(t *testing.T) {
	if zoneSettleWindow < 10*time.Second {
		t.Fatalf("zoneSettleWindow is %s; the observed handshake alone spans ~3 s and the firmware trails it", zoneSettleWindow)
	}
	s := quietServer("192.0.2.10")
	s.quietWakeMu.Lock()
	s.zoneChangedAt = time.Now().Add(-(zoneSettleWindow - 2*time.Second))
	s.quietWakeMu.Unlock()
	if held, _ := s.zoneJustChanged(); !held {
		t.Fatal("a change two seconds inside the window did not hold")
	}
}

// And it has to end, or a speaker that joined a group once would lose its stream
// recovery for good.
func TestTheHoldExpires(t *testing.T) {
	s := quietServer("192.0.2.10")
	s.quietWakeMu.Lock()
	s.zoneChangedAt = time.Now().Add(-(zoneSettleWindow + time.Second))
	s.quietWakeMu.Unlock()
	if held, age := s.zoneJustChanged(); held {
		t.Fatalf("still held %s after the change; recovery would never run again", age)
	}
}

// Leaving a group counts too. Ungrouping tears the stream down exactly the same
// way, and the watchdog would push into the aftermath.
func TestLeavingAZoneCountsAsAChange(t *testing.T) {
	s := quietServer("192.0.2.10")
	s.NoteBoxZoneState("") // the firmware reporting no master: dissolved
	if held, _ := s.zoneJustChanged(); !held {
		t.Fatal("a dissolve did not stamp a zone change")
	}
}

func TestJoiningAZoneStampsTheChange(t *testing.T) {
	s := quietServer("192.0.2.10")
	s.NoteBoxZoneState("DEV-MASTER")
	if held, _ := s.zoneJustChanged(); !held {
		t.Fatal("a join did not stamp a zone change")
	}
}

// The trap, stated as a test so the reasoning survives. quietWakeActive() is
// cleared by the very event this guard has to react to, so the two must not be
// confused for one another: at the moment the push lands, the quiet wake is
// already over and the zone change is not.
func TestTheZoneStampOutlivesTheQuietWake(t *testing.T) {
	s := quietServer("192.0.2.10")
	s.armQuietWakeRestore(30)        // group operation begins, level muted
	s.NoteBoxZoneState("DEV-MASTER") // the join: restores the level AND stamps

	if s.quietWakeActive() {
		t.Fatal("the quiet wake should be over once the zone is joined; that is the race")
	}
	if held, _ := s.zoneJustChanged(); !held {
		t.Fatal("the zone stamp must survive the join, or the watchdog is unguarded exactly when it fires")
	}
}
