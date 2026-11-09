package webui

// Last-resort OTA paths for a NAND that cannot take the update (#270).
//
// Tier 1 is the reclaim cascade in prepareBinaryWrite (drop stale temps, logs,
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
//     copy. run.sh starts the swapped binary at the next boot. On a
//     coprocessor Wi-Fi ST20 the helper starts the new agent instead of
//     rebooting (otarestart.go).
//
//     The in-place copy is the one step that can leave the NAND without a
//     runnable agent: from its first byte the old binary is gone and only the
//     RAM stage holds a good one. So a copy that does not verify never ends
//     the swap: the helper starts the staged build from RAM (the rescue
//     agent, ota_rescue.go), which keeps the speaker on STR, reports
//     otaSwapFailed so the app can retry, and itself tries the write once
//     more two minutes later.

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path"
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

// swapHelper describes one tier-3 swap: the detached BusyBox-sh helper waits
// for this agent process to exit (releasing the old binary), places the staged
// binary on the NAND, flash-verifies it and runs finish. When the binary
// cannot be placed it keeps the speaker on STR instead: it starts the staged
// build from RAM (the rescue agent, ota_rescue.go) and never runs finish.
//
// Hardened for v0.9.7 (#381 class): the old script copied even when the agent
// was STILL ALIVE after the 90 s wait — cp then failed with ETXTBSY leaving
// the OLD binary in place, and the size-only verify could not notice because
// consecutive releases are byte-equal in size (v0.9.4/5/6 are all exactly
// 12517538 bytes). It then rebooted a box that had already answered 200 OK,
// so the app saw a "successful" update that never happened. Now:
//   - the agent gets kill -9 escalation after the wait; if it STILL will not
//     die, the helper ABORTS without touching the NAND (run.sh's watchdog
//     respawns the old agent, the box stays usable, the app's version poll
//     reports the truth) and leaves a marker for diagnostics.
//   - verification is content-based: sha256sum when present, else cmp
//     against the still-present RAM stage; the size check is only the
//     last-resort fallback. The re-read goes through a sync + page-cache
//     drop so it proves flash, not RAM (#302 lesson).
//   - one retry, after killing any watchdog-respawned agent that re-pinned
//     the binary and dropping the page cache.
//
// Until v1.5.2 a second failed verify wrote the marker, DELETED the stage and
// exited. The copy had already gone over the live NAND binary by then, so the
// stage was the only good copy left: the NAND held a partial or corrupt agent,
// run.sh's watchdog kept respawning that broken binary, :8888 refused, and a
// speaker without a USB stick had lost STR (field report 2026-11-09, an ST10
// with a tight NAND). The cp exit status was not checked either. The copy is
// now placeBinaryLines' and a failure ends in rescueLines.
type swapHelper struct {
	agentPID int
	// waitSec is how long the helper waits for the agent before kill -9
	// (swapHelperWaitSec, longer when the agent first waits out run.sh's boot
	// watchdog).
	waitSec int
	stage   string
	dst     string
	size    int64
	sha     string
	// finish is the last step after a passing verify: "reboot", or on a
	// speaker that restarts only the agent after an update (otarestart.go),
	// the lines that start and supervise the new binary. A failed swap never
	// reaches it.
	finish string
	// rescueBin is where a failed swap moves the stage and starts it from;
	// args are the flags it gets (run.sh's, as for the restarted agent).
	rescueBin string
	args      []string
}

// swapFailedWhy is the marker text of a swap whose binary could not be placed.
const swapFailedWhy = "swap failed: the new agent could not be written to the speaker's storage (flash verify failed twice); STR keeps running from RAM until a retry writes it"

// script builds the helper.
func (h swapHelper) script() string {
	return fmt.Sprintf(`i=0
while [ -d /proc/%[1]d ] && [ "$i" -lt %[2]d ]; do sleep 1; i=$((i+1)); done
if [ -d /proc/%[1]d ]; then kill -9 %[1]d 2>/dev/null; sleep 3; fi
if [ -d /proc/%[1]d ]; then
  echo "swap aborted: agent PID %[1]d still alive after kill -9" > %[3]q; sync
  rm -f %[4]q
  exit 1
fi
%[5]s
killall -9 streborn-armv7l 2>/dev/null
ok=0
if place_binary; then
  ok=1
else
  killall -9 streborn-armv7l 2>/dev/null
  sync
  { echo 3 > /proc/sys/vm/drop_caches; } 2>/dev/null
  sleep 2
  if place_binary; then ok=1; fi
fi
if [ "$ok" != 1 ]; then
%[6]s
  exit 1
fi
rm -f %[4]q %[3]q
sync
sleep 1
%[7]s`,
		h.agentPID, h.waitSec, swapFailMarker, h.stage,
		placeBinaryLines(h.stage, h.dst, h.size, h.sha),
		rescueLines(h.stage, h.dst, h.rescueBin, h.args, swapFailedWhy),
		h.finish)
}

// placeBinaryLines defines the two sh functions every copy of a RAM-staged
// agent onto the NAND goes through, the helper's and the rescue agent's own
// retry alike: verify_flash FILE, and place_binary, which returns 0 only once
// dst holds the staged binary as proven by a read past the page cache.
//
// place_binary prefers a copy that cannot leave a half-written dst: when df
// reports room for the whole binary plus 1 MB next to the old one, it writes
// dst.new, verifies it and renames it over dst. Tier 3 exists because that
// room is usually missing, so the fallback writes in place, freeing first:
// rm dst and sync (the sync commits the deletion, so UBIFS counts the old
// blocks as free before the copy asks for them), then cp with its exit status
// checked, then the verify. A failed in-place copy leaves dst partial; what
// happens to it is the caller's decision. It never kills a process, so the
// rescue agent can run it against its own NAND path.
func placeBinaryLines(stage, dst string, size int64, sha string) string {
	needKB := (size+1023)/1024 + 1024
	return fmt.Sprintf(`verify_flash() {
  sync
  { echo 3 > /proc/sys/vm/drop_caches; } 2>/dev/null
  if command -v sha256sum >/dev/null 2>&1; then
    [ "$(sha256sum "$1" 2>/dev/null | cut -d' ' -f1)" = "%[4]s" ]
  elif command -v cmp >/dev/null 2>&1; then
    cmp -s %[1]q "$1"
  else
    [ "$(wc -c < "$1" 2>/dev/null | tr -d ' ')" = "%[5]d" ]
  fi
}
place_binary() {
  rm -f %[3]q
  avail=$(df -k %[6]q 2>/dev/null | awk 'NR>1 && NF>=4 {a=$(NF-2)} END {print a+0}')
  if [ "${avail:-0}" -ge %[7]d ] 2>/dev/null; then
    if cp %[1]q %[3]q && chmod 755 %[3]q && verify_flash %[3]q && mv -f %[3]q %[2]q; then
      if verify_flash %[2]q; then return 0; fi
    fi
    rm -f %[3]q
    sync
  fi
  rm -f %[2]q
  sync
  cp %[1]q %[2]q || return 1
  chmod 755 %[2]q 2>/dev/null
  verify_flash %[2]q
}`, stage, dst, dst+".new", sha, size, path.Dir(dst), needKB)
}

// rescueLines is the end of a swap whose binary could not be placed: record
// why, remove the unverified dst, and start the staged build from RAM as the
// rescue agent under the helper's supervision. Never a reboot, never finish.
//
// The stage is moved (same tmpfs, no copy) to rescueBin, so the shared stage
// path is free for the next update and the process keeps the agent's name:
// run.sh's and the desktop app's process-name stops (killall/pidof
// streborn-armv7l) find it like any agent. Should the move fail it starts from
// the stage path itself. The stage is never deleted here.
//
// The unverified dst goes because it is a partial or corrupt agent (the copy
// over the old one has already happened, so it is never the old binary) and
// run.sh would otherwise keep starting it: with dst absent a watchdog respawn
// fails at exec instead of running a corrupt agent next to the rescue one, and
// the NAND gets the room back for the PID file and the rescue agent's retry.
// The helper then claims run.sh's PID file (on a rebooting speaker the old
// agent did not hand it over) and the supervise lines put the rescue agent's
// PID in it, so the watchdog sees a live agent and starts nothing.
func rescueLines(stage, dst, rescueBin string, args []string, why string) string {
	return fmt.Sprintf(`  { echo %[1]q > %[2]q; } 2>/dev/null
  killall -9 streborn-armv7l 2>/dev/null
  rm -f %[3]q %[4]q
  sync
  run=%[5]q
  if mkdir -p %[6]q 2>/dev/null && mv -f %[5]q %[7]q 2>/dev/null; then run=%[7]q; fi
  { echo "$$" > %[8]q; } 2>/dev/null
  sync
%[9]s`, why, swapFailMarker, dst, dst+".new", stage, path.Dir(rescueBin), rescueBin, agentPIDFile,
		superviseAgentLines(`"$run"`, args))
}

// swapFailMarker is where the swap helper records an aborted/failed tier-3
// swap so the next agent start (and /api/agent/version) can surface it
// instead of the failure staying invisible on a stickless box. A variable so
// the shell tests can point it at a temp dir.
var swapFailMarker = "/mnt/nv/streborn/ota-swap-failed"

// swapHelperWaitSec is how long the swap helper waits for the agent to exit
// before it escalates to kill -9.
const swapHelperWaitSec = 90

// armRAMStagedSwap implements tier 3 once the upload is complete in the RAM
// stage (receiveAgentBinary moved it there when the NAND filled up): spawn the
// detached swap helper and return nil when the caller may answer the client
// and exit the agent process. The caller MUST exit shortly after (the helper
// waits for this PID, capped at swapHelperWaitSec, plus handOverDelay on a
// restart-only speaker). size and sum describe the staged file;
// the helper checks both before it touches dst.
//
// restartOnly reports that the helper ends by starting the new agent instead
// of rebooting (otarestart.go); the caller then hands run.sh's PID file to
// helperPID before it exits, so the watchdog neither respawns the old binary
// during the copy nor starts a second agent after it.
func (s *Server) armRAMStagedSwap(dst string, size int64, sum string) (helperPID int, restartOnly bool, err error) {
	fi, err := os.Stat(ramStageTarget)
	if err != nil {
		return 0, false, fmt.Errorf("RAM stage %s: %w", ramStageTarget, err)
	}
	if fi.Size() != size {
		return 0, false, fmt.Errorf("RAM stage %s holds %d bytes, the upload was %d", ramStageTarget, fi.Size(), size)
	}
	finish, waitSec := "reboot", swapHelperWaitSec
	restartOnly = s.otaRestartOnly(dst)
	if restartOnly {
		finish = superviseNewAgentLines(dst, agentArgs())
		// The agent waits out run.sh's boot watchdog before it exits
		// (waitOutBootWatchdog); the helper must not kill it for that.
		waitSec += int(handOverDelay().Seconds()) + 1
	}
	script := swapHelper{
		agentPID: os.Getpid(), waitSec: waitSec,
		stage: ramStageTarget, dst: dst, size: size, sha: sum,
		finish:    finish,
		rescueBin: ramRescueBin, args: agentArgs(),
	}.script()
	// Own session: the helper must survive this agent's exit and any process-
	// group teardown on the way down. Deliberately not Wait()ed: the helper
	// outlives us by design.
	helperPID, err = otaStartDetached(script)
	if err != nil {
		return 0, false, fmt.Errorf("start swap helper: %w", err)
	}
	swapArmed.Store(true)
	then := "the helper reboots the box"
	if restartOnly {
		then = "the helper starts the new agent, no box reboot"
	}
	s.logger.Warn("OTA: NAND cannot hold two agent copies; RAM-staged swap armed, agent will exit and "+then,
		"stage", ramStageTarget, "bytes", size, "helperPID", helperPID)
	return helperPID, restartOnly, nil
}
