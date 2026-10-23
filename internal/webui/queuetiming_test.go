package webui

import (
	"testing"
	"time"
)

// The queue watcher and the lone-track end watch run on real time: a 4 s poll,
// a 6 s margin, a 25 s stall, a 15 s freeze. Their tests used to wait all of
// that out, which made this package's test run a matter of minutes. The
// durations are variables (queueplay.go), so a test can run the very same
// state machine at a faster clock: every duration is divided by one factor,
// and the scenario's track lengths, positions and sleeps are written in qsec,
// one "box second" at that clock. The ratios the watcher's rules are built on
// (poll vs. margin vs. stall vs. freeze vs. slack) are untouched, so a test
// proves exactly what it proved at the slow clock.

// queueTestScale is the factor fastQueueTiming speeds the watcher up by.
const queueTestScale = 20

// qsec is one box second under fastQueueTiming: a 5 s track is 5*qsec long.
const qsec = time.Second / queueTestScale

// fastQueueTiming runs the queue watcher at queueTestScale times its normal
// speed for the rest of the test.
func fastQueueTiming(t *testing.T) { scaleQueueTiming(t, queueTestScale) }

// scaleQueueTiming divides every watcher duration by scale and restores the
// production values when the test ends.
//
// Call it BEFORE the server under test is built. Cleanups run last-in
// first-out, so the restore registered here runs after the server's own
// cleanup (stopQueue, cancelWatcher, the base context) has sent the watcher
// goroutines away; the short drain before the restore gives them the moment
// they need to see that cancel, so the production values do not land under a
// watcher still mid-poll.
func scaleQueueTiming(t *testing.T, scale int) {
	t.Helper()
	vars := []*time.Duration{
		&queuePollInterval, &queueEndEpsilon, &queueTimerMargin, &queueStallTimeout,
		&queueFrozenTimeout, &queueOverrunSlack, &queueOwnEndSlack,
		&queueZoneEndWindow, &queueZoneEndPoll,
	}
	prev := make([]time.Duration, len(vars))
	for i, v := range vars {
		prev[i] = *v
		*v /= time.Duration(scale)
	}
	t.Cleanup(func() {
		time.Sleep(100 * time.Millisecond)
		for i, v := range vars {
			*v = prev[i]
		}
	})
}

// betweenPolls is a sleep that lands between two watcher polls: past the n-th
// poll, well before the next. A sleep of exactly n polls races the ticker.
func betweenPolls(n int) time.Duration {
	return time.Duration(n)*queuePollInterval + queuePollInterval/2
}
