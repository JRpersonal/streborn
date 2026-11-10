package main

import (
	"context"
	"io"
	"log/slog"
	"runtime"
	"sync"
	"testing"
	"time"
)

// The reconcile loop and the peer sweep start long before the webui server
// and the mDNS announcer they ask exist, so run() binds their seams while the
// readers are already running. Plain package-level function vars made that a
// data race (the detector flagged the write in run() against the first read in
// the loop); the seams are atomic now. This test keeps them that way: it reads
// through every helper the loop and the roster use, from several goroutines,
// while the hooks are bound and unbound the way run() binds them. Run with
// -race, where a plain assignment would fail it again.
func TestLateBoundHooksAreRaceFreeAgainstRunningReaders(t *testing.T) {
	// The reconcile's three seams are fields of the reconciler now, so this
	// test binds them on one of its own.
	rec := newTestReconciler(t)
	prevName := peerSelfNameFn.Load()
	prevIDs := peerSelfDeviceIDsFn.Load()
	t.Cleanup(func() {
		peerSelfNameFn.Store(prevName)
		peerSelfDeviceIDsFn.Store(prevIDs)
	})
	setPeerSelfNameFn(nil)
	setPeerSelfDeviceIDsFn(nil)

	stop := make(chan struct{})
	var readers, started sync.WaitGroup
	// What the reconcile loop reads before a forced pass, and what the roster
	// reads on every sweep and seed.
	reads := []func(){
		func() { rec.groupWakeSettling() },
		func() { rec.sinceLastUserPlay() },
		func() { rec.presetWriteGroupFollower() },
		func() { peerSelfDeviceIDs() },
		func() { peerSelfName() },
		func() { matchesSelfDeviceID("AABBCCDDEEFF") },
	}
	for _, read := range reads {
		readers.Add(1)
		started.Add(1)
		go func(read func()) {
			defer readers.Done()
			read()
			started.Done()
			for {
				select {
				case <-stop:
					return
				default:
					read()
				}
			}
		}(read)
	}
	// The readers are running (each has read once) before the first bind, the
	// way the loop and the sweep are running before run() reaches the webui
	// server and the announcer. Without this the writes could all land before
	// a reader is scheduled, and the detector would have nothing to compare.
	started.Wait()

	// The binding, in run()'s order: the webui seams once the server exists,
	// the announcer seams once the announcer exists, repeated with a yield in
	// between for a short while so the reads interleave with the writes and
	// see the switch from unset to set and back.
	deadline := time.Now().Add(50 * time.Millisecond)
	for i := 0; time.Now().Before(deadline); i++ {
		rec.setQuietWakeEpisodeActive(func() bool { return i%2 == 0 })
		rec.setLastUserPlayStart(func() time.Time { return time.Now().Add(-time.Duration(i) * time.Second) })
		rec.setGroupFollower(func() (bool, string) { return i%3 == 0, "stereo pair follower" })
		setPeerSelfNameFn(func() string { return " Kitchen " })
		setPeerSelfDeviceIDsFn(func() []string { return []string{"AABBCCDDEEFF"} })
		runtime.Gosched()
		if i%10 == 9 {
			rec.setQuietWakeEpisodeActive(nil)
			rec.setLastUserPlayStart(nil)
			rec.setGroupFollower(nil)
			setPeerSelfNameFn(nil)
			setPeerSelfDeviceIDsFn(nil)
			runtime.Gosched()
		}
	}
	close(stop)
	readers.Wait()

	// The nil-safe reads answer as before the seams were atomic.
	setPeerSelfNameFn(nil)
	setPeerSelfDeviceIDsFn(nil)
	rec.setQuietWakeEpisodeActive(nil)
	rec.setLastUserPlayStart(nil)
	rec.setGroupFollower(nil)
	if rec.groupWakeSettling() {
		t.Error("an unwired quiet-wake seam must read as no group wake")
	}
	if rec.sinceLastUserPlay() != -1 {
		t.Error("an unwired play-start seam must read as unknown")
	}
	if f, _ := rec.presetWriteGroupFollower(); f {
		t.Error("an unwired follower seam must read as standalone")
	}
	if peerSelfName() != "" || peerSelfDeviceIDs() != nil {
		t.Error("unwired self-identity seams must read as empty")
	}
	setPeerSelfNameFn(func() string { return " Kitchen " })
	if got := peerSelfName(); got != "Kitchen" {
		t.Errorf("peerSelfName() = %q, want the trimmed announcer name", got)
	}
}

// The native-preset logger is bound in wireLate, long after buildMedia has
// started the reconcile goroutines, and the reconcile's pass reads it on every
// native-readiness verdict (nativePresetLocation -> nativeRadioReady) and on
// every lost native write (disableNativePresets). As a plain package variable
// that was a write in run() against a read in the loop with nothing ordering
// them. This reads it the way the pass does while it is bound the way
// wireLate binds it. Run with -race, where a plain assignment fails it.
func TestNativeReadyLoggerIsRaceFreeAgainstARunningReconcile(t *testing.T) {
	prev := nativeLogger()
	nativeReady.Lock()
	prevChecked, prevOK := nativeReady.checked, nativeReady.ok
	nativeReady.Unlock()
	t.Cleanup(func() {
		setNativeReadyLogger(prev)
		nativeReady.Lock()
		nativeReady.checked, nativeReady.ok = prevChecked, prevOK
		nativeReady.Unlock()
	})
	setNativeReadyLogger(nil)

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	stop := make(chan struct{})
	var reader, started sync.WaitGroup
	reader.Add(1)
	started.Add(1)
	go func() {
		defer reader.Done()
		first := true
		for {
			select {
			case <-stop:
				return
			default:
			}
			// A forced pass drops the cached verdict and asks again; with no
			// box host the probe answers at once and the verdict is logged.
			invalidateNativeRadioReady()
			nativeRadioReady(context.Background(), "")
			if first {
				first = false
				started.Done()
			}
		}
	}()
	started.Wait()
	deadline := time.Now().Add(50 * time.Millisecond)
	for i := 0; time.Now().Before(deadline); i++ {
		if i%2 == 0 {
			setNativeReadyLogger(quiet)
		} else {
			setNativeReadyLogger(nil)
		}
		runtime.Gosched()
	}
	close(stop)
	reader.Wait()
}
