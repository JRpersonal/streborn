package main

// A speaker's preset keys survive an STR removal on THIS PC.
//
// The STR preset store lives in the STR folder on the speaker, and "STR
// Remove" deletes that folder. A reinstall therefore starts with an empty
// store, while the firmware keeps the six keys the old install had registered:
// the desktop showed six playable stations, every press played nothing, the
// phone remote showed six empty keys, and the user was left to copy the
// presets from another speaker by hand (#882, three SoundTouch 10s).
//
// So the removal reads the keys first and keeps them here, keyed by the
// speaker's deviceID (its IP when the app never learned the id), under the
// same user-config folder as known-speakers.json. The next install from this
// app reads the fresh store, and when it is empty puts the keys back and
// re-registers the hardware buttons. A stash that is not needed (the store
// already has presets) is dropped, so it can never overwrite keys the user set
// meanwhile. purgeSpeakerState leaves the stash alone on purpose: it records
// what was, like ota-history.log, and it is exactly the record the reinstall
// needs.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// presetStash is one speaker's kept keys.
type presetStash struct {
	DeviceID string    `json:"deviceID,omitempty"`
	Host     string    `json:"host,omitempty"`
	SavedAt  time.Time `json:"savedAt"`
	Presets  []Preset  `json:"presets"`
}

// presetStashHostMaxAge bounds the IP-keyed fallback: an IP is reused by
// DHCP, so a stash that could only be keyed by the speaker's address is
// trusted for a few days, long enough for a remove-and-reinstall.
const presetStashHostMaxAge = 7 * 24 * time.Hour

// stashSSHRead is the fallback read of the store file over the SSH session
// the removal already holds, for a speaker whose agent port this PC cannot
// reach (an ST10's firewall drops it) or whose agent is already gone. A seam
// for the tests.
var stashSSHRead = func(host string) (string, error) {
	return boxSSHOutput(host, "cat /mnt/nv/streborn/presets.json 2>/dev/null", 10*time.Second)
}

var presetStashKeyRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// presetStashKeys lists the file keys a speaker's stash may live under, the
// deviceID first, then the IP-keyed fallback.
func presetStashKeys(deviceID, host string) []string {
	var keys []string
	if id := strings.TrimSpace(deviceID); id != "" {
		keys = append(keys, strings.ToUpper(presetStashKeyRe.ReplaceAllString(id, "_")))
	}
	if h := strings.TrimSpace(host); h != "" {
		keys = append(keys, "host-"+presetStashKeyRe.ReplaceAllString(h, "_"))
	}
	return keys
}

func presetStashPath(key string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ST Reborn", "preset-stash", key+".json"), nil
}

// savePresetStash keeps presets for the speaker under its best key.
func savePresetStash(deviceID, host string, presets []Preset) error {
	keys := presetStashKeys(deviceID, host)
	if len(keys) == 0 {
		return errors.New("no deviceID and no host to key the stash by")
	}
	path, err := presetStashPath(keys[0])
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(presetStash{DeviceID: deviceID, Host: host, SavedAt: time.Now(), Presets: presets}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// loadPresetStash finds the speaker's kept keys: by deviceID, else by IP
// (bounded by presetStashHostMaxAge). ok is false when there are none.
func loadPresetStash(deviceID, host string) (presetStash, bool) {
	for i, key := range presetStashKeys(deviceID, host) {
		path, err := presetStashPath(key)
		if err != nil {
			return presetStash{}, false
		}
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var st presetStash
		if json.Unmarshal(b, &st) != nil || len(st.Presets) == 0 {
			continue
		}
		byHost := strings.HasPrefix(key, "host-")
		if byHost && (i > 0 || deviceID != "") && time.Since(st.SavedAt) > presetStashHostMaxAge {
			continue
		}
		return st, true
	}
	return presetStash{}, false
}

// dropPresetStash removes the speaker's kept keys under every key they may
// live under. Best-effort.
func dropPresetStash(deviceID, host string) {
	for _, key := range presetStashKeys(deviceID, host) {
		if path, err := presetStashPath(key); err == nil {
			_ = os.Remove(path)
		}
	}
}

// parseStoreFile reads the agent's presets.json (the {"presets":[...]}
// wrapper the store writes; a bare list is accepted too).
func parseStoreFile(raw string) ([]Preset, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("empty presets.json")
	}
	var wrapper struct {
		Presets []Preset `json:"presets"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapper); err == nil {
		return wrapper.Presets, nil
	}
	var list []Preset
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, err
	}
	return list, nil
}

// stashPresetsBeforeUninstall reads the speaker's preset store and keeps it on
// this PC. The agent's HTTP API first (the removal has just used it to open
// SSH), the store file over SSH as the fallback. port 0 lets the transport
// pick the agent port. Returns the number of keys kept; never fails the
// removal.
func (a *App) stashPresetsBeforeUninstall(host string, port int, deviceID string) int {
	presets, err := a.GetPresets(host, port)
	if err != nil || len(presets) == 0 {
		raw, serr := stashSSHRead(host)
		if serr == nil {
			if p, perr := parseStoreFile(raw); perr == nil && len(p) > 0 {
				presets, err = p, nil
			} else if perr != nil && err == nil {
				err = perr
			}
		} else if err == nil {
			err = serr
		}
	}
	var kept []Preset
	for _, p := range presets {
		if p.Slot >= 1 && p.Slot <= 6 && p.Name != "" {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		a.logger.Info("uninstall_str: no preset keys to keep for a later install", "host", host, "err", err)
		return 0
	}
	if serr := savePresetStash(deviceID, host, kept); serr != nil {
		a.logger.Warn("uninstall_str: could not keep the speaker's preset keys on this PC", "host", host, "err", serr)
		return 0
	}
	a.logger.Info("uninstall_str: kept the speaker's preset keys on this PC for the next install",
		"host", host, "deviceID", deviceID, "count", len(kept))
	return len(kept)
}

// restorePresetStashAfterInstall resolves the fresh agent's port and the
// speaker's deviceID and puts the kept keys back. Returns how many landed.
func (a *App) restorePresetStashAfterInstall(host string) int {
	ctx, cancel := context.WithTimeout(a.appCtx(), 20*time.Second)
	defer cancel()
	port, deviceID := 0, ""
	if bi, ok := probeSTRWithRetry(ctx, host, 3); ok {
		port, deviceID = bi.Port, bi.DeviceID
	}
	if deviceID == "" {
		deviceID = a.deviceIDForHost(host)
	}
	return a.restorePresetStash(host, port, deviceID)
}

// restorePresetStash puts the kept keys back onto a speaker whose fresh store
// is empty, re-registers the hardware buttons, journals it, and drops the
// stash. A store that already holds presets is left alone and the stash is
// dropped, so kept keys can never overwrite keys the user set since. A store
// that cannot be read keeps the stash for a later install.
func (a *App) restorePresetStash(host string, port int, deviceID string) int {
	st, ok := loadPresetStash(deviceID, host)
	if !ok {
		return 0
	}
	fresh, err := a.GetPresets(host, port)
	if err != nil {
		a.logger.Warn("install_str: could not read the fresh preset store; the kept keys stay on this PC for a later install",
			"host", host, "kept", len(st.Presets), "err", err)
		return 0
	}
	if len(fresh) > 0 {
		a.logger.Info("install_str: the fresh preset store already holds presets, the kept keys are not needed",
			"host", host, "count", len(fresh), "kept", len(st.Presets))
		dropPresetStash(deviceID, host)
		return 0
	}
	restored := 0
	var slotErrs []string
	for _, p := range st.Presets {
		if p.Slot < 1 || p.Slot > 6 || p.Name == "" {
			continue
		}
		if err := a.boxPut(host, port, fmt.Sprintf("%s/%d", presetAPIPath, p.Slot), p); err != nil {
			a.logger.Warn("install_str: the speaker rejected a kept preset key", "host", host, "slot", p.Slot, "err", err)
			slotErrs = append(slotErrs, fmt.Sprintf("preset %d (%s): %v", p.Slot, p.Name, err))
			continue
		}
		restored++
	}
	if restored == 0 {
		a.logger.Warn("install_str: none of the kept preset keys landed; the stash stays on this PC", "host", host, "errors", strings.Join(slotErrs, "; "))
		return 0
	}
	// Re-push the hardware keys so buttons 1-6 on the speaker match at once,
	// exactly as the box-to-box copy does.
	if _, err := a.SyncBoxPresets(host, port); err != nil {
		a.logger.Warn("install_str: hardware key sync after the restore failed (the agent's reconcile registers them within minutes)", "host", host, "err", err)
	}
	a.recordOTA(host, fmt.Sprintf("install: restored %d preset keys from the pre-uninstall stash (kept %s, slotErrs=%d)",
		restored, st.SavedAt.Format(time.RFC3339), len(slotErrs)))
	a.logger.Info("install_str: put the kept preset keys back onto the speaker", "host", host, "deviceID", deviceID,
		"restored", restored, "slotErrs", len(slotErrs))
	dropPresetStash(deviceID, host)
	return restored
}
