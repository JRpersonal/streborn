package webui

import (
	"testing"
	"time"
)

// The auto-re-push (box-dropped-the-stream recovery) and a recall's own verify
// used to push at the same time. A Wave diagnostic showed the result: switching
// preset tore down the previous stream, the disconnect armed the re-push, and
// four stream starts plus two re-pushes landed inside 1.7 s, 680 ms apart
// despite the 2 s backoff, because each push tore down the other's stream.

// TestUserPlayedRecently_OwnsRetryAfterAPress: right after a preset press or an
// app play, the recall owns the retry and the re-push must stand down.
func TestUserPlayedRecently_OwnsRetryAfterAPress(t *testing.T) {
	s := &Server{}
	if s.userPlayedRecently() {
		t.Fatal("no play recorded yet, want false")
	}

	s.NoteUserPlay()
	if !s.userPlayedRecently() {
		t.Fatal("a play the user just started must own the retry")
	}
}

// TestUserPlayedRecently_ExpiresSoTheDropRecoveryResumes: once the recall's
// verify has run its course, an ordinary mid-playback dropout must be resumed
// by the re-push again.
func TestUserPlayedRecently_ExpiresSoTheDropRecoveryResumes(t *testing.T) {
	s := &Server{}
	s.standbyStopMu.Lock()
	s.lastUserPlayStart = time.Now().Add(-(recallOwnsRetryWindow + time.Second))
	s.standbyStopMu.Unlock()

	if s.userPlayedRecently() {
		t.Fatal("past the window the drop recovery must take over again")
	}
}

// TestRecallWindowOutlivesTheVerifyStorm pins the window against the two things
// it has to span: the wrong-state re-push and the first verify ticks. If the
// verify's early pushes fell outside it, the collision this guard exists for
// would come straight back.
func TestRecallWindowOutlivesTheVerifyStorm(t *testing.T) {
	// cmd/agent: the wrong-state re-push fires at 1.5 s and the verify ticks
	// every 5 s. Two ticks plus the fast re-push must fit inside the window.
	const fastRePush = 1500 * time.Millisecond
	const twoVerifyTicks = 10 * time.Second
	if recallOwnsRetryWindow < fastRePush+time.Second {
		t.Fatalf("window %v must outlast the wrong-state re-push at %v", recallOwnsRetryWindow, fastRePush)
	}
	if recallOwnsRetryWindow < twoVerifyTicks {
		t.Fatalf("window %v must span the verify's first two ticks (%v)", recallOwnsRetryWindow, twoVerifyTicks)
	}
}
