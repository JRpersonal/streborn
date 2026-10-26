package webui

// RAM-first receive for a tight speaker.
//
// The deferred engine drop (prepareBinaryWriteDeferred + engineReclaimGate)
// keeps the Spotify engine until the body has filled the NAND to the margin.
// On a speaker that is only a little short that buys almost nothing: a BCO ST
// with 4.9 MB free and a 17.5 MB agent reached the margin after 1.8 MB, the
// engine went, and the transfer died at 3 MB anyway (2026-10-26). The speaker
// kept its old agent and lost Spotify, the exact outcome the deferral was for.
//
// So when statfs predicts the shortage and the speaker has the memory, the
// whole body goes into a RAM stage (the tmpfs the tier-3 swap already uses)
// first, hashed as it streams. Only once the last byte is in and the size
// checks pass is the engine dropped (reclaimEngineForWrite, with the engine
// stop hook and the dropped-for-update marker), and the staged binary is then
// copied onto the NAND temp path and installed exactly like a normal receive.
// A transfer that breaks off at any point before that leaves the engine and
// its marker alone and removes the stage.
//
// If the copy onto the NAND runs out of space even after the drop, the binary
// is still complete in the RAM stage, which is precisely what the tier-3 swap
// (armRAMStagedSwap) installs from, so the stage is handed over to the swap
// path and the result is reported as ramStaged instead of failing.
//
// Every RAM-first receive stages under its OWN name (ramFirstStagePrefix plus
// the PID and a sequence number), never at the shared swap path. The server
// sets no ReadTimeout, so a request whose link died can sit in its read until
// TCP gives up while the app's retry is already streaming; with one shared path
// the two would delete each other's stage, and the survivor would drop the
// engine and then find no stage to copy. A stage stays registered as in use
// until its install has passed the flash verify (releaseRAMFirstStage), so the
// leftover cleanup never takes a stage another receive still owns.

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ramFirstMemFloor is the MemAvailable that must remain AFTER the whole body
// sits in the RAM stage, or the route is not taken.
//
// tmpfs pages cannot be reclaimed and the speakers have no swap, so every
// staged byte comes straight out of MemAvailable until the stage is removed.
// What is left has to carry the rest of the install: the dirty page cache of
// the RAM->NAND copy, the 512 KB flash-verify window, Go heap growth (GOGC lets
// the heap double before a collection) and whatever BoseApp does meanwhile.
// The field marks where that runs out: #1083 sat at 7.5 MB MemAvailable on the
// cliff, and the 2026-08-22 ST20 that was OOM-killed during the flash verify
// had about 19 MB left and then asked for another 14.6 MB, i.e. under 5 MB of
// real room. 16 MB is twice the lowest figure a speaker came back from, and
// still lets the measured speakers in (45-51 MB MemAvailable, a 17.5 MB agent
// leaves 27-33 MB after the stage). The engine is stopped right after the stage
// completes, which hands back its RSS on top of that.
const ramFirstMemFloor = 16 << 20

// ramFirstStageSlack is the room the RAM stage directory must report beyond
// the binary itself, the same slack spillToRAMStage asks for.
const ramFirstStageSlack = 1 << 20

// ramFirstStagePrefix names the per-receive RAM-first stages, next to the
// swap's ramStageTarget in the same tmpfs.
const ramFirstStagePrefix = "streborn-ota.rf-"

// ramStageAvailFn and memAvailableKBFn are the two figures the route decision
// reads, behind seams so tests can script them.
var (
	ramStageAvailFn = func(dir string) (int64, bool) {
		_, avail, ok := diskFree(dir)
		return avail, ok
	}
	memAvailableKBFn = func() int64 {
		avail, _ := uploadMemKB()
		return avail
	}
)

var (
	// ramFirstSeq numbers the RAM-first stages of this process.
	ramFirstSeq atomic.Uint64
	// ramFirstActive holds the RAM-first stages a receive or an install still
	// owns; the leftover cleanup skips them.
	ramFirstActiveMu sync.Mutex
	ramFirstActive   = map[string]bool{}
	// agentReceivesActive counts the agent receives in progress in this
	// process. The shared swap stage is only cleaned up by a receive that is
	// the only one, since a concurrent spill may be writing it.
	agentReceivesActive atomic.Int32
	// swapArmed is set once a tier-3 swap helper has been started: from then on
	// the shared swap stage belongs to that helper and this process neither
	// removes nor replaces it.
	swapArmed atomic.Bool
	// ramFirstStageFailed is set when writing a RAM-first stage failed on the
	// speaker's side (a full tmpfs, typically). The rest of this agent's life
	// takes the deferred route, so the app's retries do not keep running into
	// the same wall.
	ramFirstStageFailed atomic.Bool
)

// stageToNAND copies the complete RAM stage onto the NAND temp file and syncs
// it. A variable so a test can make the NAND refuse the copy.
var stageToNAND = func(stage, tmp string, buf []byte) error {
	in, err := os.Open(stage)
	if err != nil {
		return fmt.Errorf("open RAM stage: %w", err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("open tmp: %w", err)
	}
	_, err = io.CopyBuffer(out, in, buf)
	if err == nil {
		// fsync before close, as the streamed write does (#381).
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// newRAMFirstStage returns a fresh stage path for one receive and registers it
// as in use.
func newRAMFirstStage() string {
	p := filepath.Join(filepath.Dir(ramStageTarget),
		fmt.Sprintf("%s%d-%d", ramFirstStagePrefix, os.Getpid(), ramFirstSeq.Add(1)))
	ramFirstActiveMu.Lock()
	ramFirstActive[p] = true
	ramFirstActiveMu.Unlock()
	return p
}

// releaseRAMFirstStage removes a RAM-first stage and drops its registration.
// Safe to call with "" and more than once.
func releaseRAMFirstStage(p string) {
	if p == "" {
		return
	}
	_ = os.Remove(p)
	forgetRAMFirstStage(p)
}

// forgetRAMFirstStage drops the registration of a stage without touching any
// file (used once the stage has been renamed onto the swap path).
func forgetRAMFirstStage(p string) {
	ramFirstActiveMu.Lock()
	delete(ramFirstActive, p)
	ramFirstActiveMu.Unlock()
}

// removeStaleRAMFirstStages removes the RAM-first stages in dir that no
// receive of this process owns, and returns how many it removed.
func removeStaleRAMFirstStages(dir string) int {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	removed := 0
	ramFirstActiveMu.Lock()
	defer ramFirstActiveMu.Unlock()
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), ramFirstStagePrefix) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if ramFirstActive[p] {
			continue
		}
		if os.Remove(p) == nil {
			removed++
		}
	}
	return removed
}

// procRoot is where swapHelperRunning looks for processes. A variable so a
// test can script it.
var procRoot = "/proc"

// swapHelperRunning reports whether another process's command line names the
// swap stage, which is how the detached tier-3 helper (sh -c <script>) shows
// up. When the process table cannot be read it answers true, so the caller
// keeps the stage.
func swapHelperRunning(stage string) bool {
	ents, err := os.ReadDir(procRoot)
	if err != nil {
		return true
	}
	self := fmt.Sprint(os.Getpid())
	needle := []byte(stage)
	for _, e := range ents {
		name := e.Name()
		if name == "" || name[0] < '0' || name[0] > '9' || name == self {
			continue
		}
		b, err := os.ReadFile(filepath.Join(procRoot, name, "cmdline"))
		if err == nil && bytes.Contains(b, needle) {
			return true
		}
	}
	return false
}

// CleanStaleRAMStages runs once at agent start. An agent that was killed or
// crashed mid-receive leaves its stage in tmpfs, where it cannot be reclaimed
// (the speakers have no swap) and would hold up to ~18 MB of the memory the
// respawned agent and BoseApp run in. Every RAM-first stage goes, since no
// receive of this new process owns one yet. The shared swap stage goes only
// when no swap helper is reading it: the helper waits for the old agent to
// exit, and a watchdog respawn can start this agent while it still copies.
func CleanStaleRAMStages(logger *slog.Logger) {
	dir := filepath.Dir(ramStageTarget)
	n := removeStaleRAMFirstStages(dir)
	swapStage := false
	if _, err := os.Stat(ramStageTarget); err == nil && !swapHelperRunning(ramStageTarget) {
		swapStage = os.Remove(ramStageTarget) == nil
	}
	if n > 0 || swapStage {
		logger.Info("removed RAM stages left behind by an earlier agent",
			"ramFirstStages", n, "swapStage", swapStage, "dir", dir)
	}
}

// ramFirstRoute decides, for a write where statfs predicted a NAND shortage,
// whether the body is received into RAM first (true) or streamed onto the
// NAND with the deferred engine drop (false). Every decision is logged.
func ramFirstRoute(need int64) bool {
	stageDir := filepath.Dir(ramStageTarget)
	// Stages left behind by an earlier attempt would only make the figures
	// lie. The stage of a receive still running (a stalled request whose retry
	// this is) is kept, and the shared swap stage is only removed when no other
	// receive is running and no swap is armed.
	removeStaleRAMFirstStages(stageDir)
	if agentReceivesActive.Load() <= 1 && !swapArmed.Load() {
		_ = os.Remove(ramStageTarget)
	}
	stageAvail, stageOK := ramStageAvailFn(stageDir)
	memKB := memAvailableKBFn()
	reason := ""
	switch {
	case need <= 0:
		reason = "upload size unknown"
	case ramFirstStageFailed.Load():
		reason = "an earlier RAM-first stage write failed"
	case !stageOK:
		reason = "RAM stage free space unknown"
	case stageAvail < need+ramFirstStageSlack:
		reason = "RAM stage too small"
	case memKB < 0:
		reason = "MemAvailable unknown"
	case memKB*1024 < need+ramFirstMemFloor:
		reason = "MemAvailable would fall below the floor once the body is staged"
	}
	route := "ram-first"
	if reason != "" {
		route = "deferred"
	}
	slog.Info("OTA write: statfs predicts a NAND shortage, choosing the receive route",
		"route", route, "reason", reason, "needKB", need/1024,
		"memAvailableKB", memKB, "memFloorKB", ramFirstMemFloor/1024,
		"stageDir", stageDir, "stageAvailKB", stageAvail/1024, "stageAvailKnown", stageOK)
	return reason == ""
}

// errRAMFirstStageWrite marks a failure writing the RAM-first stage itself,
// as opposed to reading the body.
var errRAMFirstStageWrite = errors.New("RAM-first stage")

// receiveRAMFirst receives the whole body into its own RAM stage, then drops
// the engine, copies the stage onto tmp and installs it over dst. See the file
// comment for the order and why. On success the stage is still in place and
// named by receivedAgent.ramFirstStage: the caller keeps it until the flash
// verify has passed (a mismatch is re-copied from it instead of making the app
// send the whole binary again) and then calls releaseRAMFirstStage.
func receiveRAMFirst(tmp, dst, dir string, src io.Reader, h hash.Hash, buf []byte, need, maxSize int64) (receivedAgent, error) {
	stage := newRAMFirstStage()
	start := time.Now()
	n, err := streamIntoRAMStage(stage, src, h, buf, maxSize)
	if err != nil {
		releaseRAMFirstStage(stage)
		if errors.Is(err, errRAMFirstStageWrite) {
			ramFirstStageFailed.Store(true)
		}
		slog.Info("OTA write: RAM-first receive stopped before the Spotify engine was touched; stage removed",
			"bytes", n, "err", err)
		return receivedAgent{size: n}, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	memKB := memAvailableKBFn()
	slog.Info("OTA write: the whole body is in the RAM stage, dropping the Spotify engine now",
		"bytes", n, "needKB", need/1024, "memAvailableKB", memKB,
		"receiveMs", time.Since(start).Milliseconds())
	dropStart := time.Now()
	st := reclaimEngineForWrite(dir, n)
	dropMs := time.Since(dropStart).Milliseconds()

	copyStart := time.Now()
	werr := stageToNAND(stage, tmp, buf)
	if werr != nil {
		_ = os.Remove(tmp)
		werr = classifyNANDWriteErr("copy RAM stage", werr, dir, n, st.engineStopped, st.engineReclaim, st.predictedFull)
	} else {
		werr = finishBinaryWrite(tmp, dst, dir, n, st)
	}
	if werr != nil {
		if errors.Is(werr, errInsufficientNAND) {
			if herr := handStageToSwap(stage); herr != nil {
				releaseRAMFirstStage(stage)
				slog.Warn("OTA write: the NAND refused the copy from the RAM stage and the stage could not be handed to the swap",
					"bytes", n, "err", werr, "handoffErr", herr)
				return receivedAgent{size: n}, werr
			}
			// The binary is complete in the stage: the tier-3 swap needs only
			// a single copy on the NAND.
			slog.Warn("OTA write: the NAND refused the copy from the RAM stage even after the engine drop; taking the RAM-staged swap",
				"bytes", n, "err", werr)
			return receivedAgent{sum: sum, size: n, ramStaged: true}, nil
		}
		releaseRAMFirstStage(stage)
		return receivedAgent{size: n}, werr
	}
	slog.Info("OTA write: RAM-first receive copied onto the NAND; the stage stays until the flash verify",
		"bytes", n, "engineDropMs", dropMs, "copyMs", time.Since(copyStart).Milliseconds())
	return receivedAgent{sum: sum, size: n, ramFirstStage: stage}, nil
}

// handStageToSwap moves a complete RAM-first stage onto the shared swap path
// that armRAMStagedSwap and its helper read. It refuses while a swap is armed:
// that stage belongs to the running helper.
func handStageToSwap(stage string) error {
	if swapArmed.Load() {
		return errors.New("a RAM-staged swap is already armed")
	}
	if err := os.Rename(stage, ramStageTarget); err != nil {
		return err
	}
	forgetRAMFirstStage(stage)
	return nil
}

// reinstallFromRAMFirstStage copies a RAM-first stage onto dst once more (via
// dst.new, fsynced, renamed, directory synced) after the flash verify of the
// first copy failed. The engine is already gone, so no reclaim runs.
func reinstallFromRAMFirstStage(stage, dst string) error {
	tmp := dst + ".new"
	if err := stageToNAND(stage, tmp, make([]byte, 32*1024)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	syncDir(filepath.Dir(dst))
	return nil
}

// streamIntoRAMStage writes src into stage, hashing into h, and enforces the
// size limits. It returns the bytes received; on error the caller removes the
// stage. A failure on the speaker's side wraps errRAMFirstStageWrite.
func streamIntoRAMStage(stage string, src io.Reader, h hash.Hash, buf []byte, maxSize int64) (int64, error) {
	out, err := os.OpenFile(stage, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", errRAMFirstStageWrite, err)
	}
	var n int64
	for {
		nr, rerr := src.Read(buf)
		if nr > 0 {
			if n+int64(nr) > maxSize {
				_ = out.Close()
				return n + int64(nr), errUploadTooBig
			}
			h.Write(buf[:nr])
			if _, werr := out.Write(buf[:nr]); werr != nil {
				_ = out.Close()
				return n, fmt.Errorf("%w: %w", errRAMFirstStageWrite, werr)
			}
			n += int64(nr)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			_ = out.Close()
			return n, uploadReadError{rerr}
		}
	}
	if err := out.Close(); err != nil {
		return n, fmt.Errorf("%w: %w", errRAMFirstStageWrite, err)
	}
	if n < 1024 {
		return n, errUploadTooSmall
	}
	return n, nil
}
