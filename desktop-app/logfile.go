// File-based logger for the desktop app. Production Wails builds
// discard stderr, so without a file the user has nothing to attach
// to bug reports. This writes a single rolling file under the OS
// user-local app-data dir, capped at a small size so it never grows
// unbounded.

package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
)

const (
	maxLogFileBytes = 2 * 1024 * 1024 // 2 MB before truncate
	logFileName     = "str.log"
	logDirName      = "STReborn"
)

// LogFilePath returns the absolute path where the app log lives.
// %LOCALAPPDATA%\STReborn\str.log on Windows, $HOME/Library/Application
// Support/STReborn/str.log on macOS, $XDG_DATA_HOME (or
// ~/.local/share)/STReborn/str.log on Linux.
func LogFilePath() string {
	var base string
	switch runtime.GOOS {
	case "windows":
		base = os.Getenv("LOCALAPPDATA")
		if base == "" {
			base, _ = os.UserCacheDir()
		}
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(home, "Library", "Application Support")
		}
	default:
		if v := os.Getenv("XDG_DATA_HOME"); v != "" {
			base = v
		} else if home, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(home, ".local", "share")
		}
	}
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, logDirName, logFileName)
}

// openLogFile prepares the log file: ensures the directory exists,
// truncates if the current file is too large to keep the working
// set small, opens in append mode.
func openLogFile() (*os.File, error) {
	path := LogFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if st, err := os.Stat(path); err == nil && st.Size() > maxLogFileBytes {
		_ = os.Remove(path)
	}
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
}

// safeWriter wraps an io.Writer and never returns its errors. Used
// for the stderr leg of the multi-writer below: in Wails production
// builds there is no attached console and stderr writes fail. The
// io.MultiWriter contract stops at the first error from any writer,
// which would prevent the file leg from receiving any output. We
// keep the stderr leg purely so wails dev still shows logs, but its
// errors must not cascade and silently swallow the file write.
type safeWriter struct{ w io.Writer }

func (s safeWriter) Write(p []byte) (int, error) {
	_, _ = s.w.Write(p)
	return len(p), nil
}

// newFileLogger returns a slog.Logger that writes to the file at
// LogFilePath() and (best-effort) stderr. File writes come first in
// the multi-writer so a dead stderr in a production Wails build
// cannot prevent the file from being written. Also returns the
// underlying file handle so the caller can Sync it before reading
// the file (e.g. when bundling for export). If the file cannot be
// opened, the file return is nil and the logger falls back to
// safe-stderr only.
func newFileLogger(level slog.Level) (*slog.Logger, *os.File) {
	var w io.Writer = safeWriter{os.Stderr}
	var file *os.File
	if f, err := openLogFile(); err == nil {
		w = io.MultiWriter(f, safeWriter{os.Stderr})
		file = f
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})), file
}
