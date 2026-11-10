package main

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

// loopRig is a reconciler plus the speaker it believes in: what the box
// reports, what the pass did, how long the loop slept.
type loopRig struct {
	r *reconciler

	src             string
	playing, paused bool
	sourceReads     int // now_playing reads through the source-only seam

	passes    []bool // forceFull of every pass that ran
	passReady bool

	missing   []int
	listErr   error
	listReads int

	slept   []time.Duration
	onSleep func(n int) // called with the 1-based count of the sleep that is starting
}

func newLoopRig(t *testing.T) *loopRig {
	t.Helper()
	rig := &loopRig{r: newTestReconciler(t), src: "UPNP", passReady: true}
	r := rig.r
	r.sleep = func(d time.Duration) {
		rig.slept = append(rig.slept, d)
		if rig.onSleep != nil {
			rig.onSleep(len(rig.slept))
		}
	}
	r.nowPlayingSource = func(string) string { rig.sourceReads++; return rig.src }
	r.isPlaying = func(string) bool { return rig.playing }
	r.sourcePlayState = func(string) (string, bool, bool, bool) {
		return rig.src, rig.playing, rig.paused, rig.src != ""
	}
	r.keysMissing = func() ([]int, error) { rig.listReads++; return rig.missing, rig.listErr }
	r.pass = func(force bool) bool { rig.passes = append(rig.passes, force); return rig.passReady }
	return rig
}

// steady puts the loop where it is on a speaker that has been up for a while:
// the first full pass is long done.
func (rig *loopRig) steady() *loopRig {
	rig.r.fullDone, rig.r.everFullDone = true, true
	return rig
}

func (rig *loopRig) sleptTotal() time.Duration {
	var d time.Duration
	for _, s := range rig.slept {
		d += s
	}
	return d
}

// The boot force is the one pass that is not gated on the box being awake: a
// just-booted box legitimately reads STANDBY before the first press, and
// holding there is the first-press-after-reboot regression (#4).
func TestStepBootForceRunsIntoAStandbyReading(t *testing.T) {
	t.Parallel()
	rig := newLoopRig(t)
	rig.src = "STANDBY"
	rig.r.step()
	if !reflect.DeepEqual(rig.passes, []bool{true}) {
		t.Fatalf("passes = %v, want one forced pass", rig.passes)
	}
	if rig.sourceReads != 0 {
		t.Errorf("the boot force probed the box %d times before writing; it is not gated", rig.sourceReads)
	}
	if !rig.r.fullDone || !rig.r.everFullDone {
		t.Error("a ready pass must end the boot window")
	}
	if got := rig.sleptTotal(); got != 5*time.Minute {
		t.Errorf("after a ready pass the loop waits the maintenance cadence, slept %v", got)
	}
}

// A pass that did not come back ready keeps the fast cadence.
func TestStepNotReadyKeepsTheFastRetry(t *testing.T) {
	t.Parallel()
	rig := newLoopRig(t)
	rig.passReady = false
	rig.r.step()
	if rig.r.fullDone || rig.r.everFullDone {
		t.Fatal("a pass that was not ready must leave the boot window open")
	}
	if !reflect.DeepEqual(rig.slept, []time.Duration{10 * time.Second}) {
		t.Errorf("slept %v, want one 10 s retry", rig.slept)
	}
}

// The spin fixed on 2026-09-07, as behaviour instead of a source pin: a retry
// force on a sleeping box runs no pass, drops the pending ask through the
// standby deferral, and waits a whole maintenance tick.
func TestStepRetryForceOnASleepingBoxIsHeldAndItsAskDeferred(t *testing.T) {
	t.Parallel()
	for _, src := range []string{"STANDBY", ""} {
		rig := newLoopRig(t)
		r := rig.r
		r.everFullDone = true // fullDone false: the last pass failed
		r.forceHeldSince = time.Now().Add(-time.Hour)
		rig.src = src
		r.asks.request(nil, "thumb")

		r.step()

		if len(rig.passes) != 0 {
			t.Fatalf("src=%q: a pass ran into the sleeping box: %v", src, rig.passes)
		}
		if r.asks.ask.Load() {
			t.Errorf("src=%q: the ask stayed armed, which is what made the wait exit at once and the loop spin", src)
		}
		if r.asks.last.Load() != 0 || r.asks.keysDead.Load() {
			t.Errorf("src=%q: the deferral must reset the budget and drop the dead-key evidence", src)
		}
		if !r.forceHeldSince.IsZero() {
			t.Errorf("src=%q: time asleep must not count towards the hold ceiling (#1322)", src)
		}
		if got := rig.sleptTotal(); got != 5*time.Minute {
			t.Errorf("src=%q: slept %v, want a full maintenance tick", src, got)
		}
	}
}

// ... and the wait has a floor of one tick even when an asker re-arms straight
// after the deferral reset its budget.
func TestStepRetryHoldSleepsOneTickEvenWhenAnAskRearmsAtOnce(t *testing.T) {
	t.Parallel()
	rig := newLoopRig(t)
	r := rig.r
	r.everFullDone = true
	rig.src = "STANDBY"
	r.asks.request(nil, "thumb")
	rig.onSleep = func(n int) {
		if n == 1 {
			r.asks.request(nil, "standby-exit") // the wake re-asks during the floor
		}
	}

	r.step()

	if !reflect.DeepEqual(rig.slept, []time.Duration{10 * time.Second}) {
		t.Fatalf("slept %v, want exactly the 10 s floor and then an early wake", rig.slept)
	}
	if !r.asks.ask.Load() {
		t.Error("the wake's own ask must survive the hold")
	}
	if len(rig.passes) != 0 {
		t.Errorf("no pass may run in the held iteration, got %v", rig.passes)
	}
}

// Repeated dead-key presses grant one execution despite a STANDBY reading,
// on the retry path too.
func TestStepRetryForceRunsAStandbyOKAsk(t *testing.T) {
	t.Parallel()
	rig := newLoopRig(t)
	r := rig.r
	r.everFullDone = true
	rig.src = "STANDBY"
	r.asks.request(nil, "thumb-repeat")
	r.asks.grantStandbyOK()

	r.step()

	if !reflect.DeepEqual(rig.passes, []bool{true}) {
		t.Fatalf("passes = %v, want the forced pass to run", rig.passes)
	}
	if r.asks.ask.Load() || r.asks.standbyOK.Load() || r.asks.keysDead.Load() {
		t.Error("the ask, its grant and its evidence are consumed by the pass that ran")
	}
	if r.forceKeysDead {
		t.Error("a pass that ran must clear its dead-key evidence")
	}
}

// An ask on a sleeping box in steady state is dropped, not forced: the pass
// that runs is the ordinary missing-only one (#119).
func TestStepAskOnASleepingBoxIsDeferredAndThePassStaysOrdinary(t *testing.T) {
	t.Parallel()
	rig := newLoopRig(t).steady()
	r := rig.r
	rig.src = "STANDBY"
	r.asks.requestUrgent(nil, "1036")

	r.step()

	if !reflect.DeepEqual(rig.passes, []bool{false}) {
		t.Fatalf("passes = %v, want one ordinary pass", rig.passes)
	}
	if r.asks.ask.Load() || r.asks.urgentLast.Load() != 0 || r.asks.keysDead.Load() {
		t.Error("the deferred ask must be gone with its budget and its evidence")
	}
}

// An ask on an awake, idle box forces the pass and keeps its rate-limit stamp.
func TestStepAskOnAnAwakeBoxForcesThePass(t *testing.T) {
	t.Parallel()
	rig := newLoopRig(t).steady()
	r := rig.r
	r.asks.request(nil, "reconnect")
	stamp := r.asks.last.Load()

	r.step()

	if !reflect.DeepEqual(rig.passes, []bool{true}) {
		t.Fatalf("passes = %v, want one forced pass", rig.passes)
	}
	if r.asks.last.Load() != stamp {
		t.Error("the awake path must put the consumed ask's rate-limit stamp back")
	}
}

// The recheck an account read asks for is consumed by the pass and never
// forces it.
func TestStepRecheckIsConsumedByAnOrdinaryPass(t *testing.T) {
	t.Parallel()
	rig := newLoopRig(t).steady()
	rig.r.recheck.Store(true)
	rig.r.step()
	if !reflect.DeepEqual(rig.passes, []bool{false}) {
		t.Fatalf("passes = %v, want one ordinary pass", rig.passes)
	}
	if rig.r.recheck.Load() {
		t.Error("the pass is the look the recheck asked for and must consume it")
	}
}

// A forced pass waits for STR's own playback to stop, carries itself across
// iterations (its ask is already consumed), and runs when the music ends.
func TestStepForcedPassIsHeldOnPlaybackAndRunsWhenItStops(t *testing.T) {
	t.Parallel()
	rig := newLoopRig(t).steady()
	r := rig.r
	rig.playing = true
	r.asks.request(nil, "reconnect")

	r.step()
	if len(rig.passes) != 0 {
		t.Fatalf("the forced pass wrote into a playing speaker: %v", rig.passes)
	}
	if !r.forceHeld || r.forceHeldSince.IsZero() {
		t.Fatal("the held pass must be carried to the next iteration, with the ceiling anchored")
	}
	if !reflect.DeepEqual(rig.slept, []time.Duration{forcedPlayHoldRetry}) {
		t.Fatalf("slept %v, want one hold retry", rig.slept)
	}
	anchor := r.forceHeldSince
	r.step()
	if len(rig.passes) != 0 || !r.forceHeldSince.Equal(anchor) {
		t.Fatal("a second held iteration must neither write nor move the ceiling's anchor")
	}

	rig.playing = false
	r.step()
	if !reflect.DeepEqual(rig.passes, []bool{true}) {
		t.Fatalf("passes = %v, want the held pass to run forced once the music stopped", rig.passes)
	}
	if r.forceHeld || !r.forceHeldSince.IsZero() {
		t.Error("a pass that ran releases the hold")
	}
}

// A paused play holds the pass like a running one.
func TestStepForcedPassIsHeldOnAPausedPlay(t *testing.T) {
	t.Parallel()
	rig := newLoopRig(t).steady()
	rig.paused = true
	rig.r.asks.request(nil, "reconnect")
	rig.r.step()
	if len(rig.passes) != 0 || !rig.r.forceHeld {
		t.Fatalf("the forced pass must wait on a paused play (passes %v, held %v)", rig.passes, rig.r.forceHeld)
	}
}

// At the hold ceiling the pass only writes into the music when a key is
// actually missing, or when its asker saw dead keys the list cannot show.
func TestStepHoldCeiling(t *testing.T) {
	t.Parallel()
	atCeiling := func(t *testing.T) *loopRig {
		rig := newLoopRig(t).steady()
		rig.playing = true
		rig.r.forceHeld = true
		rig.r.forceHeldSince = time.Now().Add(-forcedPlayHoldCeiling - time.Minute)
		return rig
	}

	t.Run("every key listed keeps waiting", func(t *testing.T) {
		t.Parallel()
		rig := atCeiling(t)
		rig.r.step()
		if len(rig.passes) != 0 {
			t.Fatalf("a speaker that lists every key was written into at the ceiling: %v", rig.passes)
		}
		if rig.listReads != 1 {
			t.Errorf("the list is read once per ceiling, got %d reads", rig.listReads)
		}
		if !rig.r.forceHeld || time.Since(rig.r.forceHeldSince) > time.Minute {
			t.Error("the hold must start over from now")
		}
	})
	t.Run("unreadable list keeps waiting", func(t *testing.T) {
		t.Parallel()
		rig := atCeiling(t)
		rig.listErr = errors.New("presets unreadable")
		rig.r.step()
		if len(rig.passes) != 0 {
			t.Fatalf("an unreadable list must not release the pass: %v", rig.passes)
		}
	})
	t.Run("a missing key runs the pass", func(t *testing.T) {
		t.Parallel()
		rig := atCeiling(t)
		rig.missing = []int{2}
		rig.r.step()
		if !reflect.DeepEqual(rig.passes, []bool{true}) {
			t.Fatalf("passes = %v, want the forced pass", rig.passes)
		}
	})
	t.Run("dead-key evidence runs the pass without reading the list", func(t *testing.T) {
		t.Parallel()
		rig := atCeiling(t)
		rig.r.forceKeysDead = true
		rig.r.step()
		if !reflect.DeepEqual(rig.passes, []bool{true}) || rig.listReads != 0 {
			t.Fatalf("passes = %v, list reads = %d; want the pass, and no list read", rig.passes, rig.listReads)
		}
		if rig.r.forceKeysDead {
			t.Error("the pass that ran clears the evidence")
		}
	})
	t.Run("a dead-key ask that arrived during the hold counts", func(t *testing.T) {
		t.Parallel()
		rig := atCeiling(t)
		rig.r.asks.request(nil, "thumb")
		rig.r.step()
		if !reflect.DeepEqual(rig.passes, []bool{true}) || rig.listReads != 0 {
			t.Fatalf("passes = %v, list reads = %d; want the pass, and no list read", rig.passes, rig.listReads)
		}
		if rig.r.asks.ask.Load() {
			t.Error("the pass that ran answered the ask")
		}
	})
}

// A source the listener chose holds the pass with no ceiling at all (#1035),
// and the pass runs the moment the speaker leaves it.
func TestStepForcedPassWaitsOutAListenersSourceWithoutACeiling(t *testing.T) {
	t.Parallel()
	rig := newLoopRig(t).steady()
	r := rig.r
	rig.src = "BLUETOOTH"
	r.forceHeld = true
	r.forceHeldSince = time.Now().Add(-time.Hour)

	r.step()
	if len(rig.passes) != 0 || rig.listReads != 0 {
		t.Fatalf("the pass must stay held on BLUETOOTH however long it has waited (passes %v, list reads %d)", rig.passes, rig.listReads)
	}
	if !r.forceHeldSince.IsZero() {
		t.Error("time on the listener's source must not count towards the playback ceiling")
	}
	if r.userHeldSrc != "BLUETOOTH" {
		t.Errorf("userHeldSrc = %q", r.userHeldSrc)
	}

	// Back on STR's own idle source. (Had it gone to STANDBY instead, the
	// retry gate would keep the pass held until the wake.)
	rig.src = "UPNP"
	r.step()
	if !reflect.DeepEqual(rig.passes, []bool{true}) || r.userHeldSrc != "" || r.forceHeld {
		t.Fatalf("leaving the source must run the held pass (passes %v)", rig.passes)
	}
}

// A speaker in setup without a network holds the pass; with a network for the
// whole setup ceiling the pass runs anyway.
func TestStepForcedPassAndSetup(t *testing.T) {
	t.Parallel()
	rig := newLoopRig(t)
	r := rig.r
	rig.src = "SETUP"
	r.defaultGateway = func() string { return "" }

	r.step()
	if len(rig.passes) != 0 || !r.forceHeld || r.setupHeldSrc != "SETUP" || !r.setupNetSince.IsZero() {
		t.Fatalf("a speaker in setup with no gateway must hold the first pass too (passes %v)", rig.passes)
	}

	r.defaultGateway = func() string { return "192.0.2.254" }
	r.step()
	if len(rig.passes) != 0 || r.setupNetSince.IsZero() {
		t.Fatal("on the network, the setup ceiling starts counting and the pass still waits")
	}

	r.setupNetSince = time.Now().Add(-forcedSetupHoldCeiling - time.Minute)
	r.step()
	if !reflect.DeepEqual(rig.passes, []bool{true}) {
		t.Fatalf("passes = %v, want the pass to run after the setup ceiling", rig.passes)
	}
}

// A play the user just asked for holds the pass on a source that looks free.
func TestStepForcedPassIsHeldWhileAPlayIsStarting(t *testing.T) {
	t.Parallel()
	rig := newLoopRig(t).steady()
	r := rig.r
	rig.src = "INVALID_SOURCE"
	r.setLastUserPlayStart(func() time.Time { return time.Now().Add(-5 * time.Second) })
	r.asks.request(nil, "standby-exit")

	r.step()
	if len(rig.passes) != 0 || !r.forceHeld {
		t.Fatalf("the pass must wait for the starting play (passes %v, held %v)", rig.passes, r.forceHeld)
	}
	if !reflect.DeepEqual(rig.slept, []time.Duration{userPlayStartHoldRetry}) {
		t.Errorf("slept %v, want the short start-hold retry", rig.slept)
	}

	r.setLastUserPlayStart(func() time.Time { return time.Now().Add(-userPlayStartWindow - time.Second) })
	r.step()
	if !reflect.DeepEqual(rig.passes, []bool{true}) {
		t.Fatalf("passes = %v, want the pass once the start window is over", rig.passes)
	}
}

// A live hardware recall is waited out, bounded, and the pass runs after it.
func TestStepForcedPassWaitsOutALiveRecall(t *testing.T) {
	t.Parallel()
	rig := newLoopRig(t)
	checks := 0
	rig.r.recallActiveWithin = func(time.Duration) bool { checks++; return checks <= 3 }
	rig.passReady = false
	rig.r.step()
	want := []time.Duration{2 * time.Second, 2 * time.Second, 2 * time.Second, 10 * time.Second}
	if !reflect.DeepEqual(rig.slept, want) {
		t.Fatalf("slept %v, want three recall waits and then the pass's own retry", rig.slept)
	}
	if !reflect.DeepEqual(rig.passes, []bool{true}) {
		t.Fatalf("passes = %v", rig.passes)
	}

	// A recall that never ends does not hold the pass for ever.
	rig = newLoopRig(t)
	rig.r.recallActiveWithin = func(time.Duration) bool { return true }
	rig.passReady = false
	rig.r.step()
	if got := rig.sleptTotal(); got != 60*time.Second+10*time.Second {
		t.Fatalf("slept %v, want the 60 s bound and then the pass's own retry", got)
	}
}

// The boot-window force survives a group-wake hold: the speaker was woken for
// a group, the pass waits one retry, and it is still forced afterwards.
func TestStepGroupWakeHoldsTheBootForce(t *testing.T) {
	t.Parallel()
	rig := newLoopRig(t)
	r := rig.r
	waking := true
	r.setQuietWakeEpisodeActive(func() bool { return waking })

	r.step()
	if len(rig.passes) != 0 {
		t.Fatalf("the pass wrote into a group wake (#900): %v", rig.passes)
	}
	if !reflect.DeepEqual(rig.slept, []time.Duration{forcedPlayHoldRetry}) {
		t.Fatalf("slept %v, want one hold retry", rig.slept)
	}
	waking = false
	r.step()
	if !reflect.DeepEqual(rig.passes, []bool{true}) {
		t.Fatalf("passes = %v, want the forced pass after the group wake settled", rig.passes)
	}
}

// What the loop does today when an ASK meets the group-wake hold in steady
// state, pinned as found, not as wanted: the ask was consumed before the
// hold, the hold does not carry the pass over (it is the one hold that does
// not set forceHeld), so the pass after the wake is an ordinary one and the
// forced re-sync the ask wanted does not happen. The comment on the hold
// promises the opposite ("the pass runs straight after with the ask still
// pending"). The refactoring keeps the behaviour; whether to change it is a
// separate decision, because the forced pass writes to the speaker.
func TestStepGroupWakeHoldDropsAConsumedAsk(t *testing.T) {
	t.Parallel()
	rig := newLoopRig(t).steady()
	r := rig.r
	waking := true
	r.setQuietWakeEpisodeActive(func() bool { return waking })
	r.asks.request(nil, "standby-exit")

	r.step()
	if len(rig.passes) != 0 {
		t.Fatalf("the pass wrote into a group wake (#900): %v", rig.passes)
	}
	if r.asks.ask.Load() || r.forceHeld {
		t.Fatal("this test pins the present behaviour: ask consumed, pass not carried over")
	}
	waking = false
	r.step()
	if !reflect.DeepEqual(rig.passes, []bool{false}) {
		t.Fatalf("passes = %v; the present behaviour is one ordinary pass", rig.passes)
	}
}

// The 20-minute dead-key insurance runs on an awake, idle box on STR's own
// source and nowhere else.
func TestStepAwakeInsurance(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		src     string
		playing bool
		force   bool
	}{
		{"idle on STR's source", "UPNP", false, true},
		{"playing", "UPNP", true, false},
		{"listener's source", "BLUETOOTH", false, false},
		{"standby", "STANDBY", false, false},
		{"unreadable", "", false, false},
	}
	for _, c := range cases {
		rig := newLoopRig(t).steady()
		r := rig.r
		rig.src, rig.playing = c.src, c.playing
		due := time.Now().Add(-21 * time.Minute)
		r.lastAwakeForce = due
		r.step()
		if !reflect.DeepEqual(rig.passes, []bool{c.force}) {
			t.Errorf("%s: passes = %v, want [%v]", c.name, rig.passes, c.force)
		}
		if !r.lastAwakeForce.After(due) {
			t.Errorf("%s: the insurance must be stamped even when skipped, or it re-checks every tick", c.name)
		}
	}

	// Not due: no probe at all.
	rig := newLoopRig(t).steady()
	rig.r.step()
	if rig.sourceReads != 0 {
		t.Errorf("an insurance check that is not due read now_playing %d times", rig.sourceReads)
	}
}

// The loop runs while the agent is still being wired and while the gabbo
// handler asks: the late seams are bound, asks arrive and the initial sync
// reads the full-write stamp, all against a stepping loop. Run with -race.
func TestReconcilerIsRaceFreeAgainstItsAskersAndLateBinding(t *testing.T) {
	t.Parallel()
	r := newTestReconciler(t)
	r.pass = func(force bool) bool {
		if force {
			r.fullPresetWriteAt.Store(time.Now().Unix())
		}
		// The pass asks too (missing-slots-held, refused-rewrite).
		r.asks.request(nil, "missing-slots-held")
		_, _ = r.presetWriteGroupFollower()
		return true
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // the run goroutine
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				r.step()
			}
		}
	}()
	others := []func(i int){
		func(i int) { // buildWebui binding the seams
			r.setQuietWakeEpisodeActive(func() bool { return i%7 == 0 })
			r.setLastUserPlayStart(func() time.Time { return time.Now().Add(-time.Duration(i) * time.Second) })
			r.setGroupFollower(func() (bool, string) { return i%3 == 0, "stereo pair follower" })
			if i%10 == 9 {
				r.setQuietWakeEpisodeActive(nil)
				r.setLastUserPlayStart(nil)
				r.setGroupFollower(nil)
			}
		},
		func(i int) { // the gabbo handler and autopair
			r.asks.request(nil, "thumb")
			r.asks.requestUrgent(nil, "1036")
			if r.asks.standbyThumbRepeat(int64(i)) {
				r.asks.grantStandbyOK()
			}
			r.asks.last.Store(0)
			r.asks.urgentLast.Store(0)
		},
		func(int) { r.initialSyncShouldStandDown() }, // the initial sync
		func(i int) { r.recheck.Store(i%2 == 0) },    // an account read
	}
	for _, f := range others {
		wg.Add(1)
		go func(f func(int)) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
					f(i)
				}
			}
		}(f)
	}
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}
