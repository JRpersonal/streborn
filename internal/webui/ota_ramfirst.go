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
// whole body goes into the RAM stage (the tmpfs the tier-3 swap already uses)
// first, hashed as it streams. Only once the last byte is in and the size
// checks pass is the engine dropped (reclaimEngineForWrite, with the engine
// stop hook and the dropped-for-update marker), and the staged binary is then
// copied onto the NAND temp path and installed exactly like a normal receive.
// A transfer that breaks off at any point before that leaves the engine and
// its marker alone and removes the stage.
//
// If the copy onto the NAND runs out of space even after the drop, the binary
// is still complete in the RAM stage, which is precisely what the tier-3 swap
// (armRAMStagedSwap) installs from, so the result is reported as ramStaged
// instead of failing.

import (
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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

// ramFirstRoute decides, for a write where statfs predicted a NAND shortage,
// whether the body is received into RAM first (true) or streamed onto the
// NAND with the deferred engine drop (false). Every decision is logged.
func ramFirstRoute(need int64) bool {
	stageDir := filepath.Dir(ramStageTarget)
	// A stage left behind by an earlier attempt would only make the figures
	// lie. No swap can be reading it: an armed swap exits this agent.
	_ = os.Remove(ramStageTarget)
	stageAvail, stageOK := ramStageAvailFn(stageDir)
	memKB := memAvailableKBFn()
	reason := ""
	switch {
	case need <= 0:
		reason = "upload size unknown"
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

// receiveRAMFirst receives the whole body into the RAM stage, then drops the
// engine, copies the stage onto tmp and installs it over dst. See the file
// comment for the order and why.
func receiveRAMFirst(tmp, dst, dir string, src io.Reader, h hash.Hash, buf []byte, need, maxSize int64) (receivedAgent, error) {
	n, err := streamIntoRAMStage(src, h, buf, maxSize)
	if err != nil {
		_ = os.Remove(ramStageTarget)
		slog.Info("OTA write: RAM-first receive stopped before the Spotify engine was touched; stage removed",
			"bytes", n, "err", err)
		return receivedAgent{size: n}, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	memKB := memAvailableKBFn()
	slog.Info("OTA write: the whole body is in the RAM stage, dropping the Spotify engine now",
		"bytes", n, "needKB", need/1024, "memAvailableKB", memKB)
	st := reclaimEngineForWrite(dir, n)

	werr := stageToNAND(ramStageTarget, tmp, buf)
	if werr != nil {
		_ = os.Remove(tmp)
		werr = classifyNANDWriteErr("copy RAM stage", werr, dir, n, st.engineStopped, st.engineReclaim, st.predictedFull)
	} else {
		werr = finishBinaryWrite(tmp, dst, dir, n, st)
	}
	if werr != nil {
		if errors.Is(werr, errInsufficientNAND) {
			// The binary is complete in the stage: hand it to the tier-3 swap,
			// which needs only a single copy on the NAND.
			slog.Warn("OTA write: the NAND refused the copy from the RAM stage even after the engine drop; taking the RAM-staged swap",
				"bytes", n, "err", werr)
			return receivedAgent{sum: sum, size: n, ramStaged: true}, nil
		}
		_ = os.Remove(ramStageTarget)
		return receivedAgent{size: n}, werr
	}
	_ = os.Remove(ramStageTarget)
	slog.Info("OTA write: RAM-first receive installed onto the NAND; stage removed", "bytes", n)
	return receivedAgent{sum: sum, size: n}, nil
}

// streamIntoRAMStage writes src into ramStageTarget, hashing into h, and
// enforces the size limits. It returns the bytes received; on error the caller
// removes the stage.
func streamIntoRAMStage(src io.Reader, h hash.Hash, buf []byte, maxSize int64) (int64, error) {
	out, err := os.OpenFile(ramStageTarget, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return 0, fmt.Errorf("RAM-first stage: %w", err)
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
				return n, fmt.Errorf("RAM-first stage: %w", werr)
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
		return n, fmt.Errorf("RAM-first stage: %w", err)
	}
	if n < 1024 {
		return n, errUploadTooSmall
	}
	return n, nil
}
