package main

// Persistent OTA journal. Every speaker (agent) update attempt is appended here
// with its phases and outcome. Unlike str.log it is NOT rotated away on the next
// app launch and it is always included in the diagnostic bundle, so the most
// common reason an OTA-failure report is undiagnosable, "the update attempt is
// not in the bundle" (the user restarted the app between the failure and the
// export, or str.log rolled past it), no longer applies. The file is capped so it
// stays small.

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const maxOTAJournalBytes = 64 * 1024

// otaJournalMu serializes the append+cap of the OTA journal. recordOTA is called
// from multiple goroutines when several speakers update at once (the "update all
// speakers" batch): without this, two concurrent appends interleave and, worse,
// two capOTAJournal read-modify-write rewrites race and can truncate or garble
// the very journal used to diagnose those (often weak-Wi-Fi) batch updates.
var otaJournalMu sync.Mutex

// otaJournalPath is the OTA history file, next to str.log so it lands in the same
// app-data dir the user already knows from logFile.
func otaJournalPath() string {
	return filepath.Join(filepath.Dir(LogFilePath()), "ota-history.log")
}

// recordOTA appends one timestamped phase/outcome line for a speaker update to
// the persistent OTA journal. Best-effort: any error is swallowed (this is
// diagnostics, it must never affect the update itself). Mirrored to the app
// logger so a same-session export via str.log also has it.
func (a *App) recordOTA(host, event string) {
	if a != nil && a.logger != nil {
		a.logger.Info("ota journal", "host", host, "event", event)
	}
	line := fmt.Sprintf("time=%s host=%s %s\n", time.Now().Format(time.RFC3339), host, event)
	path := otaJournalPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	// Serialize the append+cap so concurrent per-box updates cannot interleave a
	// line or race two capOTAJournal rewrites of the same file.
	otaJournalMu.Lock()
	defer otaJournalMu.Unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = f.WriteString(line)
	_ = f.Close()
	capOTAJournal(path)
}

// capOTAJournal keeps the journal under maxOTAJournalBytes by dropping the oldest
// lines (keeps the tail at a line boundary). OTA attempts are rare, so reading
// the small file on each append is cheap.
func capOTAJournal(path string) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) <= maxOTAJournalBytes {
		return
	}
	tail := data[len(data)-maxOTAJournalBytes:]
	// Start at the first full line so we never keep a half line.
	for i := 0; i < len(tail); i++ {
		if tail[i] == '\n' {
			tail = tail[i+1:]
			break
		}
	}
	_ = os.WriteFile(path, tail, 0o644)
}
