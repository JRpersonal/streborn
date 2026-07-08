package webui

// Last-resort OTA paths for a NAND that cannot take the update (#270).
//
// Tier 1 is the reclaim cascade in writeBinaryAtomic (drop stale temps, logs,
// the regenerable Spotify engine). Field data showed two eventualities it
// cannot handle:
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
// staged binary over it, verify by size (and SHA256 when the box has
// sha256sum), sync and reboot. One retry on a bad copy; the staged file is
// removed either way so RAM is not held across the reboot.
func swapHelperScript(agentPID int, stage, dst string, size int, sha string) string {
	return fmt.Sprintf(`i=0
while [ -d /proc/%d ] && [ "$i" -lt 90 ]; do sleep 1; i=$((i+1)); done
cp %q %q; sync
ok=1
[ "$(wc -c < %q 2>/dev/null | tr -d ' ')" = "%d" ] || ok=0
if [ "$ok" = "1" ] && command -v sha256sum >/dev/null 2>&1; then
  [ "$(sha256sum %q | cut -d' ' -f1)" = "%s" ] || ok=0
fi
if [ "$ok" != "1" ]; then cp %q %q; sync; fi
rm -f %q
sleep 1
reboot`,
		agentPID, stage, dst, dst, size, dst, sha, stage, dst, stage)
}

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
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = os.Remove(ramStagePath)
		return fmt.Errorf("start swap helper: %w", err)
	}
	// Deliberately not Wait()ed: the helper outlives us by design.
	s.logger.Warn("OTA: NAND cannot hold two agent copies; RAM-staged swap armed, agent will exit and the helper reboots the box",
		"stage", ramStagePath, "bytes", len(body), "helperPID", cmd.Process.Pid)
	return nil
}
