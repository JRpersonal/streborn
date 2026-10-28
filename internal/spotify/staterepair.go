// staterepair.go: a state.json the engine can no longer parse.
//
// go-librespot rewrites state.json itself (device id, volume, the saved login)
// through a temp file and a rename, but never syncs the temp file first. On the
// speaker's flash the rename can land before the data does (see writeFileAtomic),
// so a power cut, which is exactly what a user does when the speaker hangs,
// leaves state.json empty or cut off, and the engine then exits on every start
// with
//
//	failed creating app: loading state: failed unmarshalling state file: unexpected end of JSON input
//
// The supervisor treated that as a network crash loop and slowed the restarts
// down to one a minute, forever: the speaker vanished from the Spotify app and
// nothing short of an SSH session could bring it back (field, ST300, v1.1.3,
// 2026-10-28).
//
// Before every engine start the file is now checked. A file that does not parse
// is set aside (one backup, overwritten, so NAND is spared) and, when the
// multi-account store holds a copy of a login, the newest such copy is written
// back as the active login, so the user does not even have to tap the speaker
// in the Spotify app again. The engine generates a new device id for a state
// file without one. Without a copy the engine starts with no state at all,
// which is a fresh install: it waits for that one tap.
//
// The durable engine-side fix (sync before the rename in go-librespot's
// FileStateStore.Save) belongs in the fork; this repair stays as the fallback.

package spotify

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// corruptStateFile is the backup of the most recent state.json the engine could
// not parse, in configDir.
const corruptStateFile = "state.json.corrupt"

// stateRepair is the surfaced outcome of the last repair. Guarded by Manager.mu.
type stateRepair struct {
	at       time.Time
	restored bool   // a login copy was put back as the active login
	err      string // the repair itself failed
}

// repairCorruptState checks state.json before an engine start. It returns true
// when the file was unreadable as JSON and has been dealt with. A missing file
// is normal (no login yet) and left alone.
func (m *Manager) repairCorruptState() bool {
	path := filepath.Join(m.configDir, "state.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	if json.Valid(b) {
		return false
	}
	restored, rerr := m.replaceCorruptState(path, b)
	m.mu.Lock()
	m.stateRepair = stateRepair{at: time.Now(), restored: restored}
	if rerr != nil {
		m.stateRepair.err = rerr.Error()
	}
	m.mu.Unlock()
	switch {
	case rerr != nil:
		m.logger.Warn("spotify: the engine's state file is damaged and could not be repaired",
			"bytes", len(b), "err", rerr)
	case restored:
		m.logger.Warn("spotify: the engine's state file was damaged (cut off mid-write, likely by a power cut); set it aside and put the saved login back",
			"bytes", len(b), "backup", corruptStateFile)
	default:
		m.logger.Warn("spotify: the engine's state file was damaged (cut off mid-write, likely by a power cut); set it aside, the speaker needs one tap in the Spotify app to log in again",
			"bytes", len(b), "backup", corruptStateFile)
	}
	return true
}

// replaceCorruptState keeps the damaged bytes as the backup, then writes the
// newest usable login copy as the new state.json, or removes the file when
// there is none. The engine is not running when this is called.
func (m *Manager) replaceCorruptState(path string, damaged []byte) (restored bool, err error) {
	if werr := writeFileAtomic(filepath.Join(m.configDir, corruptStateFile), damaged, 0o600); werr != nil {
		return false, fmt.Errorf("backing up the damaged state file: %w", werr)
	}
	if cred, ok := m.newestCredentialCopy(); ok {
		raw, merr := json.Marshal(cred)
		if merr != nil {
			return false, merr
		}
		out, merr := json.Marshal(map[string]json.RawMessage{"credentials": raw})
		if merr != nil {
			return false, merr
		}
		if werr := writeFileAtomic(path, out, 0o600); werr != nil {
			return false, fmt.Errorf("writing the restored state file: %w", werr)
		}
		return true, nil
	}
	if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		return false, fmt.Errorf("removing the damaged state file: %w", rerr)
	}
	return false, nil
}

// newestCredentialCopy returns the most recently written login copy in the
// multi-account store that parses and is not the login Spotify already refused.
// The newest copy is the account that tapped the speaker last, which is the one
// the damaged state.json most likely named.
func (m *Manager) newestCredentialCopy() (storedCredential, bool) {
	entries, err := os.ReadDir(m.credStore)
	if err != nil {
		return storedCredential{}, false
	}
	var best storedCredential
	var bestAt time.Time
	for _, e := range entries {
		if e.IsDir() || !isCredentialCopy(e.Name()) {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		b, rerr := os.ReadFile(filepath.Join(m.credStore, e.Name()))
		if rerr != nil {
			continue
		}
		var c storedCredential
		if json.Unmarshal(b, &c) != nil || c.Username == "" || len(c.Data) == 0 || m.isRejectedCredential(c) {
			continue
		}
		if best.Username == "" || info.ModTime().After(bestAt) {
			best, bestAt = c, info.ModTime()
		}
	}
	return best, best.Username != ""
}
