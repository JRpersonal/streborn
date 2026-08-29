package main

// Backup and restore of the user's STR configuration (#778): the radio
// favorites kept by the desktop app and the six preset slots of every
// speaker, written to one JSON file the user chooses. The point is peace of
// mind before an update and a way back after a mishap, so the file format is
// plain JSON a person can read, and restoring is additive: it writes the
// backed-up slots and touches nothing else.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// backupKind is the file's self-identification; ImportBackup refuses anything
// else so a random JSON file cannot be "restored" into the speakers.
const backupKind = "str-backup"

// backupVersion is bumped only when the format changes incompatibly. Import
// accepts files up to and including this version.
const backupVersion = 1

// backupMaxBytes caps the import read. A real backup is a few hundred KB at
// most (queue presets carry track lists); this is a safety bound, not a limit
// a legitimate file can hit.
const backupMaxBytes = 8 << 20

// backupSpeaker is one speaker's share of a backup: enough identity to find
// the speaker again later (the deviceID is stable, the name is what the user
// recognizes, the host is informational) and the preset slots verbatim.
type backupSpeaker struct {
	Name     string   `json:"name"`
	Model    string   `json:"model,omitempty"`
	DeviceID string   `json:"deviceId,omitempty"`
	Host     string   `json:"host,omitempty"`
	Presets  []Preset `json:"presets"`
}

// backupFile is the whole on-disk document. Favorites stay raw JSON on
// purpose: the frontend owns that schema (its localStorage station list), and
// the backup must round-trip it verbatim rather than mirror it in Go and
// silently drop a field the frontend adds later (the queue-preset Items field
// taught that lesson once already).
type backupFile struct {
	Kind       string          `json:"kind"`
	Version    int             `json:"version"`
	CreatedAt  string          `json:"createdAt"`
	AppVersion string          `json:"appVersion"`
	Favorites  json.RawMessage `json:"favorites,omitempty"`
	Speakers   []backupSpeaker `json:"speakers"`
}

// backupBoxes snapshots the discovery cache: the STR speakers a backup can
// cover, offline ones included (the caller decides what reaching out to them
// would mean).
func (a *App) backupBoxes() []BoxInfo {
	a.discMu.Lock()
	defer a.discMu.Unlock()
	var out []BoxInfo
	for _, e := range a.discCache {
		if e.box.Kind == "stock" || e.box.Host == "" {
			continue
		}
		out = append(out, e.box)
	}
	return out
}

// ExportBackup gathers the favorites (handed in by the frontend, which owns
// them) and every reachable speaker's presets, then writes the backup file
// wherever the user points the save dialog. Speakers that could not be read
// are reported by name rather than silently left out: a backup whose gaps are
// invisible is worse than none.
func (a *App) ExportBackup(favoritesJSON string) (map[string]any, error) {
	doc := backupFile{
		Kind:       backupKind,
		Version:    backupVersion,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		AppVersion: appVersion,
	}
	favCount := 0
	if s := strings.TrimSpace(favoritesJSON); s != "" {
		var favs []json.RawMessage
		if err := json.Unmarshal([]byte(s), &favs); err != nil {
			return nil, fmt.Errorf("favorites payload is not a JSON list: %w", err)
		}
		favCount = len(favs)
		doc.Favorites = json.RawMessage(s)
	}
	var skipped []string
	for _, b := range a.backupBoxes() {
		label := b.FriendlyName
		if label == "" {
			label = b.Name
		}
		// An offline speaker cannot be asked for its presets, and waiting for
		// its transport timeouts would stall the whole export (one unplugged
		// box, field 2026-08-29). It goes on the skipped list up front.
		if b.Offline {
			skipped = append(skipped, label)
			continue
		}
		presets, err := a.GetPresets(b.Host, b.Port)
		if err != nil {
			a.logger.Warn("backup: reading presets failed", "host", b.Host, "err", err)
			skipped = append(skipped, label)
			continue
		}
		doc.Speakers = append(doc.Speakers, backupSpeaker{
			Name:     label,
			Model:    b.Model,
			DeviceID: b.DeviceID,
			Host:     b.Host,
			Presets:  presets,
		})
	}
	if favCount == 0 && len(doc.Speakers) == 0 {
		return nil, fmt.Errorf("nothing to back up: no favorites and no reachable speaker")
	}
	path, err := wailsruntime.SaveFileDialog(a.ctx, wailsruntime.SaveDialogOptions{
		DefaultFilename: "str-backup-" + time.Now().Format("2006-01-02") + ".json",
		Title:           "Save STR backup",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "STR backup (*.json)", Pattern: "*.json"},
		},
	})
	if err != nil {
		return nil, err
	}
	if path == "" {
		return map[string]any{"canceled": true}, nil
	}
	blob, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		return nil, err
	}
	presetCount := 0
	for _, s := range doc.Speakers {
		presetCount += len(s.Presets)
	}
	a.logger.Info("backup: written", "path", path,
		"favorites", favCount, "speakers", len(doc.Speakers), "presets", presetCount, "skipped", len(skipped))
	return map[string]any{
		"path":      path,
		"favorites": favCount,
		"speakers":  len(doc.Speakers),
		"presets":   presetCount,
		"skipped":   skipped,
	}, nil
}

// backupMatchTarget finds the current speaker a backed-up entry belongs to:
// the stable deviceID decides when it is known, the display name is the
// fallback (a reinstalled agent can change how the id was recorded, but the
// user keeps calling the kitchen speaker "Küche").
func backupMatchTarget(s backupSpeaker, boxes []BoxInfo) *BoxInfo {
	for i := range boxes {
		if s.DeviceID != "" && strings.EqualFold(boxes[i].DeviceID, s.DeviceID) {
			return &boxes[i]
		}
	}
	name := strings.TrimSpace(strings.ToLower(s.Name))
	if name == "" {
		return nil
	}
	for i := range boxes {
		label := boxes[i].FriendlyName
		if label == "" {
			label = boxes[i].Name
		}
		if strings.TrimSpace(strings.ToLower(label)) == name {
			return &boxes[i]
		}
	}
	return nil
}

// ImportBackup lets the user pick a backup file, restores each backed-up
// speaker's presets onto the speaker it matches today, and hands the
// favorites back to the frontend (which merges them into its own store).
//
// Restore semantics are write-only: every backed-up slot is PUT verbatim
// (the same round-trip the box-to-box copy uses), slots the backup does not
// name are left as they are, and a speaker that is offline or no longer part
// of the household is reported, not guessed at.
func (a *App) ImportBackup() (map[string]any, error) {
	path, err := wailsruntime.OpenFileDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title: "Open STR backup",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "STR backup (*.json)", Pattern: "*.json"},
		},
	})
	if err != nil {
		return nil, err
	}
	if path == "" {
		return map[string]any{"canceled": true}, nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.Size() > backupMaxBytes {
		return nil, fmt.Errorf("this file is too large to be an STR backup")
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc backupFile
	if err := json.Unmarshal(blob, &doc); err != nil || doc.Kind != backupKind {
		return nil, fmt.Errorf("this is not an STR backup file")
	}
	if doc.Version > backupVersion {
		return nil, fmt.Errorf("this backup was written by a newer STR version; update the app first")
	}
	boxes := a.backupBoxes()
	restored := 0
	restoredSpeakers := 0
	var missed []string
	for _, s := range doc.Speakers {
		if len(s.Presets) == 0 {
			continue
		}
		target := backupMatchTarget(s, boxes)
		if target == nil || target.Offline {
			missed = append(missed, s.Name)
			continue
		}
		wrote := 0
		var slotErr error
		for _, p := range s.Presets {
			if p.Slot < 1 || p.Slot > 6 || p.Name == "" {
				continue
			}
			if err := a.boxPut(target.Host, target.Port, fmt.Sprintf("%s/%d", presetAPIPath, p.Slot), p); err != nil {
				a.logger.Warn("backup: restoring a preset failed",
					"host", target.Host, "slot", p.Slot, "err", err)
				slotErr = err
				continue
			}
			wrote++
		}
		if wrote > 0 {
			restored += wrote
			restoredSpeakers++
			// Hardware keys 1-6 must match what was just written, same step as
			// the box-to-box copy. Best effort: the presets themselves landed.
			if _, err := a.SyncBoxPresets(target.Host, target.Port); err != nil {
				a.logger.Warn("backup: hardware key sync failed", "host", target.Host, "err", err)
			}
		} else if slotErr != nil {
			missed = append(missed, s.Name)
		}
	}
	a.logger.Info("backup: restored", "path", path,
		"speakers", restoredSpeakers, "presets", restored, "missed", len(missed))
	return map[string]any{
		"favorites": string(doc.Favorites),
		"speakers":  restoredSpeakers,
		"presets":   restored,
		"missed":    missed,
	}, nil
}
