package main

// One restart of a sleeping speaker whose display is stuck (#841, #1784).
//
// On SoundTouch 20 speakers BoseApp's display can stop drawing for good: the
// display controller still logs that it enters its clock state on every
// power-off, but nothing reaches the panel any more (a stuck ST20 read out on
// 2026-11-08: flip thread asleep in its wait, CPU time standing still,
// framebuffer one uniform 0x01). Switching off and on, the clock switch in
// the app and the standby nudge all leave it dark; owners report that only
// pulling the plug brings the clock back. A software restart clears it the
// same way, because it starts BoseApp afresh.
//
// The dark-panel check (internal/oled/darkwatch.go) asks for this only after
// the nudge did not help and the speaker then stayed in standby, stuck, for
// ten more minutes. The rules here keep it from ever becoming a loop or a
// surprise: the speaker must be in standby right now, up for at least 15
// minutes, and not restarted for this reason in the last 24 hours (a stamp on
// NAND, written before the restart; a stamp that cannot be written means no
// restart). Every way out is logged.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/webui"
)

const (
	// stuckDisplayStamp holds the Unix time of the last restart for a stuck
	// display.
	stuckDisplayStamp = "/mnt/nv/streborn/display-restart-at"
	// stuckDisplayEvery is the least time between two such restarts.
	stuckDisplayEvery = 24 * time.Hour
	// stuckDisplayMinUptime keeps a speaker that just booted from being
	// restarted again (the same boot-loop guard as the memory guard).
	stuckDisplayMinUptime = 15 * time.Minute
)

// stuckDisplayRestart is what the restart needs. Fields, so the rules are
// testable without a speaker.
type stuckDisplayRestart struct {
	uptime    func() time.Duration // negative when unreadable
	inStandby func(ctx context.Context) (bool, error)
	lastAt    func() (time.Time, bool)
	stamp     func(time.Time) error
	reboot    func() error
	now       func() time.Time
	logger    *slog.Logger
}

// run restarts the speaker when every rule allows it. Reports whether the
// restart was started.
func (r stuckDisplayRestart) run(ctx context.Context) bool {
	if up := r.uptime(); up >= 0 && up < stuckDisplayMinUptime {
		r.logger.Warn("display: not restarting the speaker for its stuck display, it booted only minutes ago",
			"uptimeMin", int(up.Minutes()))
		return false
	}
	now := r.now()
	if at, ok := r.lastAt(); ok {
		// Either direction: a stamp from a clock that was wrong when it was
		// written must not block the restart for good, nor allow two in a row.
		if d := now.Sub(at); d < stuckDisplayEvery && d > -stuckDisplayEvery {
			r.logger.Warn("display: not restarting the speaker for its stuck display, it already restarted for this within the last day",
				"lastRestart", at.Format(time.RFC3339))
			return false
		}
	}
	standby, err := r.inStandby(ctx)
	if err != nil || !standby {
		r.logger.Info("display: not restarting the speaker for its stuck display, it is not in standby or its state is unreadable",
			"err", err)
		return false
	}
	if err := r.stamp(now); err != nil {
		r.logger.Warn("display: not restarting the speaker for its stuck display, the restart could not be noted (it could repeat)",
			"err", err)
		return false
	}
	r.logger.Warn("display: restarting the sleeping speaker, its display stopped drawing and only a restart brings the standby clock back (#841)")
	if err := r.reboot(); err != nil {
		r.logger.Error("display: restarting the speaker failed", "err", err)
		return false
	}
	return true
}

// readStuckDisplayStamp reads the time of the last restart for a stuck
// display; ok is false when there is none.
func readStuckDisplayStamp(path string) (time.Time, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, false
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || sec <= 0 {
		return time.Time{}, false
	}
	return time.Unix(sec, 0), true
}

// writeStuckDisplayStamp notes a restart, synced to NAND before the restart
// can cut it off.
func writeStuckDisplayStamp(path string, at time.Time) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(strconv.FormatInt(at.Unix(), 10) + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// rebootSpeaker restarts the speaker the way the memory guard does.
// Not while the rescue agent runs from RAM without a verified copy on the
// NAND: the restart would leave the speaker without STR.
func rebootSpeaker() error {
	if why := webui.RebootUnsafe(); why != "" {
		return errors.New(why)
	}
	_ = exec.Command("sync").Run()
	return exec.Command("reboot").Run()
}

func uptimeDuration() time.Duration {
	sec := readUptimeSec()
	if sec < 0 {
		return -1
	}
	return time.Duration(sec) * time.Second
}
