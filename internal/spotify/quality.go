package spotify

// Streaming quality: the engine's preferred Vorbis bitrate (96/160/320 kbps).
// Pinned to 160 since the first sidecar build; #728 asked for 320. The choice
// is per speaker and persisted next to the credential store, so it survives
// OTA agent swaps (they replace only the binary). Applying it follows the same
// rule the device-name watcher uses: rewrite config.yml immediately, restart
// go-librespot only while nothing is streaming, so playback is never cut.
//
// 320 is deliberately opt-in rather than the default: Spotify serves the 320
// Vorbis files to Premium accounts, and how cleanly the engine degrades for a
// Free account was not verifiable from code alone (#728 carries that caveat).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// defaultBitrate is what runs without a stored preference, the value every
// speaker has effectively had since the sidecar shipped.
const defaultBitrate = 160

func validBitrate(kbps int) bool {
	return kbps == 96 || kbps == 160 || kbps == 320
}

// bitratePath is the persisted preference file, a sibling of sp-accounts and
// sp-resume.json for the same reason those live there.
func bitratePath(configDir string) string {
	return filepath.Join(filepath.Dir(configDir), "sp-bitrate.txt")
}

// loadBitrate reads the persisted preference. Absent, unreadable or invalid
// all mean the default; a speaker must never fail to start over this file.
func loadBitrate(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return defaultBitrate
	}
	kbps, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || !validBitrate(kbps) {
		return defaultBitrate
	}
	return kbps
}

// SetBitrate persists the preference, rewrites config.yml and restarts the
// engine when idle. Returns applied=false when a stream is live: the config is
// already written, and watchDeviceName's next idle tick does the restart (the
// pending flag below is its trigger).
func (m *Manager) SetBitrate(kbps int) (applied bool, err error) {
	if !validBitrate(kbps) {
		return false, fmt.Errorf("bitrate must be 96, 160 or 320, got %d", kbps)
	}
	m.mu.Lock()
	same := m.bitr == kbps
	m.bitr = kbps
	name, vol := m.name, m.configVol
	streaming := m.sink != nil
	restart := m.runCancel
	m.mu.Unlock()
	if same {
		return true, nil
	}
	// NAND write only on an actual change, and the preference survives even
	// when the config write below fails (next start still picks it up).
	if werr := os.WriteFile(bitratePath(m.configDir), []byte(strconv.Itoa(kbps)+"\n"), 0o644); werr != nil {
		m.logger.Warn("spotify: could not persist the bitrate preference", "err", werr)
	}
	if werr := os.WriteFile(filepath.Join(m.configDir, "config.yml"),
		[]byte(m.configYAML(name, vol)), 0o644); werr != nil {
		return false, fmt.Errorf("config rewrite: %w", werr)
	}
	if streaming {
		m.mu.Lock()
		m.bitrPending = true
		m.mu.Unlock()
		m.logger.Info("spotify: bitrate change stored, engine restart waits for playback to stop", "kbps", kbps)
		return false, nil
	}
	// The cached Ogg headers carry the OLD bitrate's Vorbis codebooks. Served
	// to the next attach in front of new-bitrate audio they decode to noise
	// (live on the Portable, 2026-08-26), so they go before the restart does.
	m.invalidateHeaderCache()
	m.logger.Info("spotify: bitrate changed, restarting go-librespot", "kbps", kbps)
	if restart != nil {
		restart()
	}
	return true, nil
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

// ServeQuality answers GET and POST /spotify/quality.
func (m *Manager) ServeQuality(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		m.mu.Lock()
		kbps, pending := m.bitr, m.bitrPending
		m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"bitrate": kbps, "pending": pending})
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
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "bitrate": req.Bitrate, "applied": applied})
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}
