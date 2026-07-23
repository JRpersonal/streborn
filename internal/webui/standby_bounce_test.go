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

// waitForAction polls for an async transport action: HandleEnterStandby issues
// its Stop+ClearURI in a goroutine so the SOAP round-trips cannot stall the
// gabbo read loop (#252).
func waitForAction(t *testing.T, rec *soapRecorder, action string) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if rec.has(action) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// TestHandleEnterStandby_PowerOffDuringRecallStillLatches guards #197: the user
// starting a preset and then pressing power DURING the recall is a real
// power-off (a NEW key press landed after the recall start, immediately
// adjacent to the flip). It must latch and clear the transport exactly as
// before.
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
	if !waitForAction(t, rec, "Stop") {
		t.Fatalf("a power press during the recall must still clear the transport (#197), got %v", rec.list())
	}
}

// TestHandleEnterStandby_StaleKeyDuringRecallDoesNotLatch: a key press that is
// merely SOMEWHERE in the recall window (a volume tweak seconds earlier) is not
// a power press - the source flip does not follow it immediately. Latching on
// it reclassified a routine firmware flap as a user power-off, cleared the
// transport mid-recall and stood every recovery down: the ST20 that "switches
// itself off on every preset press" (#252).
func TestHandleEnterStandby_StaleKeyDuringRecallDoesNotLatch(t *testing.T) {
	s, rec := newPlayTestServer(t)
	s.standbyStopMu.Lock()
	s.lastUserPlayStart = time.Now().Add(-20 * time.Second) // recall still active
	s.standbyStopMu.Unlock()
	// New key since the press (outside the trailing-frame epsilon) but NOT
	// adjacent to the flip: a volume tweak ~8s ago, flip now.
	s.SetUserActivityFn(func() time.Time { return time.Now().Add(-8 * time.Second) })

	s.HandleEnterStandby()

	if s.standbyStoppedRecently() || s.userStoppedRecently() {
		t.Fatal("a non-adjacent key during the recall must not arm the stop latches (#252)")
	}
	// Give a mistaken async clear a moment to surface before asserting.
	time.Sleep(150 * time.Millisecond)
	if rec.count() != 0 {
		t.Fatalf("a non-adjacent key during the recall must not clear the transport, got %v", rec.list())
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
	if !waitForAction(t, rec, "Stop") {
		t.Fatalf("without an activity signal the transport must still be cleared (#197), got %v", rec.list())
	}
}

// TestHandleEnterStandby_OwnPushWithNoKeySignalStaysConservative: on firmware
// that never emits userActivityUpdate, lastKey is the zero time and the
// own-push excusal's "push after key" comparison is vacuously true for EVERY
// push - and during a struggling recall STR pushes on a ~5s cadence, so a real
// power press was near-always within the flip window of some push. With no key
// signal the excusal must not fire: the drop keeps the documented conservative
// #197 handling (latch + clear), or the verify's wake powers the box back on.
func TestHandleEnterStandby_OwnPushWithNoKeySignalStaysConservative(t *testing.T) {
	s, rec := newPlayTestServer(t)
	s.SetUserActivityFn(func() time.Time { return time.Time{} })
	s.SetOwnTransportCmdFn(func() time.Time { return time.Now().Add(-2 * time.Second) })

	s.HandleEnterStandby()

	if !s.standbyStoppedRecently() || !s.userStoppedRecently() {
		t.Fatal("without a key signal an own push must not excuse the drop; the conservative #197 latching must win")
	}
	if !waitForAction(t, rec, "Stop") {
		t.Fatalf("without a key signal the transport must still be cleared (#197), got %v", rec.list())
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
	if !waitForAction(t, rec, "Stop") {
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

// TestHandleEnterStandby_FlipAfterOwnPushIsNotAPowerOff encodes the field
// signature that survived F4: power-ON key press, STR's own wake-resume push
// ~2s later, source flip 200ms after the push. The key press satisfied the
// adjacency check, so the flip latched "user stopped deliberately" and the
// very resume the key asked for was suppressed. A flip right after STR's own
// transport command - with no key press since that command - answers OUR push
// and must leave the latches and transport alone.
func TestHandleEnterStandby_FlipAfterOwnPushIsNotAPowerOff(t *testing.T) {
	s, rec := newPlayTestServer(t)
	now := time.Now()
	s.SetUserActivityFn(func() time.Time { return now.Add(-2400 * time.Millisecond) })
	s.SetOwnTransportCmdFn(func() time.Time { return now.Add(-200 * time.Millisecond) })

	s.HandleEnterStandby()

	if s.standbyStoppedRecently() || s.userStoppedRecently() {
		t.Fatal("a flip answering STR's own push must not arm the stop latches")
	}
	time.Sleep(150 * time.Millisecond)
	if rec.count() != 0 {
		t.Fatalf("a flip answering STR's own push must not clear the transport, got %v", rec.list())
	}
}

// TestHandleEnterStandby_KeyAfterOwnPushStillLatches guards #197 against the
// new excusal: a key press AFTER STR's last push is the user acting (the power
// key), so the conservative power-off handling must win.
func TestHandleEnterStandby_KeyAfterOwnPushStillLatches(t *testing.T) {
	s, rec := newPlayTestServer(t)
	s.standbyStopMu.Lock()
	s.lastUserPlayStart = time.Now().Add(-10 * time.Second) // recall still active
	s.standbyStopMu.Unlock()
	s.SetOwnTransportCmdFn(func() time.Time { return time.Now().Add(-2 * time.Second) })
	s.SetUserActivityFn(func() time.Time { return time.Now() }) // power press AFTER our push

	s.HandleEnterStandby()

	if !s.standbyStoppedRecently() || !s.userStoppedRecently() {
		t.Fatal("a key press after STR's own push is a real power-off and must latch (#197)")
	}
	if !waitForAction(t, rec, "Stop") {
		t.Fatalf("a real power-off must still clear the transport (#197), got %v", rec.list())
	}
}
