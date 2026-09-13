// Renaming a preset: the one write path that changes what a key is CALLED and
// nothing else.
//
// Station names come straight out of the radio directory, and there they are
// sometimes shouted in capitals, wrong, or long enough to fill a whole tile
// ("the names are sometimes absurd, I want to rename what sits on my own keys",
// discussion #812). The store has always carried a free-form Name, so this only
// needs a way to write it.
//
// It deliberately does NOT go through the PUT save path, even though a rename
// could be expressed as a re-save of the same preset. Two things on that path
// are about the STATION, and a rename changes no station:
//
//   - the web-page probe (looksLikeWebPage) makes an outbound request per write
//     and refuses on positive evidence of an HTML page. A key saved before that
//     probe existed can hold such a URL, so renaming it would be answered with
//     "that address is the station's website" and the user could never fix the
//     name of a key that plays.
//   - the Spotify enrichment refills the name from the playlist title, and the
//     account/shuffle refresh fires against the live engine.
//
// The duplicate guard (#836, one station on at most one key) is not a problem
// either way: it skips the slot being written, so a same-slot rename can never
// collide with itself. Coming in on its own verb keeps that true by
// construction rather than by a `continue` several hundred lines away.

package webui

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// maxPresetNameRunes bounds a typed-in preset name. The name is written on to
// the speaker as well (writeBoxPreset), where it lands in the firmware's preset
// XML, and the box caps a POST body at 1536 bytes; it also has to stay readable
// in a tile that is a third of the app window. Runes, not bytes, so a Japanese
// or Ukrainian name is not cut a third of the way in.
const maxPresetNameRunes = 64

// handlePresetRename serves PATCH /api/presets/<slot> with {"name":"..."}: it
// replaces the stored name of an existing preset and leaves every other field
// untouched. Answers the renamed preset, so a client can render the result
// without re-reading the whole store.
func (s *Server) handlePresetRename(w http.ResponseWriter, r *http.Request, slot int) {
	var in struct {
		Name string `json:"name"`
	}
	// A name is a few dozen characters; there is no reason to read more.
	if !decodeJSONRequest(w, r, 4<<10, &in) {
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		// An empty name leaves the key showing "Key 3" with no way to tell what
		// is on it, which is the opposite of what renaming is for. Refused in
		// the same {error, code} shape as the save gates.
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": "A preset needs a name. Type one and try again.",
			"code":  "name-empty",
		})
		return
	}
	if rs := []rune(name); len(rs) > maxPresetNameRunes {
		s.logger.Info("preset rename: name shortened to fit the speaker's preset entry",
			"slot", slot, "requested", len(rs), "kept", maxPresetNameRunes)
		name = string(rs[:maxPresetNameRunes])
	}
	p, ok := s.presets.Get(slot)
	if !ok {
		http.Error(w, "preset not set", http.StatusNotFound)
		return
	}
	if p.Name == name {
		// Nothing to do, and saying so costs the speaker's flash nothing: every
		// store write rewrites presets.json plus its backup on the NAND.
		writeJSON(w, http.StatusOK, p)
		return
	}
	prev := p.Name
	p.Name = name
	if err := s.presets.SetSlot(p); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Same forensic rule as the save and the delete: a key whose label changed
	// must name who changed it, or a later bundle cannot explain the name a user
	// no longer recognises.
	s.logger.Info("preset rename accepted",
		"slot", slot, "was", prev, "now", name, "type", p.Type,
		"from", r.RemoteAddr, "ua", r.Header.Get("User-Agent"))
	// Push the new label to the speaker so its own display and the hardware key
	// show it too; without this the rename would only exist in the app and the
	// box would keep announcing the old name on every press. Best-effort, like
	// the save path: the store already holds the name and a failed push is
	// repaired by the next /api/box/sync-presets.
	if s.boxHost != "" {
		boxCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		if err := s.writeBoxPreset(boxCtx, slot, p.Name, boxPresetURL(slot, p.Type == "spotify"), p.Art, p.Type == "spotify"); err != nil {
			s.logger.Warn("box preset sync failed after a rename", "slot", slot, "err", err)
		}
		cancel()
	}
	writeJSON(w, http.StatusOK, p)
}
