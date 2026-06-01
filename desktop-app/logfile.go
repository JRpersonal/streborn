// File-based logger for the desktop app. Production Wails builds
// discard stderr, so without a file the user has nothing to attach
// to bug reports. This writes a single rolling file under the OS
// user-local app-data dir, capped at a small size so it never grows
// unbounded.

package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"
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

// rotateLogOnStartup keeps the log small and bounded across sessions:
// it moves an existing log to <name>.1 (overwriting an older .1) so the
// live file starts fresh on each launch and never grows run after run,
// while the immediately previous session (e.g. one that just crashed)
// stays available for diagnosis. Best-effort; called once at startup.
func rotateLogOnStartup() {
	path := LogFilePath()
	st, err := os.Stat(path)
	if err != nil || st.Size() == 0 {
		return
	}
	_ = os.Rename(path, path+".1")
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

// logCrash appends a panic record (with stack) straight to the log
// file, independent of the slog logger, so a crash that happens before
// or outside the logger still leaves a trace. Best-effort: a failure to
// open the file is swallowed because we are already on the crash path.
func logCrash(where string, r any) {
	f, err := openLogFile()
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "time=%s level=ERROR msg=PANIC where=%q value=%v\n%s\n",
		time.Now().Format(time.RFC3339), where, r, debug.Stack())
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
	rotateLogOnStartup()
	f, err := openLogFile()
	if err != nil {
		return slog.New(slog.NewTextHandler(safeWriter{os.Stderr}, &slog.HandlerOptions{Level: level})), nil
	}
	var w io.Writer
	if stderrIsConsole() {
		// Dev (wails dev): stderr is a real console. Keep showing logs
		// there in addition to the file.
		w = io.MultiWriter(f, safeWriter{os.Stderr})
	} else {
		// Production build: Wails discards stderr (no console). Redirect
		// the OS stderr fd to the log file so the Go runtime's own crash
		// traceback, which it writes to fd 2 on an unrecovered panic in
		// ANY goroutine or a fatal runtime error (e.g. concurrent map
		// access), is captured instead of lost. Log only to the file
		// afterwards so structured lines are not written twice.
		redirectStderrTo(f)
		w = f
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})), f
}

// stderrIsConsole reports whether stderr is attached to a terminal. A
// character device means a real console (wails dev); anything else (a
// pipe, a closed/!discarded handle in a production GUI build) is not.
func stderrIsConsole() bool {
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
