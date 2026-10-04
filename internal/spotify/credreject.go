// credreject.go: what the supervisor does when Spotify refuses the login the
// engine saved on the speaker.
//
// go-librespot runs in zeroconf mode with persist_credentials, so the first tap
// in the Spotify app leaves a reusable credential in state.json and every later
// start logs in with it. When Spotify stops accepting that credential (password
// change, "sign out everywhere", account deleted or revoked), the engine retries
// a handful of times, then exits fatally with
//
//	failed authenticating accesspoint with stored credentials: ... BadCredentials
//
// before it ever gets to serve a zeroconf tap. The supervisor used to treat that
// as just another crash: the engine came back, read the same dead credential,
// died again, and the crash-loop backoff capped at a minute. A field bundle
// (ST10, v1.0.2, 2026-10-04) showed 150 such cycles and counting: one engine
// start per minute forever, a few dozen log lines each, and a speaker that never
// showed up in the Spotify app again because the engine was dead most of the
// time and never accepted a tap anyway.
//
// The durable answer is to take the dead credential out of the engine's way. It
// is moved aside (kept as a backup, never deleted) and the engine restarts with
// no stored login, which is exactly a fresh install: it advertises itself over
// zeroconf and the user taps the speaker in the Spotify app once to log it back
// in. No login on the speaker, no user action beyond the tap.
//
// Fallback: if the credential cannot be moved (unexpected file layout, NAND
// write failure) or something keeps putting the same refused login back (a
// credential sync from another speaker, an account switch to the same stale
// copy), consecutive refusals back off exponentially up to half an hour instead
// of restarting every minute.

package spotify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// rejectedCredentialFile is the backup of the most recently refused
	// credential, in configDir. Same {username,data} shape as state.json's
	// .credentials, so it can be staged back by hand if Spotify ever turns out
	// to have refused it by mistake. One file, overwritten: NAND is spared and
	// only the newest refusal is worth keeping.
	rejectedCredentialFile = "rejected-credential.json"
	// credRejectBackoffBase / Max bound the fallback backoff for refusals the
	// quarantine could not resolve: 1, 2, 4, ... minutes, capped.
	credRejectBackoffBase = time.Minute
	credRejectBackoffMax  = 30 * time.Minute
	// credRejectQuickRestart is the wait after a successful quarantine: the
	// engine comes straight back, now waiting for a tap.
	credRejectQuickRestart = 3 * time.Second
)

// credRejectState is the surfaced state of the saved-login refusal handling.
// Guarded by Manager.mu.
type credRejectState struct {
	// runRejected latches when the current engine run logs the fatal
	// stored-credential refusal; consumed once the run has exited.
	runRejected bool
	// streak counts consecutive runs that ended in a refusal. Any run that
	// ends otherwise resets it.
	streak        int
	total         int
	lastAt        time.Time
	quarantinedAt time.Time
	quarantineErr string
	retryAt       time.Time
}

// isStoredCredentialRejection reports whether a lowercased go-librespot log line
// is the engine giving up on its SAVED login because Spotify refused it. The
// retry warnings before it also carry "badcredentials", but only the fatal exit
// names the stored credential, and only that one is a verdict on it (a refused
// zeroconf tap does not touch the saved login).
func isStoredCredentialRejection(lc string) bool {
	return strings.Contains(lc, "badcredentials") && strings.Contains(lc, "stored credentials")
}

// noteCredentialLine latches a stored-credential refusal for the current run.
func (m *Manager) noteCredentialLine(lc string) {
	if !isStoredCredentialRejection(lc) {
		return
	}
	m.mu.Lock()
	m.credReject.runRejected = true
	m.mu.Unlock()
}

// resetCredRejectRun clears the per-run latch before a new engine start.
func (m *Manager) resetCredRejectRun() {
	m.mu.Lock()
	m.credReject.runRejected = false
	m.mu.Unlock()
}

// afterEngineExit is called by the supervisor once a run has ended. When the run
// ended because Spotify refused the saved login, it moves that login aside and
// returns how long to wait before the next start (handled=true). Otherwise it
// resets the refusal streak and returns handled=false, leaving the normal
// crash-loop pacing in charge.
func (m *Manager) afterEngineExit() (wait time.Duration, handled bool) {
	m.mu.Lock()
	rejected := m.credReject.runRejected
	m.credReject.runRejected = false
	if !rejected {
		m.credReject.streak = 0
		m.credReject.retryAt = time.Time{}
		m.mu.Unlock()
		return 0, false
	}
	m.credReject.streak++
	m.credReject.total++
	m.credReject.lastAt = time.Now()
	streak := m.credReject.streak
	m.mu.Unlock()

	moved, err := m.quarantineRejectedCredential()
	switch {
	case err != nil:
		m.logger.Warn("spotify: Spotify refused the speaker's saved login and it could not be set aside",
			"err", err)
	case moved:
		m.logger.Warn("spotify: Spotify no longer accepts the speaker's saved login; set it aside (backup kept) and restarting the engine without it, so a tap on the speaker in the Spotify app logs it back in",
			"backup", rejectedCredentialFile, "streak", streak)
	default:
		m.logger.Warn("spotify: Spotify refused the speaker's saved login, but no saved login was found to set aside",
			"streak", streak)
	}

	wait = credRejectBackoff(streak, moved && err == nil)
	m.mu.Lock()
	if moved && err == nil {
		m.credReject.quarantinedAt = time.Now()
		m.credReject.quarantineErr = ""
	} else if err != nil {
		m.credReject.quarantineErr = err.Error()
	}
	m.credReject.retryAt = time.Now().Add(wait)
	m.mu.Unlock()
	if wait > credRejectQuickRestart {
		m.logger.Warn("spotify: the saved login keeps being refused, waiting before the next engine start",
			"retryInS", int(wait.Seconds()), "streak", streak)
	}
	return wait, true
}

// credRejectBackoff is the wait before the next engine start after the streak-th
// consecutive refusal. The first refusal whose credential was set aside restarts
// at once: the engine then has nothing to log in with and waits for a tap. Every
// other case, including the same refused login coming back, doubles from a
// minute up to the cap, so a speaker in this state costs a start every half hour
// at worst instead of one a minute.
func credRejectBackoff(streak int, quarantined bool) time.Duration {
	if streak <= 1 && quarantined {
		return credRejectQuickRestart
	}
	n := streak - 1
	if quarantined {
		n = streak - 2
	}
	if n < 0 {
		n = 0
	}
	if n > 10 {
		n = 10
	}
	return min(credRejectBackoffBase<<n, credRejectBackoffMax)
}

// quarantineRejectedCredential moves the saved login the engine just failed
// with out of the engine's way, keeping it as rejected-credential.json:
//
//   - state.json's .credentials (the current go-librespot store) is removed,
//     every other field (device_id, last_volume, ...) is kept;
//   - a legacy credentials.json is renamed aside, since the engine falls back
//     to it when state.json names no login;
//   - the per-account copy of the same login in the multi-account store is
//     renamed aside too when it holds the very same blob, so an account switch
//     cannot put the refused login straight back. A copy that differs is a
//     newer login of that account and stays.
//
// moved reports whether anything was set aside. The engine is not running when
// this is called (its run has exited), so nothing races the rewrite.
func (m *Manager) quarantineRejectedCredential() (moved bool, err error) {
	statePath := filepath.Join(m.configDir, "state.json")
	backupPath := filepath.Join(m.configDir, rejectedCredentialFile)
	var rejected storedCredential

	if b, rerr := os.ReadFile(statePath); rerr == nil {
		st := map[string]json.RawMessage{}
		if json.Unmarshal(b, &st) == nil {
			if raw, ok := st["credentials"]; ok {
				_ = json.Unmarshal(raw, &rejected)
				if rejected.Username != "" || len(rejected.Data) > 0 {
					// Backup first: if the rewrite below fails, the engine still
					// has its credential and nothing is lost.
					if werr := writeFileAtomic(backupPath, raw, 0o600); werr != nil {
						return false, fmt.Errorf("backing up the refused login: %w", werr)
					}
					delete(st, "credentials")
					out, merr := json.Marshal(st)
					if merr != nil {
						return false, merr
					}
					if werr := writeFileAtomic(statePath, out, 0o600); werr != nil {
						return false, fmt.Errorf("removing the refused login from state.json: %w", werr)
					}
					moved = true
				}
			}
		}
	}

	legacy := filepath.Join(m.configDir, "credentials.json")
	if _, serr := os.Stat(legacy); serr == nil {
		if !moved {
			// The legacy file was the login the engine used: read it for the
			// per-account match below.
			if b, rerr := os.ReadFile(legacy); rerr == nil {
				_ = json.Unmarshal(b, &rejected)
			}
		}
		if rerr := os.Rename(legacy, legacy+".rejected"); rerr != nil {
			return moved, fmt.Errorf("setting aside the legacy credentials.json: %w", rerr)
		}
		moved = true
	}

	if rejected.Username != "" && len(rejected.Data) > 0 {
		copyPath := filepath.Join(m.credStore, sanitizeUser(rejected.Username)+".json")
		if b, rerr := os.ReadFile(copyPath); rerr == nil {
			var c storedCredential
			if json.Unmarshal(b, &c) == nil && bytes.Equal(c.Data, rejected.Data) {
				if rerr := os.Rename(copyPath, copyPath+".rejected"); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
					return moved, fmt.Errorf("setting aside the account copy of the refused login: %w", rerr)
				}
			}
		}
	}
	return moved, nil
}

// errCredentialRejected is returned by ImportCredential for a login Spotify
// has already refused on this speaker.
var errCredentialRejected = errors.New("spotify already refused this login on this speaker; tap the speaker in the Spotify app instead")

// isRejectedCredential reports whether cred is byte-for-byte the login kept in
// the rejected-credential backup.
func (m *Manager) isRejectedCredential(cred storedCredential) bool {
	b, err := os.ReadFile(filepath.Join(m.configDir, rejectedCredentialFile))
	if err != nil {
		return false
	}
	var r storedCredential
	if json.Unmarshal(b, &r) != nil || len(r.Data) == 0 {
		return false
	}
	return r.Username == cred.Username && bytes.Equal(r.Data, cred.Data)
}

// writeFileAtomic writes data to a sibling temp file and renames it over path,
// so a power cut mid-write never leaves the engine a truncated state.json.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// CredentialSnapshot is the spotify_auth debug section: whether the engine has
// a saved login, and whether (and how) Spotify refusing it was handled. It
// deliberately carries no account name or credential bytes, only state.
func (m *Manager) CredentialSnapshot() map[string]any {
	m.mu.Lock()
	s := m.credReject
	m.mu.Unlock()
	present := stateHasCredential(filepath.Join(m.configDir, "state.json"))
	_, backupErr := os.Stat(filepath.Join(m.configDir, rejectedCredentialFile))
	out := map[string]any{
		"savedLoginPresent":    present,
		"rejectedTotal":        s.total,
		"rejectStreak":         s.streak,
		"lastRejectedAt":       fmtTime(s.lastAt),
		"setAsideAt":           fmtTime(s.quarantinedAt),
		"setAsideError":        s.quarantineErr,
		"rejectedBackupKept":   backupErr == nil,
		"nextStartAt":          fmtTime(s.retryAt),
		"waitingForSpotifyTap": !present && !s.quarantinedAt.IsZero(),
	}
	return out
}

// fmtTime renders t as RFC 3339, empty for the zero time.
func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// hasUsableCredential is LoggedIn without the login Spotify already refused on
// this speaker. LoggedIn only asks whether a credential sits on disk, and a
// refused one still does whenever setting it aside failed or the account store
// holds the same blob under another name. Both preset questions, "will this key
// play?" at save time and "can this recall start?" at press time, asked
// LoggedIn, so a speaker whose saved login Spotify had stopped accepting
// answered yes to both: the key was saved without the "pick this speaker in
// Spotify once" notice, and a press reported playing while the speaker gave up
// in the background and went amber (field, ST10 stereo pair, 2026-10-04).
func (m *Manager) hasUsableCredential() bool {
	if cred, ok := m.readStateCredential(); ok && !m.isRejectedCredential(cred) {
		return true
	}
	if usableCredentialFile(m, filepath.Join(m.configDir, "credentials.json")) {
		return true
	}
	entries, err := os.ReadDir(m.credStore)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") && usableCredentialFile(m, filepath.Join(m.credStore, e.Name())) {
			return true
		}
	}
	return false
}

// usableCredentialFile reports whether path holds a credential that is not the
// refused one. An unreadable or unparsable file counts as usable when it
// exists, the same benefit of the doubt LoggedIn gives it: only a positive
// match against the refused login takes a credential out of play.
func usableCredentialFile(m *Manager, path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var c storedCredential
	if json.Unmarshal(b, &c) != nil || len(c.Data) == 0 {
		return true
	}
	return !m.isRejectedCredential(c)
}
