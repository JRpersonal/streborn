package main

// One standby after an STR update, so the speaker draws its standby clock again.
//
// The Wohnzimmer SoundTouch 30 showed no clock in standby after the v1.5.0
// update (2026-11-03). That chassis takes an update as an agent-only restart:
// BoseApp kept running (73 h) and its display controller stayed in the standby
// clock state it had entered before the update. STR's own pictures went over
// the panel in between (the update screen, the "updated" splash), and the
// firmware never drew its clock again: it draws it when it ENTERS that state,
// not on every tick. A reboot fixed it, because the controller then entered the
// clock state after STR's splash. The exact trigger did not reproduce on
// demand: later updates with the splash on and off both kept the clock.
//
// Jens' call: after an STR update, put a sleeping speaker into standby once.
// The firmware answers a standby request on a sleeping speaker by leaving its
// clock state and entering it again (live, same ST30: "Standby >>
// StandbyNoClock"), which is the transition that draws the clock. It does not
// wake the speaker. The nudge only runs after an update, after the splash has
// released the panel, on a speaker with a display, and only while it is in
// standby: a playing speaker is never touched.

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/oled"
)

// clockNudge is what the nudge needs from the speaker. Fields, so the rules
// are testable without one.
type clockNudge struct {
	displayReady func() bool
	hasDisplay   func(ctx context.Context) (has, ok bool)
	inStandby    func(ctx context.Context) (standby bool, err error)
	standby      func(ctx context.Context) error
	sleep        func(ctx context.Context, d time.Duration) bool
	logger       *slog.Logger
	// readyWait bounds the wait for the firmware's display; settle is the
	// pause after the splash before the speaker is asked.
	readyWait time.Duration
	settle    time.Duration
	// stateTries and readRetry bound the standby read (default 1, no pause).
	stateTries int
	readRetry  time.Duration
	// why names the occasion in the log when it is not an update (the
	// dark-panel check after a track overlay release, #841).
	why string
}

func (n clockNudge) readTries() int {
	if n.stateTries < 1 {
		return 1
	}
	return n.stateTries
}

// run performs the nudge once. It reports whether it sent the standby.
func (n clockNudge) run(ctx context.Context) bool {
	deadline := time.Now().Add(n.readyWait)
	for !n.displayReady() {
		if time.Now().After(deadline) {
			n.logger.Info("standby clock nudge skipped: the speaker's display did not come up in time")
			return false
		}
		if !n.sleep(ctx, 2*time.Second) {
			return false
		}
	}
	if !n.sleep(ctx, n.settle) {
		return false
	}
	if has, ok := n.hasDisplay(ctx); ok && !has {
		n.logger.Info("standby clock nudge skipped: this speaker has no display")
		return false
	}
	// BoseApp can be slow to answer right after an agent restart (live ST30,
	// 2026-11-03: /info timed out for minutes), so the state read gets a few
	// tries. Every way out is logged: a silent skip cannot be told apart from a
	// nudge that never ran.
	var standby bool
	var err error
	for attempt := 1; attempt <= n.readTries(); attempt++ {
		standby, err = n.inStandby(ctx)
		if err == nil {
			break
		}
		if attempt < n.readTries() && !n.sleep(ctx, n.readRetry) {
			return false
		}
	}
	if err != nil {
		n.logger.Info("standby clock nudge skipped: the speaker's state stayed unreadable", "tries", n.readTries(), "err", err)
		return false
	}
	if !standby {
		n.logger.Info("standby clock nudge skipped: the speaker is not in standby, so it is left alone")
		return false
	}
	if err := n.standby(ctx); err != nil {
		n.logger.Info("standby clock nudge: the standby request failed", "err", err)
		return false
	}
	if n.why != "" {
		n.logger.Info("standby clock nudge: sent the sleeping speaker one standby, so it draws its clock again", "why", n.why)
		return true
	}
	n.logger.Info("standby clock nudge: sent the sleeping speaker one standby after the update, so it draws its clock again")
	return true
}

// darkPanelHooks wires the look at the panel after the track overlay let go
// of it (oled/darkwatch.go). A standby clock that is still black a minute
// later gets the same single standby the update nudge sends: the firmware
// draws its clock when it enters the clock state, and a standby request on a
// sleeping speaker is what makes it enter that state again without waking it
// (#841). The box is only asked when the panel is black, never on a timer.
func darkPanelHooks(bc *boxapi.Client, logger *slog.Logger) oled.DarkPanelHooks {
	inStandby := func(ctx context.Context) (bool, error) {
		np, err := bc.NowPlaying(ctx)
		if err != nil {
			return false, err
		}
		return np.IsStandby(), nil
	}
	return oled.DarkPanelHooks{
		ClockExpected: func(ctx context.Context) (bool, error) {
			return clockExpected(ctx, inStandby, bc.GetClockConfig)
		},
		Recover: func(ctx context.Context) bool {
			return clockNudge{
				displayReady: oled.DisplayReady,
				hasDisplay:   bc.HasDisplay,
				inStandby:    inStandby,
				standby:      bc.Standby,
				sleep:        sleepCtx,
				logger:       logger,
				why:          "the panel stayed black after the track overlay let go of it",
			}.run(ctx)
		},
	}
}

// clockExpected reports whether the firmware should be drawing its standby
// clock: the speaker is in standby and its clock is switched on. A clock the
// owner switched off, or a speaker without one, keeps a black panel in
// standby, which is right.
func clockExpected(ctx context.Context, inStandby func(context.Context) (bool, error),
	clockConfig func(context.Context) (boxapi.ClockConfig, error)) (bool, error) {
	standby, err := inStandby(ctx)
	if err != nil || !standby {
		return false, err
	}
	cfg, err := clockConfig(ctx)
	if errors.Is(err, boxapi.ErrNoClockConfig) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(cfg.UserEnable), "true"), nil
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
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
