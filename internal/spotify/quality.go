package spotify

// Streaming quality: the engine's preferred Vorbis bitrate (96/160/320 kbps).
// Pinned to 160 since the first sidecar build; #728 asked for 320. Applying a
// change follows the same rule the device-name watcher uses: rewrite
// config.yml immediately, restart go-librespot only while nothing is
// streaming, so playback is never cut.
//
// Two things decide the bitrate, in this order:
//
//  1. An explicit choice made for this speaker (the desktop app's selector).
//     It always wins, and it is persisted next to the credential store so it
//     survives OTA agent swaps (they replace only the binary).
//  2. Otherwise the DEFAULT, which follows the account plan: 320 for a Premium
//     account, 160 for anything else, and 160 while the plan is not known.
//     Spotify only serves the 320 Vorbis files to Premium accounts, so 160 is
//     what a Free account gets anyway.
//
// The plan comes from the plan store (accountplans.go). At an engine start the
// account is not logged in yet, so the default is computed from the plan
// remembered for the account whose credential the engine is about to load
// (state.json); since v1.1.1 that is on the NAND for every speaker that has
// checked its plan once. A plan that arrives later (a fresh install, the first
// check) and changes the default does NOT restart the engine there and then:
// config.yml is rewritten, the restart is owed, and it happens at the box's
// next detach from the stream (applyPendingBitrateAfterDetach) or at the next
// engine start, whichever comes first. bitrPlanTried limits that to one owed
// restart per account and target per agent process, so a start that cannot
// see the plan can never ping-pong with the plan read that follows it.
//
// Storage, and the files older agents wrote. sp-bitrate.txt holds the chosen
// bitrate as a bare number, exactly as before, so an agent downgrade still
// reads it. The new sibling sp-bitrate.choice marks it as an explicit choice
// made under the plan-dependent rules. A sp-bitrate.txt WITHOUT the marker
// was written by an older agent, whose selector offered only 160 and 320 with
// 160 as the default and stored nothing while the default ran (selecting 160
// on a speaker already at 160 was a no-op). Such a file only says "160" when
// somebody tried 320 and went back, and going back to 160 was the only way
// back to the default that UI had. So a legacy 160 is read as the default
// (a Premium account now gets 320), while a legacy 96 or 320 can only have
// been chosen and stays an explicit choice. Anyone who wants 160 on a Premium
// account picks it once more and it sticks, now with the marker.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/anonymise"
)

// defaultBitrate is what runs without an explicit choice when the account is
// not Premium or its plan is not known, the value every speaker has
// effectively had since the sidecar shipped.
const defaultBitrate = 160

// premiumDefaultBitrate is the default for a Premium account.
const premiumDefaultBitrate = 320

// bitrateAuto is the stored choice that means "no explicit choice, follow the
// plan". POST /spotify/quality takes it to go back to the default.
const bitrateAuto = 0

// Why the configured bitrate is what it is. Sent to the apps (which translate
// them) and written into the engine-start log line.
const (
	bitrateReasonChosen  = "chosen"          // an explicit choice for this speaker
	bitrateReasonPremium = "premium-default" // no choice, Premium account
	bitrateReasonFree    = "free-default"    // no choice, a plan other than Premium
	bitrateReasonUnknown = "plan-unknown"    // no choice, plan not known (yet)
)

// pendingBitrateGrace is how long a deferred bitrate change waits after a
// detach before restarting the engine, so transient detach/re-attach cycles
// (re-pushes, network blips) do not trigger it. A variable only for the test.
var pendingBitrateGrace = 10 * time.Second

func validBitrate(kbps int) bool {
	return kbps == 96 || kbps == 160 || kbps == 320
}

// effectiveBitrate is the one place the rule lives: an explicit choice wins,
// otherwise the plan decides, and an unknown plan means the old default.
func effectiveBitrate(choice int, plan string) (kbps int, reason string) {
	switch {
	case validBitrate(choice):
		return choice, bitrateReasonChosen
	case plan == "premium":
		return premiumDefaultBitrate, bitrateReasonPremium
	case plan != "":
		return defaultBitrate, bitrateReasonFree
	default:
		return defaultBitrate, bitrateReasonUnknown
	}
}

// bitratePath is the persisted choice, a sibling of sp-accounts and
// sp-resume.json for the same reason those live there.
func bitratePath(configDir string) string {
	return filepath.Join(filepath.Dir(configDir), "sp-bitrate.txt")
}

// bitrateChoiceMarkerPath marks sp-bitrate.txt as an explicit choice made by
// an agent that knows the plan-dependent default (see the file comment).
func bitrateChoiceMarkerPath(configDir string) string {
	return filepath.Join(filepath.Dir(configDir), "sp-bitrate.choice")
}

// loadBitrateChoice reads the persisted choice: a valid bitrate when the user
// chose one, bitrateAuto otherwise. Absent, unreadable or invalid all mean
// automatic; a speaker must never fail to start over this file. A legacy 160
// without the marker is automatic too (the file comment says why).
func loadBitrateChoice(configDir string) int {
	raw, err := os.ReadFile(bitratePath(configDir))
	if err != nil {
		return bitrateAuto
	}
	kbps, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || !validBitrate(kbps) {
		return bitrateAuto
	}
	if _, err := os.Stat(bitrateChoiceMarkerPath(configDir)); err == nil {
		return kbps
	}
	if kbps == defaultBitrate {
		return bitrateAuto
	}
	return kbps
}

// saveBitrateChoice persists a choice: the number plus the marker for an
// explicit one, neither for automatic. Number first, marker second: a power
// cut between the two leaves a marker-less number, which reads as the old
// rules would (a 160 falls back to automatic, which is harmless).
func saveBitrateChoice(configDir string, choice int) error {
	num, marker := bitratePath(configDir), bitrateChoiceMarkerPath(configDir)
	if choice == bitrateAuto {
		if err := os.Remove(num); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.WriteFile(num, []byte(strconv.Itoa(choice)+"\n"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(marker, []byte("explicit\n"), 0o644)
}

// startPlan is the plan the default is computed from before the engine has
// said who it is logged in as: the plan remembered for the account whose
// credential the engine is about to load (state.json), or, when that file
// names nobody (a legacy credentials.json login), for the account the engine
// was last seen logged in as. "" when neither is known.
func (m *Manager) startPlan() string {
	user := ""
	if cred, ok := m.readStateCredential(); ok {
		user = strings.TrimSpace(cred.Username)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if user == "" {
		user = m.bitrLastUser
	}
	if user == "" {
		return ""
	}
	m.loadAccountPlansLocked()
	return m.accountPlans[user].Product
}

// prepareBitrateForStart settles the bitrate of the engine process about to be
// launched; it is the only place a plan-driven default reaches the engine
// without a user asking. config.yml is rewritten only when the bitrate
// changed; the late-joiner header cache is dropped when the new process runs
// at a different bitrate than the previous one. Logs the effective bitrate and
// why, once per start, so a diagnostic bundle answers "what did this speaker
// stream at" without guessing.
func (m *Manager) prepareBitrateForStart() {
	plan := m.startPlan()
	m.mu.Lock()
	kbps, reason := effectiveBitrate(m.bitrChoice, plan)
	cfgChanged := kbps != m.bitr
	m.bitr, m.bitrReason = kbps, reason
	headersStale := kbps != m.bitrStarted
	m.bitrStarted = kbps
	// Whatever restart was owed is paid by this start.
	m.bitrPending = false
	name, vol := m.name, m.configVol
	m.mu.Unlock()
	if cfgChanged {
		if err := os.WriteFile(filepath.Join(m.configDir, "config.yml"),
			[]byte(m.configYAML(name, vol)), 0o644); err != nil {
			m.logger.Warn("spotify: could not write the streaming quality into config.yml", "err", err)
		}
	}
	if headersStale {
		m.invalidateHeaderCache()
	}
	if plan == "" {
		plan = "unknown"
	}
	m.logger.Info("spotify: engine streaming quality", "kbps", kbps, "reason", reason, "plan", plan)
}

// reconcilePlanBitrate runs after a plan answer was recorded. When the speaker
// follows the default and the plan now asks for a different bitrate than the
// engine is configured with, config.yml is rewritten and the restart is owed
// to the next detach or engine start. Never an immediate restart: a plan read
// typically lands right after a login, i.e. while somebody is about to
// listen. One attempt per account and target per agent process (see the file
// comment), so a start that keeps disagreeing cannot loop.
func (m *Manager) reconcilePlanBitrate() {
	// No config dir, no engine config to manage (a bare Manager in a test).
	if m.configDir == "" {
		return
	}
	plan := m.productPlan()
	if plan == "" {
		return
	}
	m.mu.Lock()
	if m.bitrChoice != bitrateAuto {
		m.mu.Unlock()
		return
	}
	kbps, reason := effectiveBitrate(bitrateAuto, plan)
	if kbps == m.bitr {
		m.bitrReason = reason
		m.mu.Unlock()
		return
	}
	user := m.productUser
	key := user + "|" + strconv.Itoa(kbps)
	if m.bitrPlanTried == key {
		m.mu.Unlock()
		return
	}
	m.bitrPlanTried = key
	from := m.bitr
	m.bitr, m.bitrReason = kbps, reason
	m.bitrPending = true
	name, vol := m.name, m.configVol
	m.mu.Unlock()
	if err := os.WriteFile(filepath.Join(m.configDir, "config.yml"),
		[]byte(m.configYAML(name, vol)), 0o644); err != nil {
		m.logger.Warn("spotify: could not write the streaming quality into config.yml", "err", err)
	}
	m.logger.Info("spotify: the account plan changes the default streaming quality, applying when playback stops or the engine restarts",
		"fromKbps", from, "kbps", kbps, "reason", reason, "user", anonymise.MaskAccount(user))
}

// SetBitrate stores an explicit choice (96/160/320) or, with bitrateAuto, goes
// back to the plan-dependent default. It rewrites config.yml and restarts the
// engine when idle, but only when the EFFECTIVE bitrate changes: choosing 320
// on a Premium speaker that already streams 320 by default only records the
// choice. Returns applied=false when a stream is live: the config is already
// written, and the box's next detach from the Ogg stream performs the restart
// (applyPendingBitrateAfterDetach; watchDeviceName would have been the natural
// place, but it is deliberately disabled, see engine.go).
func (m *Manager) SetBitrate(kbps int) (applied bool, err error) {
	if kbps != bitrateAuto && !validBitrate(kbps) {
		return false, fmt.Errorf("bitrate must be 0 (automatic), 96, 160 or 320, got %d", kbps)
	}
	plan := ""
	if kbps == bitrateAuto {
		if plan = m.productPlan(); plan == "" {
			plan = m.startPlan()
		}
	}
	m.mu.Lock()
	sameChoice := m.bitrChoice == kbps
	m.bitrChoice = kbps
	eff, reason := effectiveBitrate(kbps, plan)
	sameEff := m.bitr == eff
	m.bitr, m.bitrReason = eff, reason
	name, vol := m.name, m.configVol
	streaming := m.sink != nil
	pending := m.bitrPending
	restart := m.runCancel
	m.mu.Unlock()
	// NAND write only on an actual change, and the choice survives even when
	// the config write below fails (next start still picks it up).
	if !sameChoice {
		if werr := saveBitrateChoice(m.configDir, kbps); werr != nil {
			m.logger.Warn("spotify: could not persist the streaming quality choice", "err", werr)
		}
	}
	if sameEff {
		return !pending, nil
	}
	if werr := os.WriteFile(filepath.Join(m.configDir, "config.yml"),
		[]byte(m.configYAML(name, vol)), 0o644); werr != nil {
		return false, fmt.Errorf("config rewrite: %w", werr)
	}
	if streaming {
		m.mu.Lock()
		m.bitrPending = true
		m.mu.Unlock()
		m.logger.Info("spotify: bitrate change stored, engine restart waits for playback to stop", "kbps", eff, "reason", reason)
		return false, nil
	}
	// The cached Ogg headers carry the OLD bitrate's Vorbis codebooks. Served
	// to the next attach in front of new-bitrate audio they decode to noise
	// (live on the Portable, 2026-08-26), so they go before the restart does.
	m.invalidateHeaderCache()
	m.logger.Info("spotify: bitrate changed, restarting go-librespot", "kbps", eff, "reason", reason)
	if restart != nil {
		restart()
	}
	return true, nil
}

// applyPendingBitrateAfterDetach performs a deferred bitrate change once the
// box has left the Ogg stream. The 10 s grace keeps it out of transient
// detach/re-attach cycles (re-pushes, network blips): if the box is back on
// the stream by then, nothing happens and the next real detach tries again.
func (m *Manager) applyPendingBitrateAfterDetach() {
	time.Sleep(pendingBitrateGrace)
	m.mu.Lock()
	ok := m.bitrPending && m.sink == nil
	if ok {
		m.bitrPending = false
	}
	kbps := m.bitr
	restart := m.runCancel
	m.mu.Unlock()
	if !ok {
		return
	}
	// config.yml already carries the new bitrate (SetBitrate or
	// reconcilePlanBitrate wrote it); prepareBitrateForStart confirms it.
	m.invalidateHeaderCache()
	m.logger.Info("spotify: applying deferred bitrate change, restarting go-librespot", "kbps", kbps)
	if restart != nil {
		restart()
	}
}

// invalidateHeaderCache drops the late-joiner Ogg header set, in memory and on
// NAND. Header pages are interchangeable within one bitrate profile but NOT
// across bitrates: replaying a 160 kbps header set in front of 320 kbps audio
// configures the box's decoder with the wrong codebooks and it renders noise.
// Called at the moment of a bitrate-change restart; the next stream's own
// headers re-fill the cache (and hdrPersisted re-arms the one-shot NAND write).
func (m *Manager) invalidateHeaderCache() {
	m.mu.Lock()
	m.headerPages = nil
	m.hdrPersisted = false
	m.mu.Unlock()
	if err := os.Remove(m.hdrPath); err != nil && !os.IsNotExist(err) {
		m.logger.Warn("spotify: could not remove the persisted header set", "err", err)
	}
	if err := os.Remove(m.hdrPath + ".kbps"); err != nil && !os.IsNotExist(err) {
		m.logger.Warn("spotify: could not remove the header bitrate marker", "err", err)
	}
}

// qualityState is the GET /spotify/quality answer. bitrate is what config.yml
// carries (and the engine runs at, unless pending); choice is the explicit
// choice, 0 while the plan-dependent default applies; reason says which rule
// produced bitrate. The presence of "choice" tells an app that this speaker
// understands the automatic setting (older agents answer without it).
type qualityState struct {
	Bitrate int    `json:"bitrate"`
	Pending bool   `json:"pending"`
	Choice  int    `json:"choice"`
	Reason  string `json:"reason"`
}

func (m *Manager) qualitySnapshot() qualityState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return qualityState{Bitrate: m.bitr, Pending: m.bitrPending, Choice: m.bitrChoice, Reason: m.bitrReason}
}

// ServeQuality answers GET and POST /spotify/quality. POST {"bitrate": 0}
// returns to the plan-dependent default; 96/160/320 is an explicit choice.
func (m *Manager) ServeQuality(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		_ = json.NewEncoder(w).Encode(m.qualitySnapshot())
	case http.MethodPost:
		var req struct {
			Bitrate int `json:"bitrate"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		applied, err := m.SetBitrate(req.Bitrate)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		st := m.qualitySnapshot()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "bitrate": st.Bitrate, "applied": applied,
			"pending": st.Pending, "choice": st.Choice, "reason": st.Reason,
		})
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}
