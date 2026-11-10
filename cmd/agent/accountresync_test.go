package main

import (
	"sync"
	"testing"
	"time"
)

// fireLog records each check the debouncer ran, with the reads it covered.
type fireLog struct {
	mu    sync.Mutex
	fires []int
	at    []time.Time
}

func (f *fireLog) fire(reads int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fires = append(f.fires, reads)
	f.at = append(f.at, time.Now())
}

func (f *fireLog) snapshot() ([]int, []time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.fires...), append([]time.Time(nil), f.at...)
}

// waitFires polls until n checks ran or the deadline passed.
func (f *fireLog) waitFires(n int, d time.Duration) []int {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fires, _ := f.snapshot(); len(fires) >= n {
			return fires
		}
		time.Sleep(5 * time.Millisecond)
	}
	fires, _ := f.snapshot()
	return fires
}

// TestAccountResyncRunsOnceAfterTheLastRead is the ST10 case: the firmware
// read its account twice in quick succession. One check must run, after the
// SECOND read, never one per read and never one between them.
func TestAccountResyncRunsOnceAfterTheLastRead(t *testing.T) {
	const delay = 60 * time.Millisecond
	var log fireLog
	r := newAccountResync(time.Hour, delay, log.fire)

	r.noteAccountRead()
	time.Sleep(delay / 2)
	last := time.Now()
	r.noteAccountRead()

	fires := log.waitFires(1, 2*time.Second)
	if len(fires) != 1 || fires[0] != 2 {
		t.Fatalf("checks = %v, want exactly one covering both reads", fires)
	}
	_, at := log.snapshot()
	if gap := at[0].Sub(last); gap < delay {
		t.Errorf("the check ran %v after the last read, want at least %v", gap, delay)
	}
	// Nothing else is pending: no second check turns up later.
	time.Sleep(3 * delay)
	if fires, _ := log.snapshot(); len(fires) != 1 {
		t.Errorf("checks = %v after the burst went quiet, want exactly one", fires)
	}
}

// TestAccountResyncSeparateBurstsEachGetACheck: a read long after the first
// burst's check is a new wipe and gets its own check.
func TestAccountResyncSeparateBurstsEachGetACheck(t *testing.T) {
	const delay = 30 * time.Millisecond
	var log fireLog
	r := newAccountResync(time.Hour, delay, log.fire)

	r.noteAccountRead()
	if fires := log.waitFires(1, 2*time.Second); len(fires) != 1 {
		t.Fatalf("first burst: checks = %v, want one", fires)
	}
	r.noteAccountRead()
	if fires := log.waitFires(2, 2*time.Second); len(fires) != 2 || fires[1] != 1 {
		t.Fatalf("second burst: checks = %v, want a second check covering one read", fires)
	}
}

// TestAccountResyncIgnoresReadsAfterTheBootWindow: past the boot window an
// account read changes nothing, so it cannot become a source of extra reads
// of the speaker's key list.
func TestAccountResyncIgnoresReadsAfterTheBootWindow(t *testing.T) {
	const delay = 10 * time.Millisecond
	var log fireLog
	r := newAccountResync(time.Minute, delay, log.fire)
	clock := r.start.Add(2 * time.Minute)
	r.now = func() time.Time { return clock }

	r.noteAccountRead()
	time.Sleep(10 * delay)
	if fires, _ := log.snapshot(); len(fires) != 0 {
		t.Fatalf("checks = %v for a read after the boot window, want none", fires)
	}

	// Inside the window the same read arms a check.
	clock = r.start.Add(30 * time.Second)
	r.noteAccountRead()
	if fires := log.waitFires(1, 2*time.Second); len(fires) != 1 {
		t.Fatalf("checks = %v for a read inside the boot window, want one", fires)
	}
}

// TestAccountResyncStaleTimerDoesNothing: a timer of an older arming that
// fires anyway (Stop lost the race) must neither run the check early nor eat
// the reads the newer arming is waiting for.
func TestAccountResyncStaleTimerDoesNothing(t *testing.T) {
	var log fireLog
	r := newAccountResync(time.Hour, time.Hour, log.fire)
	r.noteAccountRead()
	r.noteAccountRead()
	r.expire(1) // the first arming's timer, superseded by the second read
	if fires, _ := log.snapshot(); len(fires) != 0 {
		t.Fatalf("a stale timer ran the check: %v", fires)
	}
	r.expire(2)
	if fires, _ := log.snapshot(); len(fires) != 1 || fires[0] != 2 {
		t.Fatalf("checks = %v, want one covering both reads", fires)
	}
	r.mu.Lock()
	if r.timer != nil {
		r.timer.Stop()
	}
	r.mu.Unlock()
}

// TestAskPresetRecheckArmsTheOrdinaryPass: the wired action arms the recheck,
// never the forced full re-write.
func TestAskPresetRecheckArmsTheOrdinaryPass(t *testing.T) {
	presetRecheckAsk.Store(false)
	t.Cleanup(func() { presetRecheckAsk.Store(false) })

	// The action is handed no resync scheduler at all, so it has no way to
	// arm the forced full re-sync: only the ordinary pass can come of it.
	askPresetRecheckAfterAccountRead(nil)(2)
	if !presetRecheckAsk.Load() {
		t.Error("the recheck was not armed")
	}
}
