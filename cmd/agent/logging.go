// Logger construction and the NAND-backed agent log file.

package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
)

// nandLogPath is the persistent log file on UBIFS. It is captured in
// full by the diagnostic bundle (unlike /tmp/streborn-agent.log which
// the bundle only grabs the last 8 KB of). All slog output is mirrored
// here so remote-box bug reports include the whole agent startup, not
// just the tail after the listener loops have already settled.
const nandLogPath = "/mnt/nv/streborn/agent.log"

// nandLogMax caps the persistent log so a long-running agent does not
// fill the small NAND volume (~31 MB, shared with the Bose firmware). On
// overflow the file is rotated to agent.log.1 and a fresh agent.log starts,
// so the pair holds at most 2x this. 256 KiB still covers several fresh boots
// of debug output while keeping the log footprint well under 512 KiB;
// run.sh's cleanup_nand trims it further on each boot.
const nandLogMax = 256 * 1024

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	// Mirror to NAND so the diagnostic bundle sees more than the last
	// 8 KB of /tmp/streborn-agent.log. Best-effort: if NAND is read
	// only or full, fall back to stderr-only — agent must boot either
	// way. Rotation happens on open if the existing file already
	// exceeds nandLogMax.
	var writer io.Writer = os.Stderr
	if f := openNandLog(); f != nil {
		writer = io.MultiWriter(os.Stderr, f)
	}
	return slog.New(slog.NewTextHandler(writer, &slog.HandlerOptions{Level: lvl}))
}

// openNandLog opens /mnt/nv/streborn/agent.log in append mode, rotating
// it to agent.log.1 first if it already exceeds nandLogMax. Returns
// nil on any error so the caller falls back to stderr-only.
func openNandLog() *os.File {
	if st, err := os.Stat(nandLogPath); err == nil && st.Size() > nandLogMax {
		// Best-effort rotate. Failure here just means we keep appending
		// to a slightly oversized log; not worth bailing.
		_ = os.Rename(nandLogPath, nandLogPath+".1")
	}
	f, err := os.OpenFile(nandLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "newLogger: NAND log unavailable, stderr only:", err)
		return nil
	}
	return f
}
