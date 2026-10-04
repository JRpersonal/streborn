package webui

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Receiving an agent update without holding it in memory (#1083).
//
// The agent-update endpoint used to read the whole upload into one buffer
// before writing it, because its fallback tiers (EROFS retry, RAM-staged swap,
// flash-verify rewrite, stick refresh) all wanted the bytes afterwards. That
// buffer is the size of the agent, about 16 MB, and Go's collector lets the
// heap grow to roughly twice the live set before it runs, so the agent that
// RECEIVED an update briefly sat at 40+ MB. meierchen006 measured it on
// 2026-10-04 while v0.9.95 received v1.0.1: an ST20 went from 19.0 to 42.9 MB
// with MemAvailable falling from 32.3 to 7.5 MB, an ST300 from 16.9 to 42.4 MB.
// On a speaker that is short of memory to begin with, that is the same cliff
// the first #1083 fix took away from the start-up path.
//
// The bytes now go straight to the temp file the atomic write uses, through a
// 32 KB window, exactly as the engine (sidecar) upload already does. Each tier
// that used to need the buffer gets its bytes another way:
//
//   - read-only NAND: checked and remounted BEFORE the body is read, while the
//     upload can still go anywhere; a write that fails read-only afterwards
//     answers 507 and the app sends the update again (it retries every 5xx).
//   - NAND full mid-stream (tier 3): the part already written is copied from
//     the NAND temp file into the RAM stage and the rest of the body follows it
//     there, so the swap helper finds a complete binary. That costs the same
//     tmpfs space the tier always needed, but no agent heap.
//   - flash verify: compares against the hash taken while streaming. A mismatch
//     can no longer be rewritten from memory, so it answers 500 and the app's
//     retry delivers fresh bytes.
//   - stick refresh: copies from the file on NAND (or the RAM stage), streamed.

// ramStageTarget is where a tier-3 swap stages the binary. A variable so tests
// can point it at a temp dir; the swap helper reads the same path.
var ramStageTarget = ramStagePath

// errUploadTooBig and errUploadTooSmall reject a body by size. Checked while
// streaming, before anything is renamed over the running agent's binary.
var (
	errUploadTooBig   = errors.New("binary too big")
	errUploadTooSmall = errors.New("binary too small")
)

// uploadReadError marks a failure READING the request body (the caller went
// away or stalled), as opposed to a failure writing it on the speaker. The
// handler answers the two differently: one is the network's, one the box's.
type uploadReadError struct{ err error }

func (e uploadReadError) Error() string { return "read: " + e.err.Error() }
func (e uploadReadError) Unwrap() error { return e.err }

// receivedAgent describes where a streamed agent upload ended up.
type receivedAgent struct {
	sum       string // hex SHA256 of the complete upload
	size      int64
	ramStaged bool // NAND filled up mid-stream; the binary is complete at ramStageTarget
}

// receiveAgentBinary streams src into dst (atomically, via the temp file and
// rename the buffered write used) and returns the hash of what it received.
// Peak memory is the 32 KB window. When the NAND runs out of space the upload
// is moved to the RAM stage instead and receivedAgent.ramStaged is set; dst is
// then untouched and the caller arms the tier-3 swap.
func receiveAgentBinary(dst string, src io.Reader, need, maxSize int64) (receivedAgent, error) {
	h := sha256.New()
	buf := make([]byte, 32*1024)

	tmp, dir, st, err := prepareBinaryWrite(dst, need)
	if err != nil {
		if errors.Is(err, errInsufficientNAND) {
			return spillToRAMStage("", 0, nil, src, h, buf, need, maxSize, err)
		}
		return receivedAgent{}, err
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		cerr := classifyNANDWriteErr("open tmp", err, dir, need, st.engineStopped, st.engineReclaim, st.predictedFull)
		if errors.Is(cerr, errInsufficientNAND) {
			return spillToRAMStage("", 0, nil, src, h, buf, need, maxSize, cerr)
		}
		return receivedAgent{}, cerr
	}

	var n int64
	for {
		nr, rerr := src.Read(buf)
		if nr > 0 {
			if n+int64(nr) > maxSize {
				_ = f.Close()
				_ = os.Remove(tmp)
				return receivedAgent{size: n + int64(nr)}, errUploadTooBig
			}
			h.Write(buf[:nr])
			nw, werr := f.Write(buf[:nr])
			n += int64(nw)
			if werr != nil {
				_ = f.Close()
				if isNoSpaceErr(werr) {
					cerr := classifyNANDWriteErr("write tmp", werr, dir, need, st.engineStopped, st.engineReclaim, st.predictedFull)
					res, serr := spillToRAMStage(tmp, n, buf[nw:nr], src, h, buf, need, maxSize, cerr)
					_ = os.Remove(tmp)
					return res, serr
				}
				_ = os.Remove(tmp)
				return receivedAgent{size: n}, classifyNANDWriteErr("write tmp", werr, dir, need, st.engineStopped, st.engineReclaim, st.predictedFull)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
			return receivedAgent{size: n}, uploadReadError{rerr}
		}
	}
	if n < 1024 {
		_ = f.Close()
		_ = os.Remove(tmp)
		return receivedAgent{size: n}, errUploadTooSmall
	}
	// fsync before close: an unsynced write does not survive the reboot that
	// follows an OTA (#381).
	err = f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return receivedAgent{size: n}, classifyNANDWriteErr("write tmp", err, dir, need, st.engineStopped, st.engineReclaim, st.predictedFull)
	}
	if err := finishBinaryWrite(tmp, dst, dir, need, st); err != nil {
		return receivedAgent{size: n}, err
	}
	return receivedAgent{sum: hex.EncodeToString(h.Sum(nil)), size: n}, nil
}

// spillToRAMStage moves an upload that the NAND could not hold into the RAM
// stage: the first `written` bytes from the NAND temp file (if any), then the
// bytes of the current read that did not fit, then the rest of src. h has
// already seen everything read so far and keeps hashing what follows. nandErr
// is the original no-space error, returned when the stage cannot take the
// binary either, so the answer still describes the NAND.
func spillToRAMStage(tmp string, written int64, pending []byte, src io.Reader, h hash.Hash, buf []byte, need, maxSize int64, nandErr error) (receivedAgent, error) {
	stageDir := filepath.Dir(ramStageTarget)
	if need > 0 {
		if _, avail, ok := diskFree(stageDir); ok && avail < need+(1<<20) {
			return receivedAgent{}, fmt.Errorf("%w (RAM stage %s too small: need %d, avail %d)", nandErr, stageDir, need, avail)
		}
	}
	out, err := os.OpenFile(ramStageTarget, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return receivedAgent{}, fmt.Errorf("%w (RAM stage: %w)", nandErr, err)
	}
	fail := func(e error) (receivedAgent, error) {
		_ = out.Close()
		_ = os.Remove(ramStageTarget)
		return receivedAgent{}, e
	}
	var n int64
	if tmp != "" && written > 0 {
		in, err := os.Open(tmp)
		if err != nil {
			return fail(fmt.Errorf("%w (RAM stage: reopen partial write: %w)", nandErr, err))
		}
		c, err := io.CopyBuffer(out, io.LimitReader(in, written), buf)
		_ = in.Close()
		if err != nil || c != written {
			return fail(fmt.Errorf("%w (RAM stage: copy partial write: %d of %d bytes, %w)", nandErr, c, written, err))
		}
		n = c
	}
	if len(pending) > 0 {
		// pending aliases buf; write it before buf is reused below.
		if _, err := out.Write(pending); err != nil {
			return fail(fmt.Errorf("%w (RAM stage: %w)", nandErr, err))
		}
		n += int64(len(pending))
	}
	for {
		nr, rerr := src.Read(buf)
		if nr > 0 {
			if n+int64(nr) > maxSize {
				return fail(errUploadTooBig)
			}
			h.Write(buf[:nr])
			if _, werr := out.Write(buf[:nr]); werr != nil {
				return fail(fmt.Errorf("%w (RAM stage: %w)", nandErr, werr))
			}
			n += int64(nr)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fail(uploadReadError{rerr})
		}
	}
	if n < 1024 {
		return fail(errUploadTooSmall)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(ramStageTarget)
		return receivedAgent{}, fmt.Errorf("%w (RAM stage: %w)", nandErr, err)
	}
	return receivedAgent{sum: hex.EncodeToString(h.Sum(nil)), size: n, ramStaged: true}, nil
}

// selfPeakKB returns this process's VmHWM (peak resident set) in KB, or -1.
// Logged with every finished upload, so a measurement like the one in #1083
// can be read from the agent log instead of sampled by hand.
func selfPeakKB() int64 {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "VmHWM:" {
			if v, err := strconv.ParseInt(f[1], 10, 64); err == nil {
				return v
			}
		}
	}
	return -1
}
