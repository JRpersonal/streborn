package oled

// A look at the panel after the track overlay let go of it (#841).
//
// When STR releases the panel it leaves a black frame and lets BoseApp's flip
// thread run again; the firmware then paints its own screen. A reporter's
// standby clock stayed blank after that release, and nothing in the log could
// say why: the detach error was thrown away, and nobody looked at the panel
// afterwards. So after a track overlay release the framebuffer is looked at
// twice, at about 2 s and 65 s (the firmware draws the clock on the minute at
// the latest). A panel that is still all black at the second look while the
// firmware should be showing its clock gets one WARN with the flip thread's
// state, and the standby nudge the agent already uses after an update.
//
// Cost: two reads of the framebuffer in RAM per release (no copy, no NAND),
// the /proc reads only when the panel is dark, and the box is asked only then.

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DarkPanelHooks is what the dark-panel check needs from the agent.
type DarkPanelHooks struct {
	// ClockExpected reports whether the firmware should be drawing its
	// standby clock right now: in standby, with the clock switched on.
	ClockExpected func(ctx context.Context) (bool, error)
	// Recover asks the firmware to draw its screen again (the standby clock
	// nudge) and reports whether it sent anything.
	Recover func(ctx context.Context) bool
}

var darkHooks struct {
	mu sync.Mutex
	h  DarkPanelHooks
}

// SetDarkPanelHooks wires the dark-panel check. Without hooks (dev hosts,
// tests) a release is not looked at.
func SetDarkPanelHooks(h DarkPanelHooks) {
	darkHooks.mu.Lock()
	darkHooks.h = h
	darkHooks.mu.Unlock()
}

func currentDarkHooks() DarkPanelHooks {
	darkHooks.mu.Lock()
	defer darkHooks.mu.Unlock()
	return darkHooks.h
}

const (
	darkFirstLook  = 2 * time.Second
	darkSecondLook = 65 * time.Second
)

// darkWatchGen makes a newer release end the watch of an older one.
var darkWatchGen atomic.Uint64

// darkWatch looks at the panel after one release. Fields, so the rules are
// testable without a panel.
type darkWatch struct {
	hooks DarkPanelHooks
	// frameDark reports whether the framebuffer is all zeros; ok is false
	// when it could not be read.
	frameDark func() (dark, ok bool)
	// panelOurs reports whether STR holds the panel again (then the picture
	// on it is STR's and says nothing about the firmware).
	panelOurs func() bool
	// threadInfo describes BoseApp's flip thread as slog attributes.
	threadInfo func() []any
	// current reports whether this watch is still the newest one.
	current func() bool
	sleep   func(ctx context.Context, d time.Duration) bool
	logger  *slog.Logger
	first   time.Duration
	second  time.Duration
}

// darkVerdict is how a watch ended, for the tests.
type darkVerdict string

const (
	darkNoHooks    darkVerdict = "no hooks"
	darkSuperseded darkVerdict = "superseded"
	darkUnreadable darkVerdict = "unreadable"
	darkPainted    darkVerdict = "painted"
	darkNotClock   darkVerdict = "no clock expected"
	darkRecovered  darkVerdict = "nudged"
	darkLeft       darkVerdict = "dark, nudge not sent"
)

// look is one look at the panel: done when the watch ends here.
func (w darkWatch) look() (dark bool, verdict darkVerdict, done bool) {
	if !w.current() || w.panelOurs() {
		return false, darkSuperseded, true
	}
	d, ok := w.frameDark()
	if !ok {
		return false, darkUnreadable, true
	}
	if !d {
		return false, darkPainted, true
	}
	return true, "", false
}

func (w darkWatch) run(ctx context.Context, reason string) darkVerdict {
	if w.hooks.ClockExpected == nil {
		return darkNoHooks
	}
	if !w.sleep(ctx, w.first) {
		return darkSuperseded
	}
	if _, v, done := w.look(); done {
		return v
	}
	w.logger.Info("display: the panel is still black 2 s after STR let go of it",
		append([]any{"reason", reason}, w.threadInfo()...)...)
	if !w.sleep(ctx, w.second-w.first) {
		return darkSuperseded
	}
	if _, v, done := w.look(); done {
		if v == darkPainted {
			w.logger.Info("display: the firmware painted the panel again after STR let go of it", "reason", reason)
		}
		return v
	}
	expected, err := w.hooks.ClockExpected(ctx)
	if err != nil || !expected {
		// Black is right for a playing speaker between screens, a clock the
		// owner switched off, or a state that could not be read.
		w.logger.Info("display: the panel stayed black after STR let go of it, but no standby clock is expected",
			"reason", reason, "err", err)
		return darkNotClock
	}
	w.logger.Warn("display: the standby clock stayed black after STR let go of the panel",
		append([]any{"reason", reason, "afterSec", int(w.second.Seconds())}, w.threadInfo()...)...)
	if w.hooks.Recover != nil && w.hooks.Recover(ctx) {
		return darkRecovered
	}
	return darkLeft
}

// watchAfterRelease starts the look at the panel after a track overlay
// release, ending any older watch. Returns at once.
func watchAfterRelease(reason string) {
	h := currentDarkHooks()
	if h.ClockExpected == nil {
		return
	}
	gen := darkWatchGen.Add(1)
	w := darkWatch{
		hooks:      h,
		frameDark:  panelFrameDark,
		panelOurs:  panelHeldBySTR,
		threadInfo: flipThreadInfo,
		current:    func() bool { return darkWatchGen.Load() == gen },
		sleep:      sleepFor,
		logger:     noticeLogger(),
		first:      darkFirstLook,
		second:     darkSecondLook,
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		w.run(ctx, reason)
	}()
}

// panelHeldBySTR reports whether one of STR's own screens holds the panel.
func panelHeldBySTR() bool {
	if busy.TryLock() {
		busy.Unlock()
		return false
	}
	return true
}

// frameIsDark reports whether every byte of a frame is zero.
func frameIsDark(frame []byte) bool {
	for _, b := range frame {
		if b != 0 {
			return false
		}
	}
	return true
}

// procStatusFields pulls the State and TracerPid lines out of a
// /proc/<pid>/task/<tid>/status text ("" when absent).
func procStatusFields(status string) (state, tracerPid string) {
	for _, line := range strings.Split(status, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch k {
		case "State":
			state = strings.TrimSpace(v)
		case "TracerPid":
			tracerPid = strings.TrimSpace(v)
		}
	}
	return state, tracerPid
}

func sleepFor(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
