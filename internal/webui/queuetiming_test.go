package webui

import (
	"time"
)

// The queue watcher and the lone-track end watch run on real time: a 4 s poll,
// a 6 s margin, a 25 s stall, a 15 s freeze. Their tests used to wait all of
// that out, which made this package's test run a matter of minutes. The
// durations are a value on the server (queueTiming in queueplay.go), so a test
// can run the very same state machine at a faster clock: every duration is
// divided by one factor, and the scenario's track lengths, positions and sleeps
// are written in qsec, one "box second" at that clock. The ratios the watcher's
// rules are built on (poll vs. margin vs. stall vs. freeze vs. slack) are
// untouched, so a test proves exactly what it proved at the slow clock.
//
// The clock is installed on the server under test (s.queueTiming), never on a
// package variable: a watcher goroutine of one test must never read a value
// another test is restoring, and nothing here needs a cleanup.

// queueTestScale is the factor fastQueueTiming speeds the watcher up by.
const queueTestScale = 20

// qsec is one box second under fastQueueTiming: a 5 s track is 5*qsec long.
const qsec = time.Second / queueTestScale

// fastQueueTiming is the watcher's clock at queueTestScale times its normal
// speed, a fresh copy the test may adjust before the watcher starts.
func fastQueueTiming() *queueTiming { return scaleQueueTiming(queueTestScale) }

// scaleQueueTiming is a fresh copy of the production clock with every
// duration divided by scale.
func scaleQueueTiming(scale int) *queueTiming {
	qt := productionQueueTiming
	for _, d := range []*time.Duration{
		&qt.pollInterval, &qt.endEpsilon, &qt.timerMargin, &qt.stallTimeout,
		&qt.frozenTimeout, &qt.overrunSlack, &qt.ownEndSlack,
		&qt.zoneEndWindow, &qt.zoneEndPoll,
	} {
		*d /= time.Duration(scale)
	}
	return &qt
}

// betweenPolls is a sleep that lands between two watcher polls at the fast
// clock: past the n-th poll, well before the next. A sleep of exactly n polls
// races the ticker.
func betweenPolls(n int) time.Duration {
	poll := fastQueueTiming().pollInterval
	return time.Duration(n)*poll + poll/2
}
