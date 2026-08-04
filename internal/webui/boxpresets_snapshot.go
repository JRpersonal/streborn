// Box-side preset bookkeeping, snapshots and restore.

package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/boxsnapshot"
	"github.com/JRpersonal/streborn/internal/boxurl"
)

// boxPresetURL returns the stable agent-loopback URL the box should store for a
// preset slot, so a hardware press streams through STR's proxy (which survives
// CDN token expiry) rather than the raw CDN URL. Spotify presets point at the
// per-slot Ogg endpoint because /stream/<slot> has no Spotify source and the
// box's own activation would otherwise flash "service unavailable" (#22). This
// is the one place the box-side preset location is built; both the per-slot
// SetSlot sync and the bulk handleBoxSyncPresets go through it.
func boxPresetURL(slot int, isSpotify bool) string {
	return boxurl.Preset(slot, isSpotify)
}

// handleBoxSyncPresets overwrites the box's own preset list with all
// current stick presets via the Bose CLI. This makes hardware buttons
// 1-6 work again when the initial sync at boot did not run for some
// reason (e.g. the box was not yet reachable).
func (s *Server) handleBoxSyncPresets(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if s.presets == nil || s.boxHost == "" {
		http.Error(w, "presets store or box host not configured", http.StatusServiceUnavailable)
		return
	}
	var specs []boxcli.PresetSpec
	for _, p := range s.presets.All() {
		// Push the agent-loopback proxy URL, NOT p.StreamURL. The raw value is
		// the CDN URL (or, post-v0.7.16, the self-proxy wrapper); storing it on
		// the box defeats the whole point of the proxy slot (token-expiry
		// survival) and a Spotify preset would have no playable box-side source
		// at all. This path must match the per-slot SetSlot sync above.
		stream := boxPresetURL(p.Slot, p.Type == "spotify")
		specs = append(specs, boxcli.PresetSpec{
			Slot:           p.Slot,
			Name:           p.Name,
			StreamURL:      stream,
			NativeLocation: s.nativePresetLocation(p.Name, stream, p.Art),
		})
	}
	syncCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	errs := boxcli.SyncAllPresets(syncCtx, s.boxHost, specs)
	var failed []int
	for slot, err := range errs {
		if err != nil {
			failed = append(failed, slot)
			s.logger.Warn("preset sync failed", "slot", slot, "err", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"synced": len(specs) - len(failed),
		"failed": failed,
	})
}

// NoteBoxPresets records the box's own preset list (from the gabbo
// presetsUpdated frame, via the agent). Replaces the previous snapshot wholesale;
// the box always reports the full list. A slot the user just deleted is dropped
// while its tombstone is fresh, so a trailing presetsUpdated does not resurrect
// it before the box-side removal settles.
func (s *Server) NoteBoxPresets(ps []BoxPreset) {
	s.boxPresetsMu.Lock()
	defer s.boxPresetsMu.Unlock()
	now := time.Now()
	for slot, when := range s.deletedBoxSlots {
		if now.Sub(when) > boxPresetTombstoneTTL {
			delete(s.deletedBoxSlots, slot)
		}
	}
	if len(s.deletedBoxSlots) == 0 {
		s.boxPresets = ps
		return
	}
	filtered := make([]BoxPreset, 0, len(ps))
	for _, p := range ps {
		if _, tombstoned := s.deletedBoxSlots[p.Slot]; tombstoned {
			continue
		}
		filtered = append(filtered, p)
	}
	s.boxPresets = filtered
}

// forgetBoxPreset removes a slot from the box-preset snapshot immediately and
// tombstones it, so a just-deleted preset disappears from the app's merged view
// at once and a trailing box presetsUpdated cannot bring it straight back.
func (s *Server) forgetBoxPreset(slot int) {
	s.boxPresetsMu.Lock()
	defer s.boxPresetsMu.Unlock()
	if s.deletedBoxSlots == nil {
		s.deletedBoxSlots = make(map[int]time.Time)
	}
	s.deletedBoxSlots[slot] = time.Now()
	if len(s.boxPresets) == 0 {
		return
	}
	kept := make([]BoxPreset, 0, len(s.boxPresets))
	for _, p := range s.boxPresets {
		if p.Slot == slot {
			continue
		}
		kept = append(kept, p)
	}
	s.boxPresets = kept
}

// handleBoxPresets serves the box's OWN presets (incl. foreign sources like
// DEEZER that STR did not set), so the app can show and preserve them and recall
// a foreign one via the hardware preset key (Option C). Oldest source of truth is
// the box's gabbo presetsUpdated frame; empty until the box has reported once.
func (s *Server) handleBoxPresets(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	s.boxPresetsMu.Lock()
	// make() never returns nil, so an empty list still marshals to [] not null.
	out := make([]BoxPreset, len(s.boxPresets))
	copy(out, s.boxPresets)
	s.boxPresetsMu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

// handleBoxSnapshot serves the pre-takeover snapshot of the box's presets +
// sources (internal/boxsnapshot) so the app can warn about account-linked cloud
// sources (Deezer, ...) STR cannot carry over and show what was there. Returns
// {"captured":false} when no snapshot exists (feature off, or the box answered
// nothing capturable), so the app can tell "checked, none" from "not yet".
func (s *Server) handleBoxSnapshot(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	if s.snapshotPath == "" {
		writeJSON(w, http.StatusOK, map[string]any{"captured": false})
		return
	}
	data, err := os.ReadFile(s.snapshotPath)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"captured": false})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// serviceKeyOf normalises a source name to the key SourceStatuses uses: upper-
// cased and truncated at the first underscore, so e.g. DEEZER_HIFI maps to the
// same DEEZER status entry.
func serviceKeyOf(source string) string {
	k := strings.ToUpper(strings.TrimSpace(source))
	if i := strings.IndexByte(k, '_'); i > 0 {
		k = k[:i]
	}
	return k
}

// partitionRestorable splits account-linked cloud presets by whether the box's
// own saved login for their service is still valid. A preset whose source the
// box already reports UNAVAILABLE must NOT be written back: the firmware drops a
// preset bound to a dead source within seconds, so writing it only makes the
// button flash and vanish (a reporter watched exactly that). Those services are
// returned as expired (normalised + deduplicated) so the caller reports them
// honestly instead of claiming a restore a reboot can never make stick. A source
// the box does not list at all counts as restorable (unknown, not proven dead).
func partitionRestorable(cloud []boxsnapshot.Preset, statuses map[string]string) (writable []boxsnapshot.Preset, expired []string) {
	expSet := map[string]bool{}
	for _, p := range cloud {
		key := serviceKeyOf(p.Source)
		if statuses[key] == "UNAVAILABLE" {
			expSet[key] = true
			continue
		}
		writable = append(writable, p)
	}
	for k := range expSet {
		expired = append(expired, k)
	}
	return writable, expired
}

// handleBoxSnapshotRestore (EXPERIMENTAL) writes account-linked cloud presets
// (e.g. Deezer) back onto their original slots and re-advertises their sources
// via the reflect-sources file, so the box plays them again through its own
// cached account token. Source of the presets: a posted box /presets XML the
// user saved (presetsXML), or the agent's snapshot when no XML is given. The box
// usually needs a reboot afterwards to re-sync the restored source, so the
// response sets rebootRecommended. LAN-only (it writes to the box).
func (s *Server) handleBoxSnapshotRestore(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "restore only allowed from LAN", http.StatusForbidden)
		return
	}
	if s.boxHost == "" {
		http.Error(w, "box host not configured", http.StatusServiceUnavailable)
		return
	}
	// Body is optional: read raw so an empty body falls back to the snapshot.
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 512*1024))
	var body struct {
		PresetsXML string `json:"presetsXML"`
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
	}

	var presets []boxsnapshot.Preset
	switch {
	case strings.TrimSpace(body.PresetsXML) != "":
		p, err := boxsnapshot.ParsePresetsXML([]byte(body.PresetsXML))
		if err != nil {
			http.Error(w, "could not parse presets XML: "+err.Error(), http.StatusBadRequest)
			return
		}
		presets = p
	case s.snapshotPath != "":
		if snap, err := boxsnapshot.Load(s.snapshotPath); err == nil {
			presets = snap.Presets
		}
	}

	cloud := boxsnapshot.CloudPresets(presets)
	if len(cloud) == 0 {
		// Distinguish "I read buttons but none are account-bound" from "I could
		// not read any buttons at all" (usually a paste of the wrong text), so the
		// UI can give precise guidance instead of one ambiguous message. parsed=0
		// is the case a single <preset> block used to hit before ParsePresetsXML
		// learned to wrap it.
		writeJSON(w, http.StatusOK, map[string]any{
			"restored": []int{}, "parsed": len(presets),
			"message": "no account-linked cloud presets found to restore",
		})
		return
	}

	// Read the box's CURRENT source availability BEFORE writing anything. STR can
	// re-assert a cloud preset (Deezer, ...) only while the box's own saved login
	// for that service is still valid. Once that login has expired with the Bose
	// cloud the source reports UNAVAILABLE, and the firmware then drops any preset
	// bound to it within seconds, so writing such a button just makes it appear and
	// vanish (a reporter watched exactly that on :8090/presets). Detect the expired
	// services up front and DO NOT write their buttons; report them honestly
	// instead. A reboot cannot revive an expired box-side login.
	statuses, _ := boxsnapshot.SourceStatuses(r.Context(), s.boxHost)
	writable, expired := partitionRestorable(cloud, statuses)

	// Re-advertise the still-valid sources so a reboot re-registers them (Path A);
	// harmless for the expired ones.
	if s.reflectPath != "" {
		if err := boxsnapshot.MergeReflect(s.reflectPath, boxsnapshot.ReflectFromPresets(cloud)); err != nil {
			s.logger.Warn("restore: reflect-sources merge failed", "err", err)
		}
	}

	restored := []int{}
	failed := map[string]string{}
	services := map[string]bool{}
	for _, p := range writable {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		err := boxcli.AddPresetRaw(ctx, s.boxHost, p.Slot, p.Source, p.Type, p.Location, p.Name, p.SourceAccount)
		cancel()
		if err != nil {
			failed[fmt.Sprintf("%d", p.Slot)] = err.Error()
			continue
		}
		restored = append(restored, p.Slot)
		services[strings.ToUpper(p.Source)] = true
	}
	svcList := make([]string, 0, len(services))
	for k := range services {
		svcList = append(svcList, k)
	}
	// A source that was valid at the pre-check can still come back UNAVAILABLE if
	// the box rejected it on write: re-read and report those too, kept distinct
	// from the expired ones we never wrote.
	unavailable := []string{}
	if post, err := boxsnapshot.SourceStatuses(r.Context(), s.boxHost); err == nil {
		for svc := range services {
			if post[serviceKeyOf(svc)] == "UNAVAILABLE" {
				unavailable = append(unavailable, svc)
			}
		}
	}
	// A reboot only helps when a button was actually written to a still-valid
	// source; it cannot revive an expired login, so do not offer it otherwise.
	rebootRecommended := len(restored) > 0
	s.logger.Info("box snapshot restore (experimental)", "restored", restored, "failed", len(failed), "services", svcList, "unavailable", unavailable, "expired", expired)
	writeJSON(w, http.StatusOK, map[string]any{
		"restored":          restored,
		"failed":            failed,
		"services":          svcList,
		"unavailable":       unavailable,
		"expired":           expired,
		"rebootRecommended": rebootRecommended,
	})
}

// handleBoxPresetRecall plays one of the box's OWN presets by pressing its
// hardware preset key over the TAP CLI (POST {slot}). The box then plays that
// slot through its own source, which is how a foreign preset (Deezer, played via
// the box's cached account) is recalled from the app without STR having to be a
// Deezer player (Option C). For STR-managed presets the app uses /api/play/<slot>
// instead; this is specifically the path for box-native ones.
func (s *Server) handleBoxPresetRecall(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if s.boxHost == "" {
		http.Error(w, "box host not configured", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Slot int `json:"slot"`
	}
	if !decodeJSONRequest(w, r, 1<<10, &body) {
		return
	}
	if body.Slot < 1 || body.Slot > 6 {
		http.Error(w, "slot must be 1..6", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	if err := boxcli.PresetKey(ctx, s.boxHost, body.Slot, "p"); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "could not recall preset", "detail": err.Error(), "slot": body.Slot})
		return
	}
	s.logger.Info("box preset recalled via hardware key", "slot", body.Slot)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "slot": body.Slot})
}
