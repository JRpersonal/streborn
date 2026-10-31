package main

// A source frame out of STANDBY is not always the speaker waking up.
//
// Measured on a Portable (2026-10-27): the power key put the speaker into
// standby (gabbo UPNP -> STANDBY, syslog "DisplayController Idle >>
// StandbyNoClock >> Standby"), STR's standby bounce stopped and cleared its
// transport (#197), and 80 ms later the speaker emitted a STANDBY -> UPNP
// source frame in answer to that stop. It never left standby: no syslog wake
// followed and :8090/now_playing kept saying STANDBY for minutes. Trusting
// the frame put STR's track screen on the panel of a speaker in standby and
// fired the wake hooks (a key re-sync asked of a sleeping speaker, a wake
// recorded in the power gate, a held ring standby cancelled). Live on the
// same speaker, a speaker in standby keeps sending such UPNP frames while
// STR talks to its renderer, seconds and tens of seconds after the standby.
//
// So a switch out of STANDBY onto STR's source is checked against the
// speaker's own word before anything acts on it: one now_playing read at
// once (a real wake answers with the new source straight away, so a real
// power-on still redraws at once) and one confirming read a few seconds
// later. Two one-shot reads per switch, no polling. The read never runs on
// the gabbo read loop. The gabbo stream's own idea of the source is left as
// it is: set back to STANDBY, every later UPNP frame of the sleeping
// speaker became a new switch and a new check. A real wake the stream then
// does not report as a switch out of STANDBY still reaches the track screen
// through the speaker's syslog wake (trackOnBoxWake).

import (
	"sync"
	"time"
)

// wakeVerdict is what the speaker said about itself.
type wakeVerdict int

const (
	wakeUnknown wakeVerdict = iota // the speaker did not answer
	wakeAwake                      // any source other than STANDBY
	wakeStandby                    // still in standby
)

func (v wakeVerdict) String() string {
	switch v {
	case wakeAwake:
		return "awake"
	case wakeStandby:
		return "standby"
	}
	return "no answer"
}

var (
	// wakeConfirmDelay is how long after the first read the confirming read
	// runs. A var so tests can shorten it.
	wakeConfirmDelay = 3 * time.Second
)

// wakeCheck is one check of a switch out of standby. Results are published
// once each: first after the read at once, final after the confirming read.
type wakeCheck struct {
	firstDone, finalDone chan struct{}
	first, final         wakeVerdict
	firstSrc, finalSrc   string
}

func verdictOf(src string, ok bool) wakeVerdict {
	switch {
	case !ok || src == "":
		return wakeUnknown
	case src == "STANDBY":
		return wakeStandby
	}
	return wakeAwake
}

// startWakeCheck reads the speaker's source now and once more after
// wakeConfirmDelay, in its own goroutine. read is the :8090 now_playing
// source, ok false when the speaker did not answer.
func startWakeCheck(read func() (string, bool)) *wakeCheck {
	w := &wakeCheck{firstDone: make(chan struct{}), finalDone: make(chan struct{})}
	confirm := wakeConfirmDelay
	go func() {
		src, ok := read()
		w.first, w.firstSrc = verdictOf(src, ok), src
		close(w.firstDone)
		time.Sleep(confirm)
		src, ok = read()
		w.final, w.finalSrc = verdictOf(src, ok), src
		close(w.finalDone)
	}()
	return w
}

// settled blocks until the check can tell whether the speaker woke: awake at
// the first read, or whatever the confirming read says.
func (w *wakeCheck) settled() wakeVerdict {
	<-w.firstDone
	if w.first == wakeAwake {
		return wakeAwake
	}
	<-w.finalDone
	return w.final
}

// wakeChecks is the handler's bookkeeping: the check the newest switch out of
// standby started, for the standby-exit hook of the same frame. Zero value
// ready.
type wakeChecks struct {
	mu      sync.Mutex
	pending *wakeCheck
}

func (c *wakeChecks) set(w *wakeCheck) {
	c.mu.Lock()
	c.pending = w
	c.mu.Unlock()
}

func (c *wakeChecks) take() *wakeCheck {
	c.mu.Lock()
	defer c.mu.Unlock()
	w := c.pending
	c.pending = nil
	return w
}

// boxSourceNow is the speaker's own word on its source (:8090 now_playing).
func (h *presetWsHandler) boxSourceNow() (string, bool) {
	src, _, _ := h.nowPlayingSummary()
	return src, src != ""
}

// canCheckWake reports whether there is a speaker to ask.
func (h *presetWsHandler) canCheckWake() bool {
	return h.boxHost != "" || h.boxSummaryFn != nil
}

// beginWakeCheck starts the check for a switch from -> to when it needs one:
// out of standby onto one of STR's sources, the only switch STR's own
// transport commands provoke. nil otherwise.
func (h *presetWsHandler) beginWakeCheck(from, to string) *wakeCheck {
	if from != "STANDBY" || !trackSourceKeeps(to) || !h.canCheckWake() {
		return nil
	}
	w := startWakeCheck(h.boxSourceNow)
	h.wakeChecks.set(w)
	go h.settleWakeCheck(w, to)
	return w
}

// newBoxWakeCheck is the check for a wake read from the syslog ring, nil
// when there is no speaker to ask.
func (h *presetWsHandler) newBoxWakeCheck() *wakeCheck {
	if !h.canCheckWake() {
		return nil
	}
	return startWakeCheck(h.boxSourceNow)
}

// settleWakeCheck writes the one line a switch the speaker denied gets, once
// the confirming read is in.
func (h *presetWsHandler) settleWakeCheck(w *wakeCheck, to string) {
	<-w.finalDone
	if w.final != wakeStandby || h.logger == nil {
		return
	}
	h.logger.Info("box ws: the speaker never left standby, the source frame was not a wake; track screen and wake hooks stay off",
		"frame", "STANDBY->"+to, "firstRead", w.first.String())
}
