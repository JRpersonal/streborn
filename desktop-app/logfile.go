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

// logGenerations is how many previous sessions are kept beside the live log.
//
// It used to be one, and one is not enough for the situation these logs exist
// for. Somebody whose update looks stuck restarts the app, which is the
// reasonable thing to do and also what destroys the evidence: the run that
// went wrong becomes .1, the next restart overwrites it, and by the time they
// save a diagnostic the session that caused the problem is gone. A reporter
// whose speakers lost their Spotify engine across four re-pushes sent a bundle
// in which not one of those four runs survived (2026-09-27).
//
// Five at 2 MB each is 10 MB on a desktop machine, which is nothing, against a
// class of report that cannot otherwise be answered at all.
const logGenerations = 5

// rotateLogOnStartup keeps the log small and bounded across sessions: the live
// file starts fresh on each launch and never grows run after run, while the
// last few sessions stay available for diagnosis. Best-effort; called once at
// startup.
func rotateLogOnStartup() {
	path := LogFilePath()
	st, err := os.Stat(path)
	if err != nil || st.Size() == 0 {
		return
	}
	// Oldest first, so nothing is overwritten before it has been moved up.
	for i := logGenerations - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", path, i), fmt.Sprintf("%s.%d", path, i+1))
	}
	_ = os.Rename(path, path+".1")
}

// PreviousLogPaths lists the kept sessions, newest first, skipping any that are
// not there. The bundle ships all of them: the one that matters is rarely the
// most recent, because the act of noticing a problem is usually a restart.
func PreviousLogPaths() []string {
	path := LogFilePath()
	var out []string
	for i := 1; i <= logGenerations; i++ {
		p := fmt.Sprintf("%s.%d", path, i)
		if st, err := os.Stat(p); err == nil && st.Size() > 0 {
			out = append(out, p)
		}
	}
	return out
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
	fmt.Fprintf(f, "time=%s level=ERROR msg=PANIC where=%q value=%v\n%s\n",
		time.Now().Format(time.RFC3339), where, r, debug.Stack())
	// Surface a close error on this writable crash log rather than discarding it;
	// we are already on the crash path, so stderr is the only place left to note it.
	if cerr := f.Close(); cerr != nil {
		fmt.Fprintf(os.Stderr, "logCrash: closing %s failed: %v\n", LogFilePath(), cerr)
	}
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
