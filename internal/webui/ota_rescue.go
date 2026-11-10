package webui

// The rescue agent: what keeps a speaker on STR when a RAM-staged swap could
// not write the new agent to the NAND.
//
// By the time the swap helper (ota_lastresort.go) knows the copy failed, the
// in-place copy has already destroyed the old binary, so the NAND holds no
// runnable agent and the RAM stage is the only good copy left. The helper then
// moves the stage to ramRescueBin and starts it from there, under its own
// supervision, with run.sh's PID file pointing at it. That agent is the new
// build: it serves the app, reports otaSwapFailed (the marker) so the app can
// tell the update did not land and retry, and runs this file:
//
//   - one more attempt, two minutes after it started, to write itself onto
//     the NAND with the same verified copy the helper used (placeBinaryLines,
//     once more after a page-cache drop if the first one fails). A success
//     clears the marker; a failure leaves it, and nothing tries again on its
//     own. The app's next update push is the way on from there.
//   - no speaker restart initiated by the agent itself while the NAND copy is
//     not verified (RebootUnsafe): the bootstrap reboot, the memory guard and
//     the stuck-display restart all ask first. A restart ends the rescue
//     agent with the RAM it runs from, and the speaker would come back
//     without STR.
//
// It never reboots the speaker, and it never restarts itself onto the NAND
// copy either: the next restart, whenever it comes, starts the same build
// from the NAND.

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// ramRescueBinPath is where a failed swap moves the stage and starts it from
// (otaPaths.ramRescueBin on a speaker). Its own directory, so the process keeps
// the agent's name (killall and pidof streborn-armv7l find it, as run.sh and
// the desktop app's stops expect).
const ramRescueBinPath = ramStageDir + "/streborn-rescue/streborn-armv7l"

// rescueRetryDelay is how long the rescue agent runs before it tries the write
// itself, long enough for the speaker to settle after the failed swap and for
// the app to have looked at it.
const rescueRetryDelay = 2 * time.Minute

// rescueRetryTimeout bounds the retry script (two copies of ~18 MB onto a slow
// NAND, each with a sync and a verify).
const rescueRetryTimeout = 5 * time.Minute

// rescueState is what the rescue agent knows about its own NAND copy, part of
// otaState.
type rescueState struct {
	// nandCopyVerified is set once the rescue agent's build is proven on the
	// NAND (by its retry, or found there already). Atomic: the retry's
	// goroutine sets it, the reboot guards of cmd/agent read it.
	nandCopyVerified atomic.Bool
}

// runRescueShell runs a retry script on a speaker (otaEffects.rescueShell).
func runRescueShell(ctx context.Context, script string) ([]byte, error) {
	return exec.CommandContext(ctx, "sh", "-c", script).CombinedOutput()
}

// The four functions cmd/agent calls have no Server at hand (the retry is
// scheduled, and the reboot guards ask, beside it), so they work on the
// process's own update state and the production effects. Each is a one-line
// wrapper around the function the tests drive with their own.

// RunningAsRescue reports whether this agent is the rescue agent a failed swap
// started from RAM.
func RunningAsRescue() bool { return runningAsRescue(&productionOTAEffects) }

// runningAsRescue is RunningAsRescue for the given effects.
func runningAsRescue(fx *otaEffects) bool {
	r := fx.runningBinary()
	return r != "" && filepath.Clean(r) == filepath.Clean(fx.paths.ramRescueBin)
}

// RebootUnsafe returns why a speaker restart the agent starts on its own would
// leave the speaker without STR, or "" when it would not. Callers that reboot
// without being asked to (bootstrap refresh, memory guard, stuck display) skip
// the reboot and log this instead.
func RebootUnsafe() string { return rebootUnsafe(processOTAState, &productionOTAEffects) }

// rebootUnsafe is RebootUnsafe for the given state and effects.
func rebootUnsafe(st *otaState, fx *otaEffects) string {
	if runningAsRescue(fx) && !st.nandCopyVerified.Load() {
		return "the agent runs from RAM because the last update could not be written to the speaker's storage; a restart now would leave the speaker without STR"
	}
	return ""
}

// StartRescueRetry schedules the rescue agent's one retry. A no-op for any
// other agent. ctx ends it early (agent shutdown).
func StartRescueRetry(ctx context.Context, logger *slog.Logger) {
	startRescueRetry(ctx, processOTAState, &productionOTAEffects, logger)
}

// startRescueRetry is StartRescueRetry for the given state and effects.
func startRescueRetry(ctx context.Context, st *otaState, fx *otaEffects, logger *slog.Logger) {
	if !runningAsRescue(fx) {
		return
	}
	logger.Warn("running from RAM as the rescue agent: the last update could not be written to the speaker's storage; trying once more shortly, no speaker restarts until it is there",
		"binary", fx.paths.ramRescueBin, "dst", fx.paths.rescueNANDDst, "retryInSec", int(rescueRetryDelay.Seconds()))
	go func() {
		t := time.NewTimer(rescueRetryDelay)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		rescueRetry(ctx, st, fx, logger)
	}()
}

// rescueRetry writes the running build onto the NAND once, and reports whether
// it is there afterwards.
func rescueRetry(ctx context.Context, st *otaState, fx *otaEffects, logger *slog.Logger) bool {
	if st.updating() || st.swapArmed.Load() {
		logger.Info("rescue agent: an update is in progress, leaving the write to it")
		return false
	}
	sum := fx.rescueSum()
	if sum == "" {
		logger.Warn("rescue agent: cannot hash the running binary, not writing it to storage")
		return false
	}
	dst := fx.paths.rescueNANDDst
	if verifyBinaryOnFlash(dst, sum) == nil {
		rescueWritten(st, fx, logger, dst, "it was already there")
		return true
	}
	src := fx.paths.ramRescueBin
	fi, err := os.Stat(src)
	if err != nil {
		// The file went (an agent of an older build cleaned it up); the
		// process still has it.
		src = "/proc/" + strconv.Itoa(os.Getpid()) + "/exe"
		fi, err = os.Stat(src)
	}
	if err != nil {
		logger.Warn("rescue agent: cannot read its own binary, not writing it to storage", "err", err)
		return false
	}
	if n := killAgentsRunningFrom(fx.paths.procRoot, dst); n > 0 {
		logger.Warn("rescue agent: stopped agents run.sh had started from the unverified storage copy", "count", n)
	}
	script := placeBinaryLines(src, dst, fi.Size(), sum) + `
place_binary && exit 0
sync
{ echo 3 > /proc/sys/vm/drop_caches; } 2>/dev/null
sleep 2
place_binary`
	start := time.Now()
	cctx, cancel := context.WithTimeout(ctx, rescueRetryTimeout)
	out, err := fx.rescueShell(cctx, script)
	cancel()
	if err == nil {
		// The script verified already; this is the agent's own word on it.
		err = verifyBinaryOnFlash(dst, sum)
	}
	if err != nil {
		// Same rule as the helper: an unverified copy is not left where run.sh
		// starts the agent from.
		_ = os.Remove(dst)
		_ = os.Remove(dst + ".new")
		writeSwapFailMarker(fx.paths.swapFailMarker, swapFailedWhy+"; the agent's own retry at "+
			time.Now().UTC().Format(time.RFC3339)+" failed too")
		logger.Error("rescue agent: writing the update to the speaker's storage failed again; STR keeps running from RAM, a restart would lose it until the update is sent again",
			"err", err, "out", strings.TrimSpace(string(out)), "ms", time.Since(start).Milliseconds())
		return false
	}
	rescueWritten(st, fx, logger, dst, "the retry wrote it")
	return true
}

// rescueWritten records that the running build is proven on the NAND.
func rescueWritten(st *otaState, fx *otaEffects, logger *slog.Logger, dst, how string) {
	st.nandCopyVerified.Store(true)
	_ = os.Remove(fx.paths.swapFailMarker)
	logger.Info("rescue agent: the update is on the speaker's storage now ("+how+"); still running from RAM until the next restart, which starts the same build from storage",
		"dst", dst)
}

// writeSwapFailMarker replaces the marker text, synced. Best-effort: a full
// NAND may refuse it.
func writeSwapFailMarker(marker, msg string) {
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return
	}
	_, _ = f.WriteString(msg + "\n")
	_ = f.Sync()
	_ = f.Close()
}

// killAgentsRunningFrom SIGKILLs every other process whose executable is path
// (a watchdog respawn of the unverified NAND copy pins its blocks), and
// returns how many it signalled. By executable, not by name: the rescue agent
// has the same name.
func killAgentsRunningFrom(procRoot, path string) int {
	ents, err := os.ReadDir(procRoot)
	if err != nil {
		return 0
	}
	want := filepath.Clean(path)
	self := os.Getpid()
	n := 0
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		exe, err := os.Readlink(filepath.Join(procRoot, e.Name(), "exe"))
		if err != nil || filepath.Clean(strings.TrimSuffix(exe, " (deleted)")) != want {
			continue
		}
		if p, err := os.FindProcess(pid); err == nil && p.Kill() == nil {
			n++
		}
	}
	return n
}
