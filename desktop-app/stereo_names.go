package main

// STR-side stereo-pair names (Rolf Krause, mail 2026-08-27, points 1+2).
//
// A stereo pair carries a name field end to end already, but the box side is
// fragile: the firmware name a user sets in the Bose app is wiped by a
// reconfigure, and the desktop badge never rendered a stored name at all (it
// drew the constant "Stereo pair" heading). App-first fix: the desktop app is
// the sole owner of the DISPLAY name and keeps it here, in the same durable
// OS user-config dir that app-state.json uses, so it survives an app update, a
// reinstall, and an agent OTA (which reflashes only the agent binary).
//
// The name is keyed on the unordered SET of the pair's two member SoundTouch
// deviceIDs (the frontend computes the key; see groups.js stereoPairKey). Those
// deviceIDs are firmware SCM MACs that never change, so the name survives a
// reboot, standby, and even an L/R swap (the key is the sorted set, not the
// master alone). Because the store is app-side and keyed on live members, a
// stale pair produces no stale name: a name only renders when a live pair with
// exactly those two members is reported.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var stereoNamesMu sync.Mutex

func stereoNamesPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ST Reborn", "stereo-pair-names.json"), nil
}

func readStereoNames() map[string]string {
	m := map[string]string{}
	path, err := stereoNamesPath()
	if err != nil {
		return m
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

// applyStereoName is the pure store mutation: a trimmed name is stored, and a
// blank name deletes the key so a cleared rename reverts to the default
// "Stereo pair" heading rather than persisting an empty string. Returns the
// same map for convenience.
func applyStereoName(m map[string]string, key, name string) map[string]string {
	if m == nil {
		m = map[string]string{}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		delete(m, key)
	} else {
		m[key] = name
	}
	return m
}

// GetStereoPairName returns the user-given display name for a stereo pair, keyed
// on the sorted set of its member deviceIDs, or "" when none is stored. The
// frontend falls back to the "Stereo pair" heading on "".
func (a *App) GetStereoPairName(key string) string {
	stereoNamesMu.Lock()
	defer stereoNamesMu.Unlock()
	return readStereoNames()[key]
}

// SetStereoPairName persists the display name for a stereo pair. A blank name
// removes the entry (reverts to the default heading). Best-effort and atomic
// (temp file + rename), mirroring SetAppFlag; a write failure is returned but
// the caller treats it as non-fatal.
func (a *App) SetStereoPairName(key, name string) error {
	stereoNamesMu.Lock()
	defer stereoNamesMu.Unlock()
	if strings.TrimSpace(key) == "" {
		return nil
	}
	m := applyStereoName(readStereoNames(), key, name)
	path, err := stereoNamesPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
