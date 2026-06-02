package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// This file mitigates a file-descriptor leak in the closed-source Bose
// firmware app (BoseApp). Over runtime BoseApp accumulates eventfd/timerfd
// objects (measured live: ~720 eventfd + ~233 timerfd, climbing ~350/h)
// against a soft RLIMIT_NOFILE of 1216. When it reaches the ceiling its
// :8090 HTTP API deadlocks (alive but unresponsive): no wake, no volume, no
// clean playback, the desktop app's settings never load. FDs partially
// recycle, so it briefly recovers (the "press the preset 4-5 times until it
// plays" symptom users reported). The leak is intrinsic to BoseApp, not
// driven by STR's polling (verified: 25 now_playing requests added zero
// eventfd/timerfd). We cannot patch Bose's binary, so we do two things from
// the agent (which runs as root on the box):
//
//  1. Raise BoseApp's open-files soft limit toward its hard limit (4096),
//     tripling the headroom before the deadlock. We deliberately do NOT
//     touch the hard limit or system-wide fs.file-max: the box has ~120 MB
//     RAM and fs.file-max (~9751) is the kernel's RAM-derived cap; letting a
//     leak grow past that would starve the whole system instead of just
//     BoseApp, which is worse. 4096 + the other processes (~1200) stays well
//     under fs.file-max.
//  2. Watch :8090 and, if it is confirmed dead for several minutes while
//     BoseApp is still alive, reboot the box. The Bose supervisor reboots on
//     BoseApp death but has a blind spot for a hang; this closes that gap.
//     Guarded against reboot loops and disablable via STR_NO_HANG_RECOVERY.

const (
	// boseAppFDTarget is the soft RLIMIT_NOFILE we raise BoseApp to. Capped
	// at its existing hard limit at apply time so we never need to raise the
	// hard cap (which would invite the fs.file-max risk above).
	boseAppFDTarget uint64 = 4096

	hangProbeEvery   = 90 * time.Second
	hangFailsToReact = 4               // ~4 consecutive misses (~6 min) before acting
	hangMinUptime    = 5 * time.Minute // never act right after boot, :8090 needs ~60-70 s
	hangRebootMarker = "/mnt/nv/streborn/last-hang-reboots"
	hangLoopWindow   = time.Hour // if >= hangLoopMax reboots within this, stop (loop guard)
	hangLoopMax      = 3
)

// findBoseAppPID returns the PID of the Bose firmware app, or 0 if not
// running. /proc/<pid>/comm is the kernel-truncated (<=15 char) name;
// "BoseApp" fits.
func findBoseAppPID() int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		comm, err := os.ReadFile("/proc/" + e.Name() + "/comm")
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(comm)) == "BoseApp" {
			pid, _ := strconv.Atoi(e.Name())
			return pid
		}
	}
	return 0
}

// raiseBoseAppFDLimit lifts BoseApp's open-files soft limit to target,
// capped at its current hard limit. Idempotent and cheap; safe to call on a
// timer. A no-op when BoseApp is not (yet) running or already at the target.
func raiseBoseAppFDLimit(logger *slog.Logger, target uint64) {
	pid := findBoseAppPID()
	if pid == 0 {
		return
	}
	var cur unix.Rlimit
	if err := unix.Prlimit(pid, unix.RLIMIT_NOFILE, nil, &cur); err != nil {
		logger.Debug("fd-limit: read failed", "pid", pid, "err", err)
		return
	}
	want := target
	if want > cur.Max {
		// Stay within Bose's own hard cap. Raising the hard limit is
		// possible as root but would let the leak grow toward fs.file-max
		// and starve the system; not worth it.
		want = cur.Max
	}
	if cur.Cur >= want {
		return
	}
	newLim := unix.Rlimit{Cur: want, Max: cur.Max}
	if err := unix.Prlimit(pid, unix.RLIMIT_NOFILE, &newLim, nil); err != nil {
		logger.Warn("fd-limit: raise failed", "pid", pid, "err", err)
		return
	}
	logger.Info("fd-limit: raised BoseApp open-files soft limit",
		"pid", pid, "from", cur.Cur, "to", want, "hard", cur.Max)
}

// readUptime returns the box uptime from /proc/uptime, 0 on error.
func readUptime() time.Duration {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return time.Duration(secs * float64(time.Second))
}

// recentHangReboots returns how many hang-recovery reboots were recorded
// within hangLoopWindow before now, used as a loop guard.
func recentHangReboots(now time.Time) int {
	b, err := os.ReadFile(hangRebootMarker)
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Fields(string(b)) {
		ts, err := strconv.ParseInt(line, 10, 64)
		if err != nil {
			continue
		}
		if now.Sub(time.Unix(ts, 0)) < hangLoopWindow {
			n++
		}
	}
	return n
}

// recordHangReboot appends now to the marker file, keeping only entries
// within hangLoopWindow so it cannot grow without bound.
func recordHangReboot(now time.Time) {
	var kept []string
	if b, err := os.ReadFile(hangRebootMarker); err == nil {
		for _, line := range strings.Fields(string(b)) {
			if ts, err := strconv.ParseInt(line, 10, 64); err == nil {
				if now.Sub(time.Unix(ts, 0)) < hangLoopWindow {
					kept = append(kept, line)
				}
			}
		}
	}
	kept = append(kept, strconv.FormatInt(now.Unix(), 10))
	_ = os.WriteFile(hangRebootMarker, []byte(strings.Join(kept, "\n")+"\n"), 0o644)
}

// watchBoseAppHealth reboots the box when BoseApp's :8090 HTTP API is
// confirmed dead for ~6 minutes while BoseApp itself is still alive (the
// FD-exhaustion deadlock). Disabled by STR_NO_HANG_RECOVERY and by the
// loop guard. A reboot is the only known reset for the deadlock; FDs reset
// to baseline on a fresh boot.
func watchBoseAppHealth(ctx context.Context, boxHost string, logger *slog.Logger) {
	if boxHost == "" {
		return
	}
	if strings.TrimSpace(os.Getenv("STR_NO_HANG_RECOVERY")) != "" {
		logger.Info("boseapp health: hang-recovery disabled via STR_NO_HANG_RECOVERY")
		return
	}
	if n := recentHangReboots(time.Now()); n >= hangLoopMax {
		logger.Warn("boseapp health: hang-recovery suppressed, too many recent recovery reboots (loop guard)", "recent", n)
		return
	}

	client := &http.Client{Timeout: 5 * time.Second}
	url := fmt.Sprintf("http://%s:8090/info", boxHost)
	fails := 0
	t := time.NewTicker(hangProbeEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		// A dead BoseApp is the supervisor's job (it reboots for that); we
		// only handle the alive-but-hung case, so skip when it is absent.
		if findBoseAppPID() == 0 {
			fails = 0
			continue
		}
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			fails = 0
			continue
		}
		if readUptime() < hangMinUptime {
			continue // too early after boot to call it a hang
		}
		fails++
		logger.Warn("boseapp health: :8090 unresponsive while BoseApp alive", "consecutive", fails, "err", err)
		if fails >= hangFailsToReact {
			now := time.Now()
			recordHangReboot(now)
			logger.Error("boseapp health: :8090 deadlocked, rebooting box to recover",
				"consecutive", fails, "recentRecoveryReboots", recentHangReboots(now))
			if rerr := exec.Command("reboot").Run(); rerr != nil {
				logger.Error("boseapp health: reboot command failed", "err", rerr)
				fails = 0 // could not reboot; keep watching
				continue
			}
			return // box going down
		}
	}
}
