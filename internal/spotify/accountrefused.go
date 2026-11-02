package spotify

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/anonymise"
)

// Remembering WHICH account Spotify refuses on this engine, until it plays.
//
// AudioKeyRefused is deliberately short-lived: it says "this just happened"
// for ten minutes and goes away. That suits the moment, but not the person who
// opens the app an hour later, or the next morning, wondering why Spotify on
// (STR) never plays. Some accounts are refused for good: free accounts, and
// Premium accounts in the cohort Spotify moved to its newer DRM, where every
// audio key this engine asks for is refused (docs/streaming/spotify.md, "Known
// limitation"). For those the notice has to stay until something actually
// changes, and the only change that proves anything is the account playing.
//
// So a refusal episode records the account it happened to, on the NAND, and
// the first audio the engine delivers for that account erases it again. The
// apps show a short notice while the account the engine is logged in as is
// recorded here.
//
// Written once per transition, never per refusal: an account already recorded
// is not written again however many episodes follow, and nothing is written
// while nothing changes. Same rule as names.json and plans.json, and for the
// same reason: the flash is the part of the speaker that wears out.

// accountRefusedFile sits beside names.json and plans.json in the per-account
// store, not in the engine's config dir: it is a fact about the same accounts,
// and the store is the part of the Spotify state that outlives the engine
// being removed and reinstalled to make room for an agent update.
const accountRefusedFile = "refused.json"

// refusalAccountTimeout bounds the one engine /status read that names the
// account of an episode. The engine is next door; this only matters when it is
// busy or gone.
const refusalAccountTimeout = 3 * time.Second

func (m *Manager) accountRefusedPath() string {
	return filepath.Join(m.credStore, accountRefusedFile)
}

// loadRefusedAccountsLocked reads the store once. Missing, unreadable or
// corrupt means "nothing recorded": the notice is a courtesy, and losing it is
// far better than inventing one. Caller holds m.mu.
func (m *Manager) loadRefusedAccountsLocked() {
	if m.refusedAccounts != nil {
		return
	}
	m.refusedAccounts = map[string]time.Time{}
	if m.credStore == "" {
		return
	}
	data, err := os.ReadFile(m.accountRefusedPath())
	if err != nil {
		return
	}
	var got map[string]time.Time
	if json.Unmarshal(data, &got) != nil {
		return
	}
	for id, at := range got {
		if id = strings.TrimSpace(id); id != "" {
			m.refusedAccounts[id] = at
		}
	}
}

// writeRefusedAccounts puts a snapshot of the store on the NAND, or removes the
// file when nothing is recorded any more. Caller holds storeWriteMu, not mu.
func (m *Manager) writeRefusedAccounts(snapshot map[string]time.Time) error {
	if m.credStore == "" {
		return nil
	}
	if len(snapshot) == 0 {
		if err := os.Remove(m.accountRefusedPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(m.credStore, 0o755); err != nil {
		return err
	}
	blob, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	return writeFileAtomic(m.accountRefusedPath(), blob, 0o644)
}

// markAccountRefused records that Spotify refused user on this engine. A no-op
// (and no write) when user is already recorded.
func (m *Manager) markAccountRefused(user string) {
	user = strings.TrimSpace(user)
	if user == "" {
		return
	}
	m.storeWriteMu.Lock()
	defer m.storeWriteMu.Unlock()
	m.mu.Lock()
	m.loadRefusedAccountsLocked()
	if _, known := m.refusedAccounts[user]; known {
		m.mu.Unlock()
		return
	}
	m.refusedAccounts[user] = time.Now().UTC()
	snapshot := m.refusedSnapshotLocked()
	m.mu.Unlock()
	if err := m.writeRefusedAccounts(snapshot); err != nil {
		m.logger.Debug("spotify: could not store the refused account", "err", err)
	}
	m.logger.Warn("spotify: Spotify refuses this account on the speaker's engine; the apps say so until it plays",
		"user", anonymise.MaskAccount(user))
}

// clearAccountRefused forgets a recorded refusal for user. A no-op (and no
// write) when user is not recorded.
func (m *Manager) clearAccountRefused(user string) {
	user = strings.TrimSpace(user)
	if user == "" {
		return
	}
	m.storeWriteMu.Lock()
	defer m.storeWriteMu.Unlock()
	m.mu.Lock()
	m.loadRefusedAccountsLocked()
	if _, known := m.refusedAccounts[user]; !known {
		m.mu.Unlock()
		return
	}
	delete(m.refusedAccounts, user)
	snapshot := m.refusedSnapshotLocked()
	m.mu.Unlock()
	if err := m.writeRefusedAccounts(snapshot); err != nil {
		m.logger.Debug("spotify: could not update the refused accounts", "err", err)
	}
	m.logger.Info("spotify: an account Spotify had refused plays again on the speaker's engine; notice cleared",
		"user", anonymise.MaskAccount(user))
}

func (m *Manager) refusedSnapshotLocked() map[string]time.Time {
	out := make(map[string]time.Time, len(m.refusedAccounts))
	for k, v := range m.refusedAccounts {
		out[k] = v
	}
	return out
}

// refusalAccount names the account the engine is logged in as right now: a
// fresh /status read when the engine answers, otherwise the account it was
// last seen logged in as during this engine process. "" when neither is known.
func (m *Manager) refusalAccount() string {
	m.mu.Lock()
	canAsk := m.client != nil && m.apiAddr != ""
	m.mu.Unlock()
	if canAsk {
		ctx, cancel := context.WithTimeout(context.Background(), refusalAccountTimeout)
		user, ok := m.engineUsername(ctx)
		cancel()
		if ok && user != "" {
			m.noteEngineAccount(user)
			return user
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.productUser
}

// noteKeyRefusalEpisode is called ONCE per refusal episode, from both places
// that decide one has happened: the newer engine's own key-denied report
// (keydenied.go) and the agent's run counter for older engines (engine.go). It
// works out the account in the background, because both callers sit on the
// engine's log path, and records it.
//
// The two callers can both fire for one episode (an engine that logs each
// refused track AND reports the stop), so a second call within the run gap of
// the first is the same episode and asks nothing.
func (m *Manager) noteKeyRefusalEpisode() {
	now := time.Now()
	m.mu.Lock()
	same := !m.refusalEpisodeAt.IsZero() && now.Sub(m.refusalEpisodeAt) < keyRefusalRunGap
	m.refusalEpisodeAt = now
	m.mu.Unlock()
	if same {
		return
	}
	go func() {
		user := m.refusalAccount()
		if user == "" {
			m.logger.Info("spotify: Spotify refused the audio keys, but the engine did not say which account it is logged in as")
			return
		}
		m.markAccountRefused(user)
	}()
}

// noteAccountStreamed is called when the engine delivers real audio, the first
// audio page of a track in the Ogg drain. A refused account cannot get there:
// without the audio key nothing is decrypted, so nothing is encoded. That makes
// it the one signal that proves the account plays, unlike a load, which the
// engine logs for refused tracks too.
//
// Cheap on the common path: an empty store is one map length under the lock.
// Only while something is recorded does it ask the engine who is logged in,
// once per track start at most, and never two lookups at a time.
func (m *Manager) noteAccountStreamed() {
	m.mu.Lock()
	m.loadRefusedAccountsLocked()
	if len(m.refusedAccounts) == 0 || m.refusedClearBusy {
		m.mu.Unlock()
		return
	}
	m.refusedClearBusy = true
	m.mu.Unlock()
	go func() {
		defer func() {
			m.mu.Lock()
			m.refusedClearBusy = false
			m.mu.Unlock()
		}()
		if user := m.refusalAccount(); user != "" {
			m.clearAccountRefused(user)
		}
	}()
}

// accountRefusedFor reports whether user is recorded as refused, and the
// display name the speaker knows for it ("" when none). user "" is never
// refused: a speaker logged into nobody has no account to warn about.
func (m *Manager) accountRefusedFor(user string) (bool, string) {
	user = strings.TrimSpace(user)
	if user == "" {
		return false, ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loadRefusedAccountsLocked()
	if _, ok := m.refusedAccounts[user]; !ok {
		return false, ""
	}
	m.loadAccountNames()
	return true, m.accountNames[user]
}
