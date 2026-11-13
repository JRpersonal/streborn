package oled

// A look at the panel after the track overlay let go of it, and after the
// speaker went to standby (#841, #1784).
//
// When STR releases the panel it leaves a black frame and lets BoseApp's flip
// thread run again; the firmware then paints its own screen. On SoundTouch 20
// speakers the standby clock sometimes never came back after that. Two
// readings from a stuck ST20 (#841, 2026-11-08) pinned what the panel looks
// like then: the flip thread was free (TracerPid 0), asleep in its wait
// (poll_schedule_timeout) with its CPU time standing still for a minute, and
// /dev/fb0 was filled with 0x01 bytes. STR's release paints 0x00, so BoseApp
// did flip one more frame after the release (its own blank screen) and then
// never again, while its display controller kept logging that it entered the
// clock state. A second ST20 (#1784) shows the same: after the first dark
// power-off the speaker went through five more clock-state entries (power
// buttons, the clock switched off and on in the app) and the clock stayed
// dark. Switching off and on does not cure it; a restart does.
//
// So the panel is sampled twice, at about 2 s and 65 s, after a track
// overlay release and after every switch to standby on a speaker with a
// panel. The two samples are a minute apart, so a working standby clock has
// changed in between. A panel that is uniformly dark (every byte the same low
// value: STR's 0x00, BoseApp's 0x01) or did not change at all (a frozen
// clock, the original #841 report) while the firmware should be showing its
// clock is stuck. Then, step by step, each only if the one before did not
// help:
//
//  1. one WARN with the flip thread's state, and the single standby the
//     post-update clock nudge already sends (that recovers a controller that
//     merely missed its redraw, as on the ST30 after an update);
//  2. a third sample shortly after the nudge; still stuck means BoseApp's
//     display is stuck, which only a restart clears, logged as a WARN;
//  3. after the speaker has stayed in standby, stuck, for several more
//     minutes, the agent's Restart hook (one restart, rate-limited there).
//
// Cost: one read of the framebuffer in RAM per sample (no copy, no NAND),
// the /proc reads only when the panel is stuck, and the box is asked only
// then. A newer release, a new standby or a new STR hold of the panel ends a
// running watch.
//
// A clock can also stop hours into a standby, long after these looks are
// over; longstandby.go runs the same watch now and then for that.

import (
	"context"
	"fmt"
	"hash/fnv"
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
	// Restart is the last step for a display the nudge did not bring back:
	// it may restart the speaker (its own rules decide) and reports whether
	// it did. Nil leaves a stuck display at the WARN.
	Restart func(ctx context.Context) bool
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
	// darkAfterNudge is when the panel is looked at again after the nudge:
	// the firmware draws its clock as it enters the clock state.
	darkAfterNudge = 30 * time.Second
	// darkRestartAfter is how long a display the nudge did not bring back
	// stays stuck in standby before the Restart hook is asked, so a speaker
	// someone just switched off is not restarted under their hands.
	darkRestartAfter = 10 * time.Minute
	// darkWatchLimit bounds one watch from start to end.
	darkWatchLimit = 15 * time.Minute
	// darkFillMax is the brightest value a uniform frame may have and still
	// count as dark: STR's lowest grey level (0x11) and everything below it,
	// BoseApp's 0x01 blank included.
	darkFillMax = 0x11
)

// darkWatchGen makes a newer release or standby end the watch of an older
// one; darkWatchLive is the generation of the watch still running (0: none)
// and darkWatchStarted when it started, so a standby does not restart a
// watch that is already on it.
var (
	darkWatchGen     atomic.Uint64
	darkWatchLive    atomic.Uint64
	darkWatchStarted atomic.Int64
)

// panelSample is one look at the framebuffer.
type panelSample struct {
	// dark: every byte the same value, at most darkFillMax.
	dark bool
	fill byte
	// sum identifies the picture, so two samples tell whether it changed.
	sum uint64
}

// stuckAgainst reports whether s shows a panel that is not being drawn: dark,
// or the same picture as prev (nil on the first look).
func (s panelSample) stuckAgainst(prev *panelSample) bool {
	return s.dark || (prev != nil && s.sum == prev.sum)
}

// attrs describes the sample for the log.
func (s panelSample) attrs() []any {
	if s.dark {
		return []any{"panel", "dark", "fill", fmt.Sprintf("0x%02x", s.fill)}
	}
	return []any{"panel", "unchanged"}
}

// samplePanelFrame reads one frame into a sample. The dark test stops at the
// first byte that differs, so a drawn panel costs a few bytes; the sum is one
// pass over 12800 bytes in RAM.
func samplePanelFrame(frame []byte) panelSample {
	fill, dark := frameDarkFill(frame)
	h := fnv.New64a()
	_, _ = h.Write(frame)
	return panelSample{dark: dark, fill: fill, sum: h.Sum64()}
}

// frameDarkFill reports whether every byte of a frame has the same value of
// at most darkFillMax, and that value. A strided pass first, so a clock or
// any other picture is told apart within a few hundred bytes; only a frame
// that passes it is read in full.
func frameDarkFill(frame []byte) (fill byte, dark bool) {
	if len(frame) == 0 {
		return 0, false
	}
	fill = frame[0]
	if fill > darkFillMax {
		return fill, false
	}
	const stride = 61 // prime, so it does not line up with the 128-byte rows
	for i := 0; i < len(frame); i += stride {
		if frame[i] != fill {
			return fill, false
		}
	}
	for _, b := range frame {
		if b != fill {
			return fill, false
		}
	}
	return fill, true
}

// darkWatch looks at the panel after one release or standby. Fields, so the
// rules are testable without a panel.
type darkWatch struct {
	hooks DarkPanelHooks
	// readFrame samples the framebuffer; ok is false when it could not be
	// read.
	readFrame func() (s panelSample, ok bool)
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
	// afterNudge and restartAfter time the steps after the nudge.
	afterNudge   time.Duration
	restartAfter time.Duration
	// quiet keeps the routine lines of a standby watch at DEBUG: every
	// standby of a speaker with the clock switched off is dark, rightly.
	quiet bool
}

// darkVerdict is how a watch ended, for the tests.
type darkVerdict string

const (
	darkNoHooks    darkVerdict = "no hooks"
	darkSuperseded darkVerdict = "superseded"
	darkUnreadable darkVerdict = "unreadable"
	darkPainted    darkVerdict = "painted"
	darkNotClock   darkVerdict = "no clock expected"
	darkRecovered  darkVerdict = "nudged, clock back"
	darkLeft       darkVerdict = "dark, nudge not sent"
	darkStuck      darkVerdict = "stuck, not restarted"
	darkRestarted  darkVerdict = "stuck, restarted"
)

// look is one look at the panel: done when the watch ends here. prev is the
// sample to compare with (nil: only a dark panel counts as stuck).
func (w darkWatch) look(prev *panelSample) (s panelSample, verdict darkVerdict, done bool) {
	if !w.current() || w.panelOurs() {
		return s, darkSuperseded, true
	}
	s, ok := w.readFrame()
	if !ok {
		return s, darkUnreadable, true
	}
	if !s.stuckAgainst(prev) {
		return s, darkPainted, true
	}
	return s, "", false
}

func (w darkWatch) info(msg string, args ...any) {
	if w.quiet {
		w.logger.Debug(msg, args...)
		return
	}
	w.logger.Info(msg, args...)
}

func (w darkWatch) run(ctx context.Context, reason string) darkVerdict {
	if w.hooks.ClockExpected == nil {
		return darkNoHooks
	}
	if !w.sleep(ctx, w.first) {
		return darkSuperseded
	}
	if !w.current() || w.panelOurs() {
		return darkSuperseded
	}
	first, ok := w.readFrame()
	if !ok {
		return darkUnreadable
	}
	if first.dark {
		w.info("display: the panel is still black 2 s after STR let go of it or the speaker went to standby",
			append([]any{"reason", reason, "fill", fmt.Sprintf("0x%02x", first.fill)}, w.threadInfo()...)...)
	}
	if !w.sleep(ctx, w.second-w.first) {
		return darkSuperseded
	}
	second, v, done := w.look(&first)
	if done {
		if v == darkPainted && first.dark {
			w.info("display: the firmware painted the panel again after STR let go of it", "reason", reason)
		}
		return v
	}
	expected, err := w.hooks.ClockExpected(ctx)
	if err != nil || !expected {
		// Black is right for a playing speaker between screens, a clock the
		// owner switched off, or a state that could not be read.
		args := append([]any{"reason", reason, "err", err}, second.attrs()...)
		if second.dark {
			w.info("display: the panel stayed black, but no standby clock is expected", args...)
		} else {
			// a firmware screen that stood still for a minute while playing
			w.logger.Debug("display: the panel stood still, but no standby clock is expected", args...)
		}
		return darkNotClock
	}
	w.logger.Warn("display: the standby clock is not being drawn a minute after STR let go of the panel or the speaker went to standby",
		append(append([]any{"reason", reason, "afterSec", int(w.second.Seconds())}, second.attrs()...), w.threadInfo()...)...)
	if w.hooks.Recover == nil || !w.hooks.Recover(ctx) {
		return darkLeft
	}

	if !w.sleep(ctx, w.afterNudge) {
		return darkSuperseded
	}
	third, v, done := w.look(&second)
	if done {
		if v == darkPainted {
			w.logger.Info("display: the standby nudge brought the clock back", "reason", reason)
			return darkRecovered
		}
		return v
	}
	w.logger.Warn("display: the standby nudge did not bring the clock back; BoseApp's display is stuck and only a restart clears it (#841)",
		append(append([]any{"reason", reason}, third.attrs()...), w.threadInfo()...)...)
	if w.hooks.Restart == nil {
		return darkStuck
	}

	// Give whoever just switched the speaker off time to switch it on again
	// before it is restarted; a wake, a new standby or STR's own screen ends
	// the watch in between.
	if !w.sleep(ctx, w.restartAfter) {
		return darkSuperseded
	}
	last, v, done := w.look(&third)
	if done {
		if v == darkPainted {
			w.logger.Info("display: the firmware drew its panel again on its own", "reason", reason)
		}
		return v
	}
	if expected, err := w.hooks.ClockExpected(ctx); err != nil || !expected {
		w.logger.Info("display: BoseApp's display is still stuck, but the speaker is no longer in standby with its clock on, so it is not restarted",
			"reason", reason, "err", err)
		return darkNotClock
	}
	w.logger.Warn("display: the standby clock stayed dark for minutes after the nudge; asking for a restart of the speaker",
		append([]any{"reason", reason, "stuckMin", int((w.afterNudge + w.restartAfter).Minutes())}, last.attrs()...)...)
	if w.hooks.Restart(ctx) {
		return darkRestarted
	}
	return darkStuck
}

// watchAfterRelease starts the look at the panel after a track overlay
// release, ending any older watch. Returns at once.
func watchAfterRelease(reason string) { startDarkWatch(reason, false, longStandbyVerdict) }

// WatchStandbyPanel starts the look at the panel for a speaker that just went
// to standby, unless STR holds the panel (its release starts its own watch)
// or a watch started moments ago. This also covers a display that got stuck
// earlier: once BoseApp's display stopped drawing, every later power-off is
// dark without STR touching the panel (#1784). Returns at once.
func WatchStandbyPanel(reason string) {
	if currentDarkHooks().ClockExpected == nil || panelHeldBySTR() {
		return
	}
	if !standbyMayStartWatch(darkWatchLive.Load(), darkWatchGen.Load(),
		time.Unix(0, darkWatchStarted.Load()), time.Now()) {
		return
	}
	startDarkWatch(reason, true, longStandbyVerdict)
}

// standbyMayStartWatch is the rule for a standby: a watch that is still in
// its first minute is already on it (the gabbo frame, the syslog line and a
// speaker flapping in and out of standby all arrive within seconds), while an
// older one gives way, so a restart never comes sooner than the full wait
// after the latest switch-off.
func standbyMayStartWatch(live, gen uint64, started, now time.Time) bool {
	if live == 0 || live != gen {
		return true
	}
	return now.Sub(started) > darkSecondLook
}

// startDarkWatch runs one watch in the background; done, when set, hears how
// it ended.
func startDarkWatch(reason string, standby bool, done func(darkVerdict)) {
	h := currentDarkHooks()
	if h.ClockExpected == nil {
		return
	}
	darkWatchStarted.Store(time.Now().UnixNano())
	gen := darkWatchGen.Add(1)
	darkWatchLive.Store(gen)
	w := darkWatch{
		hooks:        h,
		readFrame:    panelFrameSample,
		panelOurs:    panelHeldBySTR,
		threadInfo:   flipThreadInfo,
		current:      func() bool { return darkWatchGen.Load() == gen },
		sleep:        sleepFor,
		logger:       noticeLogger(),
		first:        darkFirstLook,
		second:       darkSecondLook,
		afterNudge:   darkAfterNudge,
		restartAfter: darkRestartAfter,
		quiet:        standby,
	}
	go func() {
		defer darkWatchLive.CompareAndSwap(gen, 0)
		// A standby watch reads sysfs first, off the caller's goroutine (the
		// gabbo read loop): a speaker without a panel has nothing to watch.
		if standby && !panelSupported() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), darkWatchLimit)
		defer cancel()
		v := w.run(ctx, reason)
		if done != nil {
			done(v)
		}
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
