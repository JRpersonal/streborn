// HTTP handlers for preset CRUD and the URI helpers they share.

package webui

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/oled"
	"github.com/JRpersonal/streborn/internal/presets"
)

// ---- Presets CRUD ----

func (s *Server) handlePresets(w http.ResponseWriter, r *http.Request) {
	if s.presets == nil {
		http.Error(w, "presets store not initialized", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		all := s.presets.All()
		// Rate-limited per client (presets_readlog.go): the per-GET WARN on
		// an empty store flooded the NAND log tail out of a bundle (#882).
		s.notePresetsRead(r.RemoteAddr, len(all))
		writeJSON(w, http.StatusOK, all)
	case http.MethodPut:
		s.handlePresetsBulkPut(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// maxBulkPresetBody bounds PUT /api/presets. Six times the per-slot cap, for
// the same reason it is generous there: a queue preset carries the whole track
// list of a DLNA folder, and a whole-set write may carry six of them.
const maxBulkPresetBody = 6 << 20

// handlePresetsBulkPut writes a WHOLE preset set in one go: PUT /api/presets
// with a JSON array of at most six presets, each on a slot 1-6. Slots the
// payload does not name are left untouched.
//
// It exists because the per-slot duplicate guard is the wrong rule for a
// transfer. A box-to-box copy PUT the source slots one at a time, and the guard
// (#836: refuse rather than silently delete the key a station already sits on)
// looked at the HALF-WRITTEN store. Two speakers holding the same six stations
// in a different order therefore failed mid-transfer with a 409 per swapped
// pair (#882), even though the finished set would have been perfectly legal.
// Here the duplicate rule is applied to the RESULT, so a re-ordering is fine
// while a genuine collision is still refused, and refusing costs the user
// nothing because nothing is written until the whole set is accepted.
func (s *Server) handlePresetsBulkPut(w http.ResponseWriter, r *http.Request) {
	var in []presets.Preset
	if !decodeJSONRequest(w, r, maxBulkPresetBody, &in) {
		return
	}
	if len(in) == 0 {
		http.Error(w, "empty preset set", http.StatusBadRequest)
		return
	}
	if len(in) > 6 {
		http.Error(w, "too many presets, at most 6", http.StatusBadRequest)
		return
	}
	inPayload := make(map[int]bool, len(in))
	for i := range in {
		p := &in[i]
		if p.Slot < 1 || p.Slot > 6 {
			http.Error(w, "invalid slot, must be 1-6", http.StatusBadRequest)
			return
		}
		if inPayload[p.Slot] {
			http.Error(w, "slot "+strconv.Itoa(p.Slot)+" appears twice", http.StatusBadRequest)
			return
		}
		inPayload[p.Slot] = true
		if p.Type == "" {
			p.Type = "radio"
		}
		if p.Type == "spotify" {
			p.URI = normalizeSpotifyURI(p.URI)
		}
		// A Pandora or iHeartRadio key copied from another speaker carries the
		// item itself; it is checked and normalised like a fresh native save.
		if p.Type == presets.TypeNative {
			if err := validNativeItem(p.Native); err != nil {
				s.logger.Warn("bulk preset write refused: a native service preset in the set is not usable",
					"slot", p.Slot, "name", p.Name, "err", err,
					"from", r.RemoteAddr, "ua", r.Header.Get("User-Agent"))
				writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
					"error": "This station can't be kept on a key.", "code": "native-item-invalid", "slot": p.Slot,
				})
				return
			}
			np, _ := presets.NewNativePreset(p.Slot, *p.Native, p.Name)
			*p = np
			continue
		}
		// Same art rule as the per-slot save: the store keeps the station's
		// ORIGIN image URL, never this agent's own art-proxy wrapper (#696).
		p.Art = healSelfArtProxy(p.Art)
		if msg, code := bulkPresetRejection(*p); code != "" {
			s.logger.Warn("bulk preset write refused: a preset in the set is not playable",
				"slot", p.Slot, "name", p.Name, "code", code,
				"from", r.RemoteAddr, "ua", r.Header.Get("User-Agent"))
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error": msg, "code": code, "slot": p.Slot,
			})
			return
		}
	}
	// A station lives on at most ONE key, checked against the FINISHED set:
	// two payload entries carrying the same station collide with each other,
	// and a payload station also collides with a slot the payload does not
	// rewrite. Nothing is ever deleted to make room, so the loss-free promise
	// of #836 holds here too.
	for i := range in {
		for j := i + 1; j < len(in); j++ {
			if !samePresetStation(in[i], in[j]) && !samePresetStation(in[j], in[i]) {
				continue
			}
			s.logger.Info("bulk preset write refused: the set puts one station on two keys",
				"slot", in[i].Slot, "otherSlot", in[j].Slot, "name", in[j].Name,
				"from", r.RemoteAddr, "ua", r.Header.Get("User-Agent"))
			writeJSON(w, http.StatusConflict, map[string]any{
				"code": "already-on-slot", "slot": in[j].Slot, "name": in[j].Name,
			})
			return
		}
	}
	for _, other := range s.presets.All() {
		if inPayload[other.Slot] {
			continue // this slot is being rewritten, so it cannot collide
		}
		for _, p := range in {
			if !samePresetStation(p, other) {
				continue
			}
			s.logger.Info("bulk preset write refused: a station in the set is already on a key the set does not rewrite",
				"slot", p.Slot, "existingSlot", other.Slot, "name", other.Name,
				"from", r.RemoteAddr, "ua", r.Header.Get("User-Agent"))
			writeJSON(w, http.StatusConflict, map[string]any{
				"code": "already-on-slot", "slot": other.Slot, "name": other.Name,
			})
			return
		}
	}
	// One store write for the whole set: six SetSlot calls would rewrite
	// presets.json plus its backup six times on the speaker's NAND for a
	// single user action.
	if err := s.presets.SetSlots(in); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slots := make([]int, 0, len(in))
	for _, p := range in {
		slots = append(slots, p.Slot)
	}
	// Same forensic rule as the per-slot save: every accepted write names its
	// writer, because a silent success path is a hole in a later bundle.
	s.logger.Info("bulk preset write accepted",
		"slots", slots, "count", len(in),
		"from", r.RemoteAddr, "ua", r.Header.Get("User-Agent"))
	// Sync the hardware keys, same mapping as the per-slot save. Best-effort:
	// the store already holds the set, and a failed box push is retried by the
	// caller's own /api/box/sync-presets.
	if s.boxHost != "" {
		for _, p := range in {
			boxCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			if err := s.writeStorePresetToBox(boxCtx, p); err != nil {
				s.logger.Warn("box preset sync failed", "slot", p.Slot, "err", err)
			}
			cancel()
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "written": len(in), "slots": slots,
	})
}

// bulkPresetRejection reports why a preset in a bulk set cannot be stored, as
// the same {error, code} pair the per-slot save answers with, or "" when it is
// fine. It keeps the structural gates that stop a dead preset from being stored
// (#45/#105/#252) and deliberately drops the per-slot save's NETWORK probes and
// Spotify enrichment: a whole-set write must not fan out into a dozen outbound
// requests, and every preset in it was already vetted when it was first saved
// on the source speaker.
func bulkPresetRejection(p presets.Preset) (msg, code string) {
	switch p.Type {
	case "spotify":
		if !playableSpotifyURI(p.URI) {
			return "This Spotify preset has no replayable playlist, album or track.", "spotify-uri-unplayable"
		}
	case "queue":
		for _, it := range p.Items {
			if it.URL != "" {
				return "", ""
			}
		}
		return "This folder preset has no playable tracks.", "queue-empty"
	default:
		if p.StreamURL == "" {
			return "This preset has no playable stream.", "stream-url-missing"
		}
		if !isHTTPURL(p.StreamURL) {
			return "This preset has no playable stream.", "stream-url-invalid"
		}
		// A stream URL pointing back at an agent's own /stream/<n> proxy is a
		// poisoned value (#252) and must never be stored: on this speaker it
		// would dial itself.
		if _, self := selfProxySlot(p.StreamURL); self {
			return "This preset stores a speaker's own proxy address instead of the station.", "stream-url-self-proxy"
		}
	}
	return "", ""
}

// samePresetStation reports whether p and other play the same thing. It mirrors
// the per-slot duplicate rule exactly: the SAVED preset's type decides which
// field is compared, and an empty field never collides (a queue preset carries
// neither, so folders never collide with anything).
func samePresetStation(p, other presets.Preset) bool {
	if p.Type == presets.TypeNative {
		return p.IsNative() && other.IsNative() && presets.SameNativeItem(p.Native, other.Native)
	}
	if p.Type == "spotify" {
		return p.URI != "" && other.URI == p.URI
	}
	return p.StreamURL != "" && other.StreamURL == p.StreamURL
}

func (s *Server) handlePresetSlot(w http.ResponseWriter, r *http.Request) {
	if s.presets == nil {
		http.Error(w, "presets store not initialized", http.StatusServiceUnavailable)
		return
	}
	slotStr := strings.TrimPrefix(r.URL.Path, "/api/presets/")
	slot, err := strconv.Atoi(slotStr)
	if err != nil || slot < 1 || slot > 6 {
		http.Error(w, "invalid slot, must be 1-6", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		p, ok := s.presets.Get(slot)
		if !ok {
			http.Error(w, "preset not set", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, p)
	case http.MethodPatch:
		// Rename only (presets_rename.go). Its own verb on purpose: a rename must
		// not be judged by the station gates below.
		s.handlePresetRename(w, r, slot)
	case http.MethodPut:
		var p presets.Preset
		// A queue preset carries the whole track list of a DLNA folder or an
		// .m3u playlist, so 64 KB is not a safety margin, it is a hard cap on
		// how long a playlist may be: a 341-line list already failed with
		// "request body too large" (#489). The box only ever stores what its
		// own NAND holds and the preset store is written whole, so a MB is
		// still bounded and comfortably fits any realistic playlist.
		if !decodeJSONRequest(w, r, 1<<20, &p) {
			return
		}
		// The rules live in savePresetSlot (presetsave.go), shared with the
		// hold-to-save and "save what plays" paths; this is the wire adapter.
		saved, ref := s.savePresetSlot(r.Context(), presetSaveRequest{
			Slot: slot, Preset: p, Live: r.URL.Query().Get("save") == "live",
			UserAgent: r.Header.Get("User-Agent"), RemoteAddr: r.RemoteAddr,
		})
		if ref != nil {
			writePresetRefusal(w, ref)
			return
		}
		writeJSON(w, http.StatusOK, saved)
	case http.MethodDelete:
		if err := s.presets.RemoveSlot(slot); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Same forensic line as the PUT: a vanished preset must name its deleter.
		s.logger.Info("preset delete accepted",
			"slot", slot, "from", r.RemoteAddr, "ua", r.Header.Get("User-Agent"))
		s.clearHeldRefusal(slot)
		// Drop it from the box-preset snapshot right away and tombstone the slot so
		// a trailing gabbo presetsUpdated does not resurface it as a foreign (UPNP)
		// entry the user then "cannot delete" (reported after an app restart).
		s.forgetBoxPreset(slot)
		if s.boxHost != "" {
			boxCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			// Log the box-side removal outcome instead of dropping it: a silent
			// failure here is exactly how the preset comes back on the next boot.
			if err := boxcli.RemovePreset(boxCtx, s.boxHost, slot); err != nil {
				s.logger.Warn("preset delete: box-side RemovePreset failed", "slot", slot, "err", err)
			}
			cancel()
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handlePresetMove moves the station on one key over to another key:
// POST /api/box/preset-move with {"from":N,"to":M}. Not under /api/presets/:
// that prefix belongs to the per-key handler, which reads "move" as a key
// number and answers 400 (the phone remote called it there until 2026-10-09).
//
// It is the third answer to the duplicate guard in the per-slot save above,
// which refuses a save whose station already sits on another key. That refusal
// stays the default (deleting the other key behind the user's back is what wiped
// preset after preset, #836), but it left the user with no way to say "then put
// it on the new key". Two reporters met the same wall from opposite sides: one
// read the refusal as a bug (discussion #709), the other concluded he could only
// ever keep one Spotify playlist (discussion #925). A move is an explicit act, so
// it is allowed where the silent delete is not.
//
// The store does both halves in ONE write, so an interrupted move can never end
// with the station on neither key.
func (s *Server) handlePresetMove(w http.ResponseWriter, r *http.Request) {
	if s.presets == nil {
		http.Error(w, "presets store not initialized", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		From int `json:"from"`
		To   int `json:"to"`
	}
	if !decodeJSONRequest(w, r, 1024, &req) {
		return
	}
	if req.From < 1 || req.From > 6 || req.To < 1 || req.To > 6 {
		http.Error(w, "invalid slot, must be 1-6", http.StatusBadRequest)
		return
	}
	if req.From == req.To {
		http.Error(w, "the station is already on that key", http.StatusBadRequest)
		return
	}
	moved, ok := s.presets.Get(req.From)
	if !ok {
		http.Error(w, "preset not set", http.StatusNotFound)
		return
	}
	replacedName := ""
	if old, had := s.presets.Get(req.To); had {
		replacedName = old.Name
	}
	moved.Slot = req.To
	if err := s.presets.MoveSlot(req.From, moved); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Same forensic rule as every other accepted write: a key that changed must
	// name who changed it, because a silent success path is a hole in a later
	// bundle (#758).
	s.logger.Info("preset move accepted",
		"fromSlot", req.From, "toSlot", req.To, "name", moved.Name,
		"replaced", replacedName, "type", moved.Type,
		"from", r.RemoteAddr, "ua", r.Header.Get("User-Agent"))
	// The move is the answer to the "already on key N" offer: the station
	// now sits on the key the user wanted, so say so on the display.
	oled.Show(oled.SavedFromApp(req.To))
	s.clearHeldRefusal(req.From)
	s.clearHeldRefusal(req.To)
	// The hardware keys follow: write the new key first, then drop the old one,
	// so the station is never absent from the box's own preset list. The
	// tombstone stops a trailing gabbo presetsUpdated from resurfacing the old
	// key as a foreign entry, same as the DELETE path.
	s.forgetBoxPreset(req.From)
	if s.boxHost != "" {
		boxCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		if err := s.writeStorePresetToBox(boxCtx, moved); err != nil {
			s.logger.Warn("box preset sync failed", "slot", moved.Slot, "err", err)
		}
		if err := boxcli.RemovePreset(boxCtx, s.boxHost, req.From); err != nil {
			s.logger.Warn("preset move: box-side RemovePreset failed", "slot", req.From, "err", err)
		}
		cancel()
	}
	writeJSON(w, http.StatusOK, moved)
}

// normalizeSpotifyURI rewrites an ephemeral autoplay STATION context to the
// real context it wraps: spotify:station:playlist:X -> spotify:playlist:X.
// Station contexts are session-bound; stored in a preset they later recall a
// foreign or expired station. Applied on save (above) and on recall (heals
// presets stored before this fix).
func normalizeSpotifyURI(uri string) string {
	uri = strings.TrimSpace(uri)
	if rest, ok := strings.CutPrefix(uri, "spotify:station:"); ok && rest != "" {
		return "spotify:" + rest
	}
	return uri
}

// playableSpotifyURI reports whether uri is a Spotify context that go-librespot
// can replay on a preset recall. This is a SAVE gate, deliberately permissive:
// /player/play accepts any well-formed spotify: context (playlist, album, track,
// artist, show/podcast, episode, collection/Liked Songs, and user-scoped
// playlists like spotify:user:<id>:playlist:<id>), so we accept any non-empty
// spotify: URI with a real id and reject only what genuinely cannot recall: an
// empty URI or a /spotify/stream / container URL stored as a "URI" (the
// dead-preset cause, #45/#105). go-librespot is the authority on real
// playability; over-narrowing here wrongly blocked podcast/Liked-Songs saves.
func playableSpotifyURI(uri string) bool {
	uri = strings.TrimSpace(uri)
	if !strings.HasPrefix(uri, "spotify:") || looksLikeSpotifyStreamURL(uri) {
		return false
	}
	parts := strings.Split(uri, ":")
	// Require a kind and a non-empty trailing id: rejects "spotify:" and
	// "spotify:playlist:", accepts spotify:playlist:ID, spotify:show:ID,
	// spotify:episode:ID, spotify:collection, spotify:user:<id>:playlist:<id>.
	return len(parts) >= 2 && parts[1] != "" && parts[len(parts)-1] != ""
}

// isHTTPURL reports whether s is a real http(s) URL the stream proxy can fetch.
func isHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// isPlainHTTPURL reports whether s is a plaintext http:// URL (not https). A LAN
// media server reachable this way can be played by the box directly, skipping
// the stream proxy: the proxy exists to give Bose UPnP HTTPS and radio token
// resilience, neither of which a plain-HTTP LAN file needs (#139).
func isPlainHTTPURL(s string) bool {
	return strings.HasPrefix(strings.ToLower(s), "http://")
}

// mimeFromURL guesses an audio MIME from a stream URL's file extension. Used to
// recall a library preset that did not record its codec MIME. Returns "" for an
// unknown or missing extension, in which case the caller leaves the box on its
// audio/mpeg default.
func mimeFromURL(raw string) string {
	u := strings.ToLower(raw)
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	switch {
	case strings.HasSuffix(u, ".flac"):
		return "audio/flac"
	case strings.HasSuffix(u, ".wav"):
		return "audio/wav"
	case strings.HasSuffix(u, ".m4a"), strings.HasSuffix(u, ".mp4"), strings.HasSuffix(u, ".aac"):
		return "audio/mp4"
	case strings.HasSuffix(u, ".ogg"), strings.HasSuffix(u, ".oga"):
		return "audio/ogg"
	case strings.HasSuffix(u, ".aif"), strings.HasSuffix(u, ".aiff"):
		return "audio/aiff"
	case strings.HasSuffix(u, ".mp3"):
		return "audio/mpeg"
	}
	return ""
}

// looksLikeSpotifyStreamURL reports whether a stored stream URL points at a
// Spotify source, so a non-spotify preset carrying it is really a mis-saved
// Spotify preset (#45/#105).
func looksLikeSpotifyStreamURL(s string) bool {
	return strings.Contains(s, "/spotify/stream") || strings.Contains(s, "/playback/container/")
}

// legacySpotifyURI recovers the spotify: context URI from a preset that an older
// version mis-saved as a non-spotify preset whose stream URL encoded a Spotify
// container, e.g. "/playback/container/<base64 spotify:playlist:...>". Returns
// "" when the URL is a normal radio/HTTP stream or carries no recoverable URI.
func legacySpotifyURI(streamURL string) string {
	const marker = "/playback/container/"
	i := strings.Index(streamURL, marker)
	if i < 0 {
		return ""
	}
	enc := streamURL[i+len(marker):]
	if j := strings.IndexAny(enc, "/?#"); j >= 0 {
		enc = enc[:j]
	}
	// The container is encoded with RawURLEncoding (boxurl), a URL-safe alphabet
	// with no '/'; URLEncoding (padded) is accepted too. The Std alphabets are
	// intentionally omitted: they can emit '/', which the cut above would have
	// truncated, so they could never round-trip here anyway.
	for _, d := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding} {
		if b, err := d.DecodeString(enc); err == nil && strings.HasPrefix(string(b), "spotify:") {
			return string(b)
		}
	}
	return ""
}

// healSelfArtProxy rewrites preset art that points back at this agent so the
// store holds the station's ORIGIN image URL, the art analogue of the
// self-proxy stream heal (#252) above. Desktop apps up to v0.9.56 hold-saved
// the playing ORION descriptor's imageUrl, which is the loopback art-proxy
// wrapper stationImageURL builds for the SPEAKER's display; from the user's
// machine that URL is dead, so the app tile fell through to a grey-chevron
// placeholder (#696). Healing here (not only in the app) means a fixed agent
// stores clean art no matter how old the client that saved is.
//
// Per candidate of the pipe-separated chain: an /art?u= wrapper is unwrapped
// back to the image URL it carries whatever its host says, because the
// desktop app persists the wrapper with whichever authority the descriptor
// carried and a LAN-addressed one dies with the next DHCP lease; the
// /icon.png stand-in (STR's own logo, put in place by the agent when a
// station has no art, never chosen by the user) and broken wrappers are
// dropped; every other value passes through untouched. An input with nothing
// to heal comes back byte-identical.
func healSelfArtProxy(art string) string {
	var out []string
	keep := func(c string) {
		for _, have := range out {
			if have == c {
				return
			}
		}
		out = append(out, c)
	}
	for _, c := range strings.Split(art, "|") {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		u, err := url.Parse(c)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			keep(c) // data: URIs and anything unparsable are not ours to judge
			continue
		}
		if u.Path == artProxyPath {
			if enc := u.Query().Get("u"); enc != "" {
				for _, d := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding} {
					if b, err := d.DecodeString(enc); err == nil && isHTTPURL(string(b)) {
						keep(string(b))
						break
					}
				}
			}
			continue // a wrapper never survives as-is, decoded or broken
		}
		// Same authority test as selfProxySlot: the agent's own ports, or a
		// loopback host (the wrapper is written with boxurl.Authority, but be
		// as tolerant here as the stream heal is).
		if u.Port() == "8888" || u.Port() == "17008" ||
			u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1" {
			continue // box-local (above all /icon.png): not the station's art
		}
		keep(c)
	}
	return strings.Join(out, "|")
}
