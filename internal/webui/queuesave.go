package webui

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
)

// Saving the folder that plays right now as a key (#1030).
//
// A media-server folder plays as an agent-side queue: every track is a UPnP
// push, so "what is playing" is a single file and a key saved from it was a
// plain radio link to that one track, without the "from <server>" line. The
// queue itself knows the whole folder, so both save gestures (the app's hold
// on a key tile and the speaker's own hold on a hardware key) build the folder
// preset from it here, the same preset the Library tab's star button stores.

// queueFolderFallbackName names a saved folder whose queue carried neither a
// folder name nor a media-server name (an app too old to send either).
const queueFolderFallbackName = "Music folder"

// queuePresetFromLive builds the folder preset for slot from a live queue's
// tracks, shuffle flag and folder identity. It mirrors the desktop app's
// librarySaveFolderAsPreset field for field: name, type queue, shuffle,
// source, and the tracks in folder order with url/title/art/mime/duration/
// artist. No preset-level art, exactly like the star button. The track list
// is capped at presets.MaxQueueItems, the cap the store applies anyway.
func queuePresetFromLive(slot int, items []queueItem, shuffle bool, folder recentCardCtx) (presets.Preset, bool) {
	out := make([]presets.PresetItem, 0, len(items))
	for _, it := range items {
		if it.URL == "" {
			continue
		}
		out = append(out, presets.PresetItem{
			URL:         it.URL,
			Title:       it.Title,
			Art:         it.Art,
			Mime:        it.Mime,
			DurationSec: int(it.Duration / time.Second),
			Artist:      it.Artist,
		})
		if len(out) == presets.MaxQueueItems {
			break
		}
	}
	if len(out) == 0 {
		return presets.Preset{}, false
	}
	source := strings.TrimSpace(folder.source)
	name := strings.TrimSpace(folder.name)
	if name == "" {
		name = source
	}
	if name == "" {
		name = queueFolderFallbackName
	}
	return presets.Preset{
		Slot:    slot,
		Name:    name,
		Type:    "queue",
		Shuffle: shuffle,
		Source:  source,
		Items:   out,
	}, true
}

// LiveQueuePreset is the folder preset for slot built from the queue that
// plays now; ok is false when no queue is active.
//
// A queue that was itself recalled from a folder key ("queue:slot:N") is that
// key's preset, so it is copied whole onto slot: a re-save of a recalled
// folder then recalls exactly like the original, whatever the original was
// saved with.
func (s *Server) LiveQueuePreset(slot int) (presets.Preset, bool) {
	if s.queue == nil {
		return presets.Preset{}, false
	}
	items, shuffle, active := s.queue.contents()
	if !active {
		return presets.Preset{}, false
	}
	s.queueMu.Lock()
	folder := s.queueFolder
	s.queueMu.Unlock()
	if rest, ok := strings.CutPrefix(folder.key, "queue:slot:"); ok && s.presets != nil {
		if n, err := strconv.Atoi(rest); err == nil {
			if src, have := s.presets.Get(n); have && src.Type == "queue" && len(src.Items) > 0 {
				src.Slot = slot
				return src, true
			}
		}
	}
	if folder.source == "" {
		folder.source = s.queueFolderServerName(folder.key)
	}
	return queuePresetFromLive(slot, items, shuffle, folder)
}

// queueFolderServerName resolves the media-server name for a folder card key
// ("queue:<udn>:<container>") from the registered servers, for queues started
// by an app that did not send the name along.
func (s *Server) queueFolderServerName(key string) string {
	if s.mediaServers == nil || !strings.HasPrefix(key, queueCardKeyPrefix) {
		return ""
	}
	udn, _, ok := s.splitQueueCardKey(key)
	if !ok {
		return ""
	}
	for _, reg := range s.mediaServers.List() {
		if udnKey(reg.ID) == udnKey(udn) {
			return reg.Name
		}
	}
	return ""
}

// storeQueuePreset writes a folder preset to the store and registers its key
// on the box. The physical press is intercepted by RecallSlot (which starts
// the queue), but the box still needs an entry for the key to fire at all, so
// it points at this slot's stream proxy URL like every other preset. A failed
// box write is logged, not returned: the store holds the key and the
// reconcile writes the box later.
func (s *Server) storeQueuePreset(ctx context.Context, p presets.Preset) error {
	if err := s.presets.SetSlot(p); err != nil {
		return err
	}
	if s.boxHost != "" {
		boxCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := s.writeBoxPreset(boxCtx, p.Slot, p.Name, boxPresetURL(p.Slot, false), p.Art, false); err != nil {
			s.logger.Warn("box preset sync failed", "slot", p.Slot, "err", err)
		}
		cancel()
	}
	return nil
}

// handleQueueSaveSlot answers POST /api/queue/save-slot {"slot": N}: store the
// folder that plays now as a queue preset on key N (the app's hold-to-save
// while a library folder plays, #1030). 409 with code "no-queue" when no
// folder plays, so the app can fall back to its other save paths.
func (s *Server) handleQueueSaveSlot(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if s.presets == nil {
		http.Error(w, "presets store not initialized", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Slot int `json:"slot"`
	}
	if !decodeJSONRequest(w, r, 1<<12, &req) {
		return
	}
	if req.Slot < 1 || req.Slot > 6 {
		http.Error(w, "invalid slot, must be 1-6", http.StatusBadRequest)
		return
	}
	p, ok := s.LiveQueuePreset(req.Slot)
	if !ok {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "No music-library folder is playing right now.",
			"code":  "no-queue",
		})
		return
	}
	if err := s.storeQueuePreset(r.Context(), p); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.logger.Info("queue preset save: stored the playing folder on a key",
		"slot", p.Slot, "name", p.Name, "source", p.Source, "tracks", len(p.Items), "shuffle", p.Shuffle)
	writeJSON(w, http.StatusOK, p)
}
