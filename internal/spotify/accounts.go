// accounts.go: the multi-account credential plane — per-account credential
// capture and swap, login/product-type detection, and credential
// export/import between speakers.

package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ---- Multi-account credential swap (#27) ----
//
// go-librespot is a single-user receiver: it persists ONE zeroconf credential
// (credentials.json in configDir) and logs in as the last account that tapped
// the device. To recall a preset saved by a different household account, the
// manager keeps a per-account copy of each credential as it taps (captureLoop),
// then on a cross-account recall swaps the right copy into credentials.json and
// restarts go-librespot, which re-reads it at startup (it has no runtime login
// API). The restart takes ~3s, shorter than the box's playback buffer, so the
// switch is audibly seamless. Same-account recall does not switch or restart.

// accountProduct returns the Spotify account product type ("premium"/"free"/
// "open") via go-librespot's authenticated Web API proxy (GET /web-api/v1/me),
// cached for a few minutes. Returns "" when unknown (the zeroconf token may lack
// the user-read-private scope, in which case /v1/me omits product), so callers
// fall back to the log signal. Best-effort.
func (m *Manager) accountProduct(ctx context.Context) string {
	m.mu.Lock()
	if m.productType != "" && time.Since(m.productCheckedAt) < 5*time.Minute {
		p := m.productType
		m.mu.Unlock()
		return p
	}
	m.mu.Unlock()
	data, err := m.apiGet(ctx, "/web-api/v1/me")
	if err != nil {
		return ""
	}
	var me struct {
		Product string `json:"product"`
	}
	if json.Unmarshal(data, &me) != nil || me.Product == "" {
		return ""
	}
	m.mu.Lock()
	m.productType, m.productCheckedAt = me.Product, time.Now()
	m.mu.Unlock()
	return me.Product
}

// PremiumRequired reports whether the current Spotify account cannot do the
// autonomous on-demand playback a preset recall needs, i.e. it is a free/open
// account rather than Premium (#45). Non-blocking: it uses the cached product
// type and the go-librespot free-account log signal, kicking a background product
// refresh when the type is not yet known. Conservative: returns true only on a
// POSITIVE non-Premium signal, never on "unknown", so a Premium user is never
// wrongly blocked.
func (m *Manager) PremiumRequired() bool {
	m.mu.Lock()
	free := m.sawFreeAccountLog
	p := m.productType
	tried := m.productTriedAt
	m.mu.Unlock()
	if free {
		return true
	}
	if p == "" && time.Since(tried) > 30*time.Second {
		m.mu.Lock()
		m.productTriedAt = time.Now()
		m.mu.Unlock()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			m.accountProduct(ctx)
		}()
	}
	return p == "free" || p == "open"
}

// currentUsername returns the Spotify account go-librespot is currently logged
// in as, or "" if it is not reachable / not authed yet.
func (m *Manager) currentUsername(ctx context.Context) string {
	data, err := m.apiGet(ctx, "/status")
	if err != nil {
		return ""
	}
	var st struct {
		Username string `json:"username"`
	}
	if json.Unmarshal(data, &st) != nil {
		return ""
	}
	return st.Username
}

// CurrentUsername is the exported form; the preset-save path stamps it onto a
// new Spotify preset so a later recall can switch back to that account.
func (m *Manager) CurrentUsername(ctx context.Context) string {
	return m.currentUsername(ctx)
}

// LoggedIn reports whether this speaker has ever completed a Spotify Connect
// login, i.e. a reusable credential is persisted on disk. Recall needs this:
// without a credential go-librespot cannot start playback on its own, so the
// preset does nothing (#45 Pierre: the saved preset had account="" and
// go-librespot was not running). It is a filesystem check (not a :3678 query), so
// it stays true while go-librespot is mid-restart, distinguishing "never logged
// in" (actionable: log the speaker into Spotify first) from "logged in but
// momentarily down" (recovers on its own).
//
// Current go-librespot persists the zeroconf credential into configDir/state.json
// (under .credentials.username); credentials.json is only its read-only LEGACY
// fallback for installs predating that merged-state layout, so a box logged in
// via a current binary has state.json and NO credentials.json. Checking only
// credentials.json reported every fresh install as not-logged-in and silently
// blocked all Spotify recall, even with an active session (a user diagnostic,
// 2026-06-23: go-librespot loaded its persisted credential and authenticated,
// yet LoggedIn() was false). So check state.json too. go-librespot
// also writes a bare state.json (device_id/last_volume only) before any login, so
// require a non-empty persisted username there rather than mere file presence.
func (m *Manager) LoggedIn() bool {
	if stateHasCredential(filepath.Join(m.configDir, "state.json")) {
		return true
	}
	if _, err := os.Stat(filepath.Join(m.configDir, "credentials.json")); err == nil {
		return true
	}
	// A per-account credential copy (multi-account swap store) also counts: the
	// active credential can be briefly absent during a SwitchAccount swap.
	if entries, err := os.ReadDir(m.credStore); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".json") {
				return true
			}
		}
	}
	return false
}

// stateHasCredential reports whether go-librespot's state.json at path holds a
// persisted zeroconf credential (a non-empty credentials.username). go-librespot
// writes state.json with only device_id/last_volume before any login, so file
// presence alone is not proof of a credential.
func stateHasCredential(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var st struct {
		Credentials struct {
			Username string `json:"username"`
		} `json:"credentials"`
	}
	if json.Unmarshal(data, &st) != nil {
		return false
	}
	return st.Credentials.Username != ""
}

// sanitizeUser maps a Spotify username to a filesystem-safe credential filename.
func sanitizeUser(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// storedCredential is the on-disk shape STR uses for a captured, exported, or
// per-account-stored Spotify credential. It is exactly go-librespot's
// AppState.Credentials, so the same blob loads whether it lands in state.json
// (.credentials, the active account) or credentials.json (go-librespot's legacy
// read-only fallback). Data marshals as base64, matching go-librespot.
type storedCredential struct {
	Username string `json:"username"`
	Data     []byte `json:"data"`
}

// readStateCredential returns the credential go-librespot has persisted in
// state.json (its current store; credentials.json is only a legacy read-fallback
// and is never written by go-librespot). ok is false when none is present.
func (m *Manager) readStateCredential() (storedCredential, bool) {
	b, err := os.ReadFile(filepath.Join(m.configDir, "state.json"))
	if err != nil {
		return storedCredential{}, false
	}
	var s struct {
		Credentials storedCredential `json:"credentials"`
	}
	if json.Unmarshal(b, &s) != nil || s.Credentials.Username == "" || len(s.Credentials.Data) == 0 {
		return storedCredential{}, false
	}
	return s.Credentials, true
}

// writeActiveCredential makes cred the active account by setting .credentials in
// state.json, preserving every other field (device_id, last_volume, ...) so
// go-librespot logs in as that account on its next start. The restart SIGKILLs
// go-librespot (exec.CommandContext cancel), so the outgoing process never saves
// state on the way out and cannot clobber this write. Writing credentials.json
// instead would be ignored whenever the target's state.json already names an
// account, which is why an account switch must land here.
func (m *Manager) writeActiveCredential(cred storedCredential) error {
	if err := os.MkdirAll(m.configDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(m.configDir, "state.json")
	st := map[string]json.RawMessage{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &st) // best-effort merge; a missing/corrupt file just starts fresh
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	st["credentials"] = raw
	out, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}

// captureCredential snapshots go-librespot's current persisted credential into
// the per-account store keyed by username, so a later recall of a preset stamped
// with that account can switch back to it. This is the multi-account case: two
// people each Connect to the same box, go-librespot persists whoever logged in
// last, and each must be saved so each preset replays under its own account.
// Reads state.json (go-librespot's current credential store).
func (m *Manager) captureCredential(user string) error {
	if user == "" {
		return nil
	}
	cred, ok := m.readStateCredential()
	if !ok {
		return fmt.Errorf("no spotify credential to capture")
	}
	blob, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.credStore, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(m.credStore, sanitizeUser(user)+".json"), blob, 0o600)
}

// captureLoop watches the active account and snapshots its credential whenever a
// new account taps the device (go-librespot rewrites state.json on each tap). Low
// NAND wear: it only writes on an account change or a missing copy.
func (m *Manager) captureLoop(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	last := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		user := m.currentUsername(ctx)
		if user == "" {
			continue
		}
		if user == last {
			if _, err := os.Stat(filepath.Join(m.credStore, sanitizeUser(user)+".json")); err == nil {
				continue // already captured
			}
		}
		if err := m.captureCredential(user); err != nil {
			m.logger.Debug("spotify: capture credential failed", "user", user, "err", err)
			continue
		}
		last = user
		m.logger.Info("spotify: captured account credential", "user", user)
	}
}

// SwitchAccount makes go-librespot log in as username if it is not already. It
// returns (false, nil) with no restart when username is empty, already active,
// or has no stored credential (the recall then plays with the current account;
// public playlists still work). Otherwise it swaps the credential and restarts
// go-librespot, waiting until it re-auths as the target.
func (m *Manager) SwitchAccount(ctx context.Context, username string) (bool, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return false, nil
	}
	if cur := m.currentUsername(ctx); cur == username {
		return false, nil // already this account: no switch, no restart
	}
	data, err := os.ReadFile(filepath.Join(m.credStore, sanitizeUser(username)+".json"))
	if err != nil {
		m.logger.Info("spotify: no stored credential for account, playing with current", "want", username)
		return false, nil
	}
	var cred storedCredential
	if err := json.Unmarshal(data, &cred); err != nil || len(cred.Data) == 0 {
		m.logger.Info("spotify: stored credential unreadable, playing with current", "want", username, "err", err)
		return false, nil
	}
	if cred.Username == "" {
		cred.Username = username
	}
	if err := m.writeActiveCredential(cred); err != nil {
		return false, err
	}
	start := time.Now()
	m.mu.Lock()
	restart := m.runCancel
	m.recallRestartAt = start // mark the restart so ServeOgg resumes this cross-account recall on re-attach
	m.mu.Unlock()
	if restart != nil {
		restart() // supervise loop relaunches go-librespot, which reads the swapped credential
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		cur := m.currentUsername(ctx)
		if cur == username {
			m.logger.Info("spotify: switched account", "user", username, "tookMs", time.Since(start).Milliseconds())
			return true, nil
		}
		// If go-librespot re-authed as a DIFFERENT account after the restart,
		// that account's app is still connected and overrides the credential
		// swap. Give up fast; the recall then plays with the active account
		// (public playlists still play). Spotify allows only one live session.
		if cur != "" && cur != username && time.Since(start) > 4*time.Second {
			m.logger.Warn("spotify: account switch overridden by a connected app", "want", username, "got", cur)
			return true, fmt.Errorf("account switch to %q overridden by connected app %q", username, cur)
		}
	}
	m.logger.Warn("spotify: account switch timed out", "want", username)
	return true, fmt.Errorf("account switch to %q timed out", username)
}

// ExportCredential returns the active go-librespot credential (credentials.json)
// so it can be copied to another speaker. The blob is a reusable Spotify Connect
// credential for whatever account last logged in here; copying it to another box
// lets that box log into the SAME Spotify account without the user picking it in
// Spotify again. Returns an error when no credential is stored yet (the box was
// never logged in). LAN-only, same trust model as the rest of the agent API.
func (m *Manager) ExportCredential() ([]byte, error) {
	// The credential lives in state.json on a current go-librespot; export it as a
	// {username,data} blob the receiving box stages back in. Fall back to a legacy
	// credentials.json (same shape) for a box that predates the state.json layout.
	if cred, ok := m.readStateCredential(); ok {
		return json.Marshal(cred)
	}
	data, err := os.ReadFile(filepath.Join(m.configDir, "credentials.json"))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("no spotify credential stored")
	}
	return data, nil
}

// ImportCredential writes a credential blob exported from another speaker into
// this box's go-librespot config and restarts go-librespot so it logs in as that
// account. This is the receiving half of "log in once, sync to all speakers":
// the user logs into Spotify on one box, and STR copies that credential to the
// others so recall works everywhere without tapping each box in Spotify.
func (m *Manager) ImportCredential(ctx context.Context, data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("empty credential")
	}
	if err := os.MkdirAll(m.configDir, 0o755); err != nil {
		return err
	}
	// The blob is go-librespot's {username,data} credential (from another box's
	// state.json, or a legacy credentials.json of the same shape). Set it as the
	// active account in state.json so go-librespot logs in as it on restart, even
	// when the target already named a different account (writing credentials.json
	// would then be ignored). A blob we cannot parse is staged as-is to
	// credentials.json (legacy fallback).
	var cred storedCredential
	if json.Unmarshal(data, &cred) == nil && cred.Username != "" && len(cred.Data) > 0 {
		if err := m.writeActiveCredential(cred); err != nil {
			return err
		}
	} else if err := os.WriteFile(filepath.Join(m.configDir, "credentials.json"), data, 0o600); err != nil {
		return err
	}
	m.logger.Info("spotify: imported credential from another speaker, restarting go-librespot")
	m.mu.Lock()
	restart := m.runCancel
	m.mu.Unlock()
	if restart != nil {
		restart() // supervise loop relaunches go-librespot, which reads the imported credential
	}
	return nil
}
