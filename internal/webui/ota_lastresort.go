package webui

// Last-resort OTA paths for a NAND that cannot take the update (#270).
//
// Tier 1 is the reclaim cascade in writeBinaryAtomic (drop stale temps, logs,
// the regenerable Spotify engine), followed by an OPTIMISTIC write attempt:
// the pessimistic UBIFS statfs figure steers the cascade but never refuses the
// write, so only a real ENOSPC from the filesystem escalates further. Field
// data showed two eventualities tier 1 cannot handle:
//
//   - UBIFS flips the volume to READ-ONLY after an I/O error (its protective
//     mode on aging NAND). Every delete in the cascade then fails silently,
//     the engine survives, and the user sees an unexplainable "no space"
//     (the #270 ST20: the inventory still carried the full engine after the
//     reclaim). Tier 2 detects the write-protected state, tries a
//     remount,rw, and retries once; if the volume stays read-only, the error
//     says so and tells the user the one thing that helps (power-cycle).
//
//   - Even a successful reclaim cannot beat the DOUBLE-COPY requirement: the
//     atomic .new+rename needs old + new agent side by side while the old
//     binary's blocks are pinned by the running process. On a small volume
//     (ST20: ~26 MB) that can be impossible no matter what is reclaimed.
//     Tier 3 sidesteps it: stage the new binary in RAM (/dev/shm tmpfs,
//     sized at half the box RAM), spawn a detached helper, exit the agent
//     (releasing ETXTBSY and its pinned blocks), and let the helper copy
//     RAM -> NAND, verify, sync and reboot. Peak NAND need drops to a single
//     copy. run.sh starts the swapped binary at the next boot, so even a
//     helper that dies leaves the box recoverable by a power-cycle.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// ramStageDir is the tmpfs the last-resort swap stages into. /dev/shm is
// mounted rw with no size cap (defaults to half the RAM) on every observed
// SoundTouch firmware; the boxes have no dedicated /tmp (rootfs is ro).
const ramStageDir = "/dev/shm"

// ramStagePath is the staged new agent binary awaiting the swap.
const ramStagePath = ramStageDir + "/streborn-ota.stage"

// isReadOnlyFSErr reports whether err smells like EROFS. The syscall error is
// matched directly and by message, because it usually arrives wrapped in
// fs.PathError -> fmt.Errorf chains.
func isReadOnlyFSErr(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, syscall.EROFS) ||
		strings.Contains(err.Error(), "read-only file system")
}

// nandWritable probes whether the NAND volume actually accepts writes right
// now. df/statfs stays happy on a read-only UBIFS, so a real write is the
// only trustworthy probe.
func nandWritable() bool {
	p := nandRoot + "/.str-write-probe"
	if err := os.WriteFile(p, []byte("w"), 0o600); err != nil {
		return false
	}
	_ = os.Remove(p)
	return true
}

// remountNANDRW asks the kernel to flip the NAND volume back to read-write
// after UBIFS parked it read-only. Best-effort; the outcome is logged and
// returned so the caller can retry or fail with a truthful message.
func remountNANDRW(logger *slog.Logger) bool {
	out, err := exec.Command("mount", "-o", "remount,rw", nandRoot).CombinedOutput()
	if err != nil {
		logger.Warn("NAND remount,rw failed", "err", err, "out", strings.TrimSpace(string(out)))
		return false
	}
	ok := nandWritable()
	logger.Info("NAND remount,rw attempted", "writableAfter", ok)
	return ok
}

// errNANDReadOnly is returned when the NAND is write-protected and could not
// be remounted read-write. The message is surfaced verbatim to the user.
var errNANDReadOnly = errors.New(
	"the speaker's storage is in a write-protected state and could not be re-enabled; " +
		"unplug the speaker from power for a minute and run the update again")

// swapHelperScript builds the BusyBox-sh script the detached helper runs:
// wait for this agent process to exit (releasing the old binary), copy the
// staged binary over it, flash-verify, sync and reboot.
//
// Hardened for v0.9.7 (#381 class): the old script copied even when the agent
// was STILL ALIVE after the 90 s wait — cp then failed with ETXTBSY leaving
// the OLD binary in place, and the size-only verify could not notice because
// consecutive releases are byte-equal in size (v0.9.4/5/6 are all exactly
// 12517538 bytes). It then rebooted a box that had already answered 200 OK,
// so the app saw a "successful" update that never happened. Now:
//   - the agent gets kill -9 escalation after the wait; if it STILL will not
//     die, the helper ABORTS without rebooting (run.sh's watchdog respawns
//     the old agent, the box stays usable, the app's version poll reports
//     the truth) and leaves a marker for diagnostics.
//   - verification is content-based: sha256sum when present, else cmp
//     against the still-present RAM stage; the size check is only the
//     last-resort fallback. The re-read goes through a sync + page-cache
//     drop so it proves flash, not RAM (#302 lesson).
//   - one retry (after killing any watchdog-respawned agent that re-pinned
//     the binary); on a second failure it aborts without rebooting and
//     leaves the marker instead of rebooting into a silently-old binary.
func swapHelperScript(agentPID int, stage, dst string, size int, sha string) string {
	return fmt.Sprintf(`i=0
while [ -d /proc/%d ] && [ "$i" -lt 90 ]; do sleep 1; i=$((i+1)); done
if [ -d /proc/%d ]; then kill -9 %d 2>/dev/null; sleep 3; fi
if [ -d /proc/%d ]; then
  echo "swap aborted: agent PID %d still alive after kill -9" > %q; sync
  rm -f %q
  exit 1
fi
verify_flash() {
  sync
  echo 3 > /proc/sys/vm/drop_caches 2>/dev/null
  if command -v sha256sum >/dev/null 2>&1; then
    [ "$(sha256sum %q | cut -d' ' -f1)" = "%s" ]
  elif command -v cmp >/dev/null 2>&1; then
    cmp -s %q %q
  else
    [ "$(wc -c < %q 2>/dev/null | tr -d ' ')" = "%d" ]
  fi
}
cp %q %q; sync
if ! verify_flash; then
  killall -9 streborn-armv7l 2>/dev/null
  sleep 2
  cp %q %q; sync
  if ! verify_flash; then
    echo "swap failed: flash verify mismatch after retry" > %q; sync
    rm -f %q
    exit 1
  fi
fi
rm -f %q %q
sync
sleep 1
reboot`,
		agentPID,
		agentPID, agentPID,
		agentPID,
		agentPID, swapFailMarker,
		stage,
		dst, sha,
		stage, dst,
		dst, size,
		stage, dst,
		stage, dst,
		swapFailMarker,
		stage,
		stage, swapFailMarker)
}

// swapFailMarker is where the swap helper records an aborted/failed tier-3
// swap so the next agent start (and /api/agent/version) can surface it
// instead of the failure staying invisible on a stickless box.
const swapFailMarker = "/mnt/nv/streborn/ota-swap-failed"

// stageAndSwapViaRAM implements tier 3: write body to the RAM stage, spawn
// the detached swap helper, and return nil when the caller may answer the
// client and exit the agent process. The caller MUST exit shortly after (the
// helper waits for this PID, capped at 90 s).
func (s *Server) stageAndSwapViaRAM(dst string, body []byte) error {
	if _, avail, ok := diskFree(ramStageDir); ok && avail < int64(len(body))+(1<<20) {
		return fmt.Errorf("RAM stage %s too small: need %d, avail %d", ramStageDir, len(body), avail)
	}
	if err := os.WriteFile(ramStagePath, body, 0o755); err != nil {
		return fmt.Errorf("write RAM stage: %w", err)
	}
	sum := sha256.Sum256(body)
	script := swapHelperScript(os.Getpid(), ramStagePath, dst, len(body), hex.EncodeToString(sum[:]))
	cmd := exec.Command("sh", "-c", script)
	// Own session: the helper must survive this agent's exit and any process-
	// group teardown on the way down.
	cmd.SysProcAttr = sysProcAttrSetsid()
	if err := cmd.Start(); err != nil {
		_ = os.Remove(ramStagePath)
		return fmt.Errorf("start swap helper: %w", err)
	}
	// Deliberately not Wait()ed: the helper outlives us by design.
	s.logger.Warn("OTA: NAND cannot hold two agent copies; RAM-staged swap armed, agent will exit and the helper reboots the box",
		"stage", ramStagePath, "bytes", len(body), "helperPID", cmd.Process.Pid)
	return nil
}
