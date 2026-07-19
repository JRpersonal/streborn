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

// TestNoteUserPlayClearsLatches: a preset press / app play is newer intent than
// any earlier stop, so it must clear both suppression latches (#419: after a
// source bounce every preset press died against the stale latch).
func TestNoteUserPlayClearsLatches(t *testing.T) {
	s := &Server{}
	s.noteStandbyStop()
	s.NoteUserPlay()
	if s.standbyStoppedRecently() {
		t.Fatal("standby-bounce window must be cleared by a user play")
	}
	if s.userStoppedRecently() {
		t.Fatal("user-stop must be cleared by a user play")
	}
}

// TestHandleEnterStandby_SpontaneousDropDoesNotLatch: a UPNP->STANDBY drop with
// no physical key press anywhere near it is the firmware powering off STR's
// source on its own (#419). It must NOT be recorded as a deliberate user stop
// (which would suppress every recovery path) and must NOT clear the transport.
func TestHandleEnterStandby_SpontaneousDropDoesNotLatch(t *testing.T) {
	s, rec := newPlayTestServer(t)
	s.SetUserActivityFn(func() time.Time { return time.Now().Add(-5 * time.Minute) })

	s.HandleEnterStandby()

	if s.standbyStoppedRecently() || s.userStoppedRecently() {
		t.Fatal("a spontaneous source power-off must not arm the deliberate-stop latches (#419)")
	}
	if rec.count() != 0 {
		t.Fatalf("a spontaneous source power-off must not touch the transport, got %v", rec.list())
	}
}

// TestHandleEnterStandby_DropDuringOwnRecall: the box flipping to STANDBY
// moments after the user asked for playback (and with no NEW key since) is the
// firmware settling during STR's own recall, not a power-off. Latching and
// clearing here is what killed the in-flight recall (#419: 1036 wrong-state
// loop until a power pull).
func TestHandleEnterStandby_DropDuringOwnRecall(t *testing.T) {
	s, rec := newPlayTestServer(t)
	s.NoteUserPlay()
	// The key frame belonging to the preset press itself can trail the recall
	// start by a moment; it must not count as a NEW key.
	s.SetUserActivityFn(func() time.Time { return time.Now().Add(-time.Second) })

	s.HandleEnterStandby()

	if s.standbyStoppedRecently() || s.userStoppedRecently() {
		t.Fatal("a source flip during the user's own recall must not arm the stop latches (#419)")
	}
	if rec.count() != 0 {
		t.Fatalf("a source flip during the user's own recall must not clear the transport, got %v", rec.list())
	}
}

// TestHandleEnterStandby_PowerOffDuringRecallStillLatches guards #197: the user
// starting a preset and then pressing power DURING the recall is a real
// power-off (a NEW key press landed after the recall start). It must latch and
// clear the transport exactly as before.
func TestHandleEnterStandby_PowerOffDuringRecallStillLatches(t *testing.T) {
	s, rec := newPlayTestServer(t)
	s.standbyStopMu.Lock()
	s.lastUserPlayStart = time.Now().Add(-10 * time.Second) // recall still active
	s.standbyStopMu.Unlock()
	s.SetUserActivityFn(func() time.Time { return time.Now() }) // fresh power press

	s.HandleEnterStandby()

	if !s.standbyStoppedRecently() {
		t.Fatal("a power press during the recall must still arm the standby-bounce window (#197)")
	}
	if !s.userStoppedRecently() {
		t.Fatal("a power press during the recall must still arm the user-stop (#197)")
	}
	if !rec.has("Stop") {
		t.Fatalf("a power press during the recall must still clear the transport (#197), got %v", rec.list())
	}
}

// TestHandleEnterStandby_NoActivitySignalStaysConservative: firmware that never
// emits userActivityUpdate gives the discriminator no signal (zero lastKey), so
// every drop must keep the conservative #197 handling: latch + clear.
func TestHandleEnterStandby_NoActivitySignalStaysConservative(t *testing.T) {
	s, rec := newPlayTestServer(t)
	s.SetUserActivityFn(func() time.Time { return time.Time{} })

	s.HandleEnterStandby()

	if !s.standbyStoppedRecently() || !s.userStoppedRecently() {
		t.Fatal("without an activity signal every drop must keep the conservative #197 latching")
	}
	if !rec.has("Stop") {
		t.Fatalf("without an activity signal the transport must still be cleared (#197), got %v", rec.list())
	}
}

// TestHandleEnterStandby_AppStandbyStaysDeliberate: a standby STR itself sent
// (app / phone remote) produces no gabbo key frame, so the activity
// discriminator alone would misread the resulting drop as spontaneous and wake
// the box right back up. NoteUserStop (armed by the standby endpoints) must
// keep the conservative deliberate handling.
func TestHandleEnterStandby_AppStandbyStaysDeliberate(t *testing.T) {
	s, rec := newPlayTestServer(t)
	s.SetUserActivityFn(func() time.Time { return time.Now().Add(-5 * time.Minute) })
	s.NoteUserStop() // what handleBoxSource/handleBoxPower arm before /standby

	s.HandleEnterStandby()

	if !s.standbyStoppedRecently() {
		t.Fatal("an app-initiated standby must keep the deliberate power-off handling (#419)")
	}
	if !rec.has("Stop") {
		t.Fatalf("an app-initiated standby must still clear the transport (#197), got %v", rec.list())
	}
}

// TestHandleEnterStandby_SpontaneousDropResumesActiveStream: when the drop
// interrupted a stream the box was actively pulling, STR must re-push it (the
// firmware hiccup, not the user, stopped the music, #419).
func TestHandleEnterStandby_SpontaneousDropResumesActiveStream(t *testing.T) {
	s, rec := newPlayTestServer(t)
	s.SetUserActivityFn(func() time.Time { return time.Now().Add(-5 * time.Minute) })
	s.SetStreamActivityFn(func() (time.Time, time.Time) { return time.Now(), time.Time{} })
	s.setLastPlay("http://127.0.0.1:8888/stream/3", "Station", "", "")

	s.HandleEnterStandby()

	// The recovery settles ~3s before pushing; poll for the re-push.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if rec.has("SetAVTransportURI") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("spontaneous-off recovery never re-pushed the interrupted stream, got %v", rec.list())
}
