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

// newFileLogger returns a slog.Logger that writes to BOTH the file
// at LogFilePath() and stderr (so wails dev still shows logs in the
// console). If the file cannot be opened, falls back to stderr only.
func newFileLogger(level slog.Level) *slog.Logger {
	var w io.Writer = os.Stderr
	if f, err := openLogFile(); err == nil {
		w = io.MultiWriter(os.Stderr, f)
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}
