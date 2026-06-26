package webui

import (
	"testing"
	"time"
)

// TestStandbyStoppedRecently covers the discriminator ResumeLastPlay uses to tell
// the scm power-off bounce (a DO_NOT_RESUME that follows a power-OFF, #197) from a
// genuine power-on. HandleEnterStandby stamps lastStandbyStop when it clears the
// transport; a DO_NOT_RESUME "wake" within standbyBounceWakeWindow of that stamp
// is the bounce and must NOT resume.
func TestStandbyStoppedRecently(t *testing.T) {
	s := &Server{}

	if s.standbyStoppedRecently() {
		t.Fatal("no standby-stop recorded yet, want false")
	}

	// Mirrors HandleEnterStandby stamping the transport clear. The bounce's
	// DO_NOT_RESUME wake fires ~200 ms later and ResumeLastPlay settles 2 s before
	// it checks, so a stamp ~2 s old must still read as "recent".
	s.standbyStopMu.Lock()
	s.lastStandbyStop = time.Now().Add(-2 * time.Second)
	s.standbyStopMu.Unlock()
	if !s.standbyStoppedRecently() {
		t.Fatal("standby-stop ~2s ago is within the bounce window, want true")
	}

	// A power-on long after a power-off (the overnight case the resume default is
	// for) is past the window and must be allowed to resume.
	s.standbyStopMu.Lock()
	s.lastStandbyStop = time.Now().Add(-(standbyBounceWakeWindow + time.Second))
	s.standbyStopMu.Unlock()
	if s.standbyStoppedRecently() {
		t.Fatal("standby-stop older than the bounce window, want false")
	}
}

// TestNoteStandbyStopArmsSuppression covers the decoupling: noteStandbyStop must
// arm BOTH the standby-bounce window and the user-stop on every UPNP->STANDBY,
// independent of the transport-clear, so a zoned/debounced power-off still
// suppresses all three wake paths. It also debounces the burst.
func TestNoteStandbyStopArmsSuppression(t *testing.T) {
	s := &Server{}

	if s.standbyStoppedRecently() || s.userStoppedRecently() {
		t.Fatal("nothing armed yet, want both false")
	}

	// A power-off arms BOTH the standby-bounce window (ResumeLastPlay) and the
	// user-stop (maybeRePush / RecoverAfterReconnect), regardless of the transport-
	// clear path the caller takes afterward.
	s.noteStandbyStop()
	if !s.standbyStoppedRecently() {
		t.Fatal("standby-bounce window not armed after noteStandbyStop")
	}
	if !s.userStoppedRecently() {
		t.Fatal("user-stop not armed after noteStandbyStop (maybeRePush/RecoverAfterReconnect rely on it)")
	}
}
