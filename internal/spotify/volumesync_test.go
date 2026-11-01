package spotify

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// volRec records what the bridge sent to each side.
type volRec struct {
	mu     sync.Mutex
	engine []int
	box    []int
}

func (r *volRec) engineVals() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.engine...)
}
func (r *volRec) boxVals() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.box...)
}

func newSyncTestManager(boxVol int) (*Manager, *volRec) {
	r := &volRec{}
	m := &Manager{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	m.vs.engineSet = func(_ context.Context, pct int) error {
		r.mu.Lock()
		r.engine = append(r.engine, pct)
		r.mu.Unlock()
		return nil
	}
	m.vs.boxSet = func(_ context.Context, pct int) error {
		r.mu.Lock()
		r.box = append(r.box, pct)
		r.mu.Unlock()
		return nil
	}
	m.vs.boxGet = func(context.Context) (int, error) { return boxVol, nil }
	m.setEventsLive(true)
	return m, r
}

func waitVol(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// A hardware-key burst on the speaker reaches the engine as its final level,
// in a handful of pushes rather than one per frame.
func TestBoxVolumeBurstIsPushedToTheEngineCoalesced(t *testing.T) {
	m, r := newSyncTestManager(0)
	for v := 10; v <= 18; v++ {
		m.NoteBoxVolume(v)
	}
	if !waitVol(t, 2*time.Second, func() bool {
		e := r.engineVals()
		return len(e) > 0 && e[len(e)-1] == 18
	}) {
		t.Fatalf("the final speaker level must reach the engine, got %v", r.engineVals())
	}
	if e := r.engineVals(); len(e) > 2 {
		t.Fatalf("a 9-frame burst must coalesce to at most 2 pushes, got %v", e)
	}
	if len(r.boxVals()) != 0 {
		t.Fatalf("the box -> Spotify direction must never write the box, got %v", r.boxVals())
	}
}

// No engine up (events socket down): nothing to push to.
func TestBoxVolumeNotPushedWithoutEngine(t *testing.T) {
	m, r := newSyncTestManager(0)
	m.setEventsLive(false)
	m.NoteBoxVolume(12)
	time.Sleep(boxVolSettle + 150*time.Millisecond)
	if e := r.engineVals(); len(e) != 0 {
		t.Fatalf("no engine, no push; got %v", e)
	}
}

// The engine echoes each of STR's own pushes as a "volume" event. Writing that
// back to the speaker was the audible step down on every activation
// (2026-09-25, the seed's vol-1 nudge); a box-originated level must not be
// re-applied to the box either.
func TestOwnEnginePushEchoIsNotWrittenToTheBox(t *testing.T) {
	m, r := newSyncTestManager(20)
	m.seedEngineVolume(context.Background(), "session-ready")
	if e := r.engineVals(); len(e) != 1 || e[0] != 20 {
		t.Fatalf("session-ready must seed the speaker level 20, got %v", e)
	}
	m.handleEngineVolumeEvent(20, 100)
	m.NoteBoxVolume(23)
	waitVol(t, time.Second, func() bool { return len(r.engineVals()) == 2 })
	m.handleEngineVolumeEvent(23, 100)
	if b := r.boxVals(); len(b) != 0 {
		t.Fatalf("echoes of STR's own pushes must not reach the speaker, got %v", b)
	}
}

// The nudge on "active" (vol-1, vol) is marked as ours too.
func TestActiveNudgeEchoesAreNotWrittenToTheBox(t *testing.T) {
	m, r := newSyncTestManager(30)
	m.seedEngineVolume(context.Background(), "active")
	if e := r.engineVals(); len(e) != 3 || e[0] != 30 || e[1] != 29 || e[2] != 30 {
		t.Fatalf("an unconfirmed engine gets set + nudge on active, got %v", e)
	}
	m.handleEngineVolumeEvent(29, 100)
	m.handleEngineVolumeEvent(30, 100)
	if b := r.boxVals(); len(b) != 0 {
		t.Fatalf("the nudge must not reach the speaker, got %v", b)
	}
}

// When the engine already holds the speaker's level (the session-ready seed
// landed), "active" sends nothing: no nudge, no extra PutState.
func TestActiveSkipsWhenEngineAlreadyShowsTheSpeakerLevel(t *testing.T) {
	m, r := newSyncTestManager(7)
	m.seedEngineVolume(context.Background(), "session-ready")
	m.handleEngineVolumeEvent(7, 100) // the echo confirms the Connect volume
	m.seedEngineVolume(context.Background(), "active")
	if e := r.engineVals(); len(e) != 1 {
		t.Fatalf("active must not re-seed a confirmed engine, got %v", e)
	}
}

// A Spotify-app change goes to the speaker once, and the speaker's
// volumeUpdated for it is not bounced back into the engine.
func TestSpotifyChangeReachesTheBoxAndDoesNotBounceBack(t *testing.T) {
	m, r := newSyncTestManager(10)
	m.handleEngineVolumeEvent(35, 100)
	if b := r.boxVals(); len(b) != 1 || b[0] != 35 {
		t.Fatalf("the Spotify change must reach the speaker, got %v", b)
	}
	m.NoteBoxVolume(35)
	time.Sleep(boxVolSettle + 150*time.Millisecond)
	if e := r.engineVals(); len(e) != 0 {
		t.Fatalf("the speaker's echo must not go back to the engine, got %v", e)
	}
}

// Echo marks are value-matched: a real Spotify change to a DIFFERENT level
// right after STR pushed one still reaches the speaker.
func TestRealSpotifyChangeInsideTheEchoWindowIsApplied(t *testing.T) {
	m, r := newSyncTestManager(20)
	m.seedEngineVolume(context.Background(), "session-ready")
	m.handleEngineVolumeEvent(42, 100)
	if b := r.boxVals(); len(b) != 1 || b[0] != 42 {
		t.Fatalf("want 42 on the speaker, got %v", b)
	}
}

// The engine already holding the level means nothing to push.
func TestBoxLevelEqualToEngineIsNotRepushed(t *testing.T) {
	m, r := newSyncTestManager(0)
	m.handleEngineVolumeEvent(15, 100) // engine says 15 (also writes box 15)
	m.vs.mu.Lock()
	m.vs.toBox = nil // pretend the box echo window has passed
	m.vs.mu.Unlock()
	m.NoteBoxVolume(15)
	time.Sleep(boxVolSettle + 150*time.Millisecond)
	if e := r.engineVals(); len(e) != 0 {
		t.Fatalf("same level as the engine: no push, got %v", e)
	}
}

// The safety net: a 100 % from the app that is replaced at once by another
// level (the user dragging down from a slider that showed max) never reaches
// the speaker.
func TestMaxJumpReplacedAtOnceIsDropped(t *testing.T) {
	m, r := newSyncTestManager(10)
	m.seedEngineVolume(context.Background(), "session-ready") // box known: 10
	m.handleEngineVolumeEvent(100, 100)
	if b := r.boxVals(); len(b) != 0 {
		t.Fatalf("the max must be held, not applied at once, got %v", b)
	}
	m.handleEngineVolumeEvent(25, 100)
	time.Sleep(maxJumpHold + 200*time.Millisecond)
	b := r.boxVals()
	if len(b) != 1 || b[0] != 25 {
		t.Fatalf("only the chosen level may reach the speaker, got %v", b)
	}
	m.vs.mu.Lock()
	fired := m.vs.guardFired
	m.vs.mu.Unlock()
	if fired != 1 {
		t.Fatalf("guard count want 1, got %d", fired)
	}
}

// A lone 100 % is a real "all the way up": applied after the hold.
func TestLoneMaxIsAppliedAfterTheHold(t *testing.T) {
	m, r := newSyncTestManager(10)
	m.seedEngineVolume(context.Background(), "session-ready")
	m.handleEngineVolumeEvent(100, 100)
	if !waitVol(t, maxJumpHold+time.Second, func() bool {
		b := r.boxVals()
		return len(b) == 1 && b[0] == 100
	}) {
		t.Fatalf("a lone max must be applied after the hold, got %v", r.boxVals())
	}
}

// A small rise to 100 is not a jump: applied at once.
func TestSmallRiseToMaxIsNotHeld(t *testing.T) {
	m, r := newSyncTestManager(90)
	m.seedEngineVolume(context.Background(), "session-ready")
	m.handleEngineVolumeEvent(100, 100)
	if b := r.boxVals(); len(b) != 1 || b[0] != 100 {
		t.Fatalf("90 -> 100 is not a jump, got %v", b)
	}
}

// A new session starts at go-librespot's 100 % again, so the confirmed level is
// forgotten and the next "active" re-seeds.
func TestSessionResetForgetsTheEngineLevel(t *testing.T) {
	m, r := newSyncTestManager(7)
	m.handleEngineVolumeEvent(7, 100)
	m.noteEngineSessionReset()
	m.seedEngineVolume(context.Background(), "session-ready")
	if e := r.engineVals(); len(e) != 1 || e[0] != 7 {
		t.Fatalf("after a session reset the seed must go out, got %v", e)
	}
}

// The Spotify app cannot turn the speaker past the ceiling set in STR: the
// speaker gets the ceiling, and the app's slider is moved back to it so it
// shows the level the speaker really plays at. Below the ceiling nothing
// changes.
func TestSpotifyVolumeStopsAtTheSpeakerCeiling(t *testing.T) {
	m, r := newSyncTestManager(20)
	m.SetVolumeCapFn(func() (int, bool) { return 40, true })

	m.handleEngineVolumeEvent(30, 100)
	if got := r.boxVals(); len(got) != 1 || got[0] != 30 {
		t.Fatalf("below the ceiling the level must pass untouched, box got %v", got)
	}
	if got := r.engineVals(); len(got) != 0 {
		t.Fatalf("below the ceiling the slider must not be moved, engine got %v", got)
	}

	m.handleEngineVolumeEvent(70, 100)
	if got := r.boxVals(); len(got) != 2 || got[1] != 40 {
		t.Errorf("above the ceiling the speaker must get the ceiling, box got %v", got)
	}
	if got := r.engineVals(); len(got) != 1 || got[0] != 40 {
		t.Errorf("the Spotify slider must be moved back to the ceiling, engine got %v", got)
	}

	// The engine echoes that push as a volume event; it must not be written
	// to the speaker again.
	m.handleEngineVolumeEvent(40, 100)
	if got := r.boxVals(); len(got) != 2 {
		t.Errorf("the echo of the slider push must not reach the speaker, box got %v", got)
	}
}
