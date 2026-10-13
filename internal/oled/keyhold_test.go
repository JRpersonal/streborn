package oled

import (
	"testing"
	"time"
)

// frameStep is how often the fake session panel looks at the session.
const frameStep = 50 * time.Millisecond

// sessionRig records what a held-key session put on the fake panel and when,
// relative to the session's start.
type sessionRig struct {
	*fakeQueue
	kinds    []NoticeKind
	at       []time.Duration
	end      time.Duration
	sessions int
	acquires int
	// tick runs on every frame with the time since the session started.
	tick func(d time.Duration)
}

func newSessionRig() *sessionRig {
	r := &sessionRig{fakeQueue: newFakeQueue()}
	acq := r.acquire
	r.acquire = func() bool {
		ok := acq()
		if ok {
			r.acquires++
		}
		return ok
	}
	r.showSession = func(s *heldSession, _ <-chan struct{}) error {
		r.sessions++
		start := r.now()
		for {
			n, _, ok := r.sessionFrame(s)
			d := r.now().Sub(start)
			if !ok {
				r.end = d
				return nil
			}
			if len(r.kinds) == 0 || r.kinds[len(r.kinds)-1] != n.Kind {
				r.kinds = append(r.kinds, n.Kind)
				r.at = append(r.at, d)
			}
			if r.tick != nil {
				r.tick(d)
			}
			r.sleep(frameStep)
		}
	}
	return r
}

// once runs f the first time the session clock reaches d.
func once(d time.Duration, f func()) func(time.Duration) {
	done := false
	return func(now time.Duration) {
		if !done && now >= d {
			done = true
			f()
		}
	}
}

func ticks(fs ...func(time.Duration)) func(time.Duration) {
	return func(d time.Duration) {
		for _, f := range fs {
			f(d)
		}
	}
}

func near(got, want time.Duration) bool {
	return got >= want && got <= want+2*frameStep
}

// TestKeyHoldOutcomeReplacesInPlace: the field case. The held screen is on
// the panel 1.8 s after the press, the "unchanged" confirmation comes 0.6 s
// later and replaces it in the same panel hold, no start gap, and the session
// lasts until the firmware's hold message is gone.
func TestKeyHoldOutcomeReplacesInPlace(t *testing.T) {
	r := newSessionRig()
	r.tick = once(600*time.Millisecond, func() { r.post(SavedByKey(6, "Jens Chill")) })
	r.holdStart(6, r.now().Add(-heldScreenAfterForTest))
	r.drain()
	if r.sessions != 1 || r.acquires != 1 {
		t.Fatalf("sessions %d, panel takes %d, want one each", r.sessions, r.acquires)
	}
	if len(r.kinds) != 2 || r.kinds[0] != NoticeKeyHeld || r.kinds[1] != NoticeSavedByKey {
		t.Fatalf("panel showed %v, want key-held then saved-key", r.kinds)
	}
	if !near(r.at[1], 600*time.Millisecond) {
		t.Fatalf("outcome on the panel %v into the session, want right at 600ms", r.at[1])
	}
	if !near(r.end, heldMinSession) {
		t.Fatalf("session ended after %v, want the minimum %v", r.end, heldMinSession)
	}
	if len(r.shown) != 0 {
		t.Fatalf("the outcome also showed as a separate notice: %v", r.kinds)
	}
}

// heldScreenAfterForTest mirrors the agent's start point (cmd/agent
// heldScreenAfter); only the press-relative log fields use it.
const heldScreenAfterForTest = 1800 * time.Millisecond

// TestKeyHoldLongSaveHoldsOutcome: a real write answers late; the outcome
// keeps its full hold time from its own start.
func TestKeyHoldLongSaveHoldsOutcome(t *testing.T) {
	r := newSessionRig()
	r.tick = once(2800*time.Millisecond, func() { r.post(SavedByKey(6, "X")) })
	r.holdStart(6, r.now())
	r.drain()
	if want := 2800*time.Millisecond + SavedByKey(6, "X").Hold; !near(r.end, want) {
		t.Fatalf("session ended after %v, want %v", r.end, want)
	}
}

// TestKeyHoldReleaseLinger: the firmware also reacts at the release, so the
// panel stays heldReleaseLinger past it, but never beyond heldMaxSession.
func TestKeyHoldReleaseLinger(t *testing.T) {
	r := newSessionRig()
	r.tick = ticks(
		once(500*time.Millisecond, func() { r.post(SavedByKey(2, "X")) }),
		once(3500*time.Millisecond, func() { r.holdReleased(2, r.now()) }),
	)
	r.holdStart(2, r.now())
	r.drain()
	if want := 3500*time.Millisecond + heldReleaseLinger; !near(r.end, want) {
		t.Fatalf("session ended after %v, want %v", r.end, want)
	}

	// A second outcome (the keeper after holdsave) restarts the outcome's
	// hold; a release late in that still ends at the cap.
	r = newSessionRig()
	r.tick = ticks(
		once(5*time.Second, func() { r.post(SavedByKey(2, "X")) }),
		once(7*time.Second, func() { r.post(NotSaved(2)) }),
		once(9500*time.Millisecond, func() { r.holdReleased(2, r.now()) }),
	)
	r.holdStart(2, r.now())
	r.drain()
	if !near(r.end, heldMaxSession) {
		t.Fatalf("session ended after %v, want the cap %v", r.end, heldMaxSession)
	}
}

// TestKeyHoldNoOutcome: nothing answers, the screen ends after
// heldOutcomeWait, and a late outcome shows as an ordinary notice.
func TestKeyHoldNoOutcome(t *testing.T) {
	r := newSessionRig()
	r.holdStart(3, r.now())
	r.drain()
	if !near(r.end, heldOutcomeWait) || len(r.kinds) != 1 {
		t.Fatalf("ended after %v showing %v, want key-held only for %v", r.end, r.kinds, heldOutcomeWait)
	}
	r.post(NotSaved(3))
	r.drain()
	if len(r.shown) != 1 || r.shown[0].Kind != NoticeNotSaved {
		t.Fatalf("late outcome not shown on its own: %v", r.fakeQueue.kinds())
	}
}

// TestKeyHoldCancel: a cancel ends the screen at once, but not one an outcome
// already reached.
func TestKeyHoldCancel(t *testing.T) {
	r := newSessionRig()
	r.tick = once(300*time.Millisecond, func() { r.holdCancel(4, "a tap after all") })
	r.holdStart(4, r.now())
	r.drain()
	if !near(r.end, 300*time.Millisecond) {
		t.Fatalf("cancelled screen ended after %v", r.end)
	}

	r = newSessionRig()
	r.tick = ticks(
		once(300*time.Millisecond, func() { r.post(EmptyKey(4)) }),
		once(600*time.Millisecond, func() { r.holdCancel(4, "no hold verdict") }),
	)
	r.holdStart(4, r.now())
	r.drain()
	if len(r.kinds) != 2 || r.kinds[1] != NoticeEmptyKey || !near(r.end, heldMinSession) {
		t.Fatalf("outcome cut short by a cancel: %v, ended after %v", r.kinds, r.end)
	}

	// Cancelled before the worker got to it: the panel is never taken.
	r = newSessionRig()
	r.holdStart(4, r.now())
	r.holdCancel(4, "a tap after all")
	r.drain()
	if r.sessions != 0 || r.acquires != 0 {
		t.Fatal("a cancelled session took the panel")
	}
}

// TestKeyHoldOnlyTakesItsOutcome: an unrelated notice waits for its turn, an
// "already on key M" belongs to the held key whatever M is.
func TestKeyHoldOnlyTakesItsOutcome(t *testing.T) {
	r := newSessionRig()
	r.tick = ticks(
		once(200*time.Millisecond, func() { r.post(SavedFromApp(1)) }),
		once(400*time.Millisecond, func() { r.post(SavedByKey(5, "other key")) }),
		once(600*time.Millisecond, func() { r.post(AlreadyOnKey(2)) }),
	)
	r.holdStart(6, r.now())
	r.drain()
	if len(r.kinds) != 2 || r.kinds[1] != NoticeAlreadyOnKey {
		t.Fatalf("session showed %v", r.kinds)
	}
	if len(r.shown) != 1 || r.shown[0].Key != 5 {
		t.Fatalf("after the session want the newest unrelated notice (key 5), got %+v", r.shown)
	}
}

// TestKeyHoldSkipsTheGap: a notice that just ended does not delay the held
// screen by the start gap.
func TestKeyHoldSkipsTheGap(t *testing.T) {
	r := newSessionRig()
	r.post(EmptyKey(1))
	r.drain() // shows 2.5 s, the gap would run another 0.5 s
	before := r.now()
	var startedAfter time.Duration = -1
	r.tick = once(0, func() { startedAfter = r.now().Sub(before) })
	r.holdStart(1, r.now())
	r.drain()
	if startedAfter != 0 {
		t.Fatalf("held screen waited %v for the start gap", startedAfter)
	}
}

// TestKeyHoldPreemptsNotice: a running ordinary notice gives way to the held
// screen at once and is not shown again.
func TestKeyHoldPreemptsNotice(t *testing.T) {
	r := newSessionRig()
	var quit <-chan struct{}
	r.show = func(n Notice, q <-chan struct{}) error {
		r.shown = append(r.shown, n)
		quit = q
		r.holdStart(3, r.now())
		return nil
	}
	r.post(SavedFromApp(1))
	r.drain()
	select {
	case <-quit:
	default:
		t.Fatal("the running notice was not asked to end")
	}
	if r.sessions != 1 || len(r.shown) != 1 {
		t.Fatalf("sessions %d, notices %d", r.sessions, len(r.shown))
	}
}

// TestKeyHoldOffOrUnsupported: no panel session, and an outcome posted
// meanwhile goes the ordinary way.
func TestKeyHoldOffOrUnsupported(t *testing.T) {
	r := newSessionRig()
	r.supported = func() bool { return false }
	r.holdStart(1, r.now())
	r.drain()
	if r.sessions != 0 {
		t.Fatal("session on an unsupported panel")
	}
	r = newSessionRig()
	r.enabled = func() bool { return false }
	r.holdStart(1, r.now())
	r.post(SavedByKey(1, "X")) // attached while the session waits
	r.enabled = func() bool { return true }
	r.supported = func() bool { return false }
	r.drain()
	if r.sessions != 0 || r.held != nil {
		t.Fatal("session ran or stayed registered")
	}
}

// TestKeyHoldRenders: the held screen draws the key, its busy badge, the
// lines, and the dots move.
func TestKeyHoldRenders(t *testing.T) {
	n := KeyHeld(6)
	if n.Line1 != "KEY 6" || n.Line2 != "SAVING..." {
		t.Fatalf("lines %q %q", n.Line1, n.Line2)
	}
	r := newNoticeRenderer(n)
	a, b := make([]byte, FrameSize), make([]byte, FrameSize)
	r.frame(0.5, a)
	r.frame(0.85, b)
	if levelAt(a, badgeCX+6, badgeCY) != 15 {
		t.Error("badge disc not lit")
	}
	if string(a) == string(b) {
		t.Error("busy dots do not move")
	}
	if litIn(a, 0, line1Y, Width, 14) < 40 || litIn(a, 0, line2Y, Width, 7) < 15 {
		t.Error("lines not drawn")
	}
}
