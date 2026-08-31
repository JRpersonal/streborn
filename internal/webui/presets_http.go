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
		// Phase marker for the "presets reported empty" symptom on #60.
		// A WARN log on every empty GET makes it directly visible in the
		// diagnostic bundle whether the desktop app actually polled the
		// agent and received an empty array (vs the agent never being
		// reached). Non-empty responses stay at Debug to avoid noise.
		if len(all) == 0 {
			s.logger.Warn("preset store phase: GET /api/presets returned empty",
				"remote", r.RemoteAddr)
		} else {
			s.logger.Debug("GET /api/presets", "count", len(all), "remote", r.RemoteAddr)
		}
		writeJSON(w, http.StatusOK, all)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
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
		p.Slot = slot
		if p.Type == "" {
			p.Type = "radio"
		}
		// Art pointing at THIS agent's own art proxy (or its /icon.png
		// stand-in) is the box-display form, not the station's artwork: the
		// proxy URL is built for the SPEAKER (lir.go stationImageURL) and only
		// resolves on the box's loopback. Desktop apps up to v0.9.56 persisted
		// exactly that on a hold-to-save (they copied the playing ORION
		// descriptor's imageUrl), which left every such key's tile on the grey
		// chevron because the URL is dead from the user's machine (#696, six
		// ST10 keys). The store keeps the origin image URL, same rule as the
		// self-proxy stream heal below; the box side re-wraps at write time
		// anyway, so the display loses nothing.
		p.Art = healSelfArtProxy(p.Art)
		// A queue preset (a saved DLNA folder, #queue-preset) has no single
		// StreamURL/URI: it carries an ordered Items list and a Shuffle flag and
		// recalls into the agent play-queue. It skips the radio/spotify URL gates
		// below; require at least one item with a URL instead, mirroring their
		// 422 shape. The dedup loop below is keyed on URI/StreamURL, both empty
		// here, so it is harmless for queue presets and leaves other slots alone.
		if p.Type == "queue" {
			hasItem := false
			for _, it := range p.Items {
				if it.URL != "" {
					hasItem = true
					break
				}
			}
			if !hasItem {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
					"error": "This folder can't be saved as a preset: it has no playable tracks. Open a folder with audio files and try again.",
					"code":  "queue-empty",
				})
				return
			}
			// A whole-library folder can carry thousands of tracks; the store caps
			// the persisted list at presets.MaxQueueItems so one preset cannot fill
			// the box's flash and strand the next OTA (juelo's 2600-track preset,
			// 2026-08-31). Log the trim; SetSlot below does the actual capping.
			if n := len(p.Items); n > presets.MaxQueueItems {
				s.logger.Info("queue preset capped to fit NAND", "slot", slot, "requested", n, "kept", presets.MaxQueueItems)
			}
			if err := s.presets.SetSlot(p); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			// Register the slot on the box so the hardware button is mapped. The
			// physical press is intercepted by RecallSlot (which starts the queue),
			// but the box still needs an entry for the key to fire at all, so point
			// it at this slot's stream proxy URL like every other preset.
			if s.boxHost != "" {
				boxCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				if err := s.writeBoxPreset(boxCtx, slot, p.Name, boxPresetURL(slot, false), p.Art, false); err != nil {
					s.logger.Warn("box preset sync failed", "slot", slot, "err", err)
				}
				cancel()
			}
			writeJSON(w, http.StatusOK, p)
			return
		}
		// Reject (or heal) the saves that produced the dead presets that fail to
		// recall with "Service not available" (#45/#105). A type=spotify preset
		// MUST carry a replayable context URI; a non-spotify preset MUST carry a
		// real http(s) stream URL. A non-spotify preset whose stream URL actually
		// encodes a Spotify container (an older mis-save) is healed into a proper
		// Spotify preset instead of being stored as a dead radio link.
		if p.Type == "spotify" {
			// Unwrap an ephemeral autoplay STATION context before storing: the
			// save path captures the engine's live context, and after autoplay
			// kicked in that is spotify:station:playlist:X - a session-bound
			// radio wrapper that recalls a foreign/expired station later (field
			// 2026-07-26: every recall of such a preset played an unrelated
			// station and skipped through 51 unplayable tracks). The wrapped
			// context is what the user actually chose, so unwrapping is the
			// faithful save.
			p.URI = normalizeSpotifyURI(p.URI)
			if !playableSpotifyURI(p.URI) {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
					"error": "This Spotify selection can't be saved to a preset: it has no replayable playlist, album or track. Open a playlist or album and try again.",
					"code":  "spotify-uri-unplayable",
				})
				return
			}
		} else if p.StreamURL == "" {
			// An EMPTY stream URL used to slip through the invalid-URL gate below
			// and save as a dead radio preset: the box then fetches /stream/<slot>
			// and the proxy 404s, i.e. a button that assigns fine but never plays
			// (#252). A non-spotify, non-queue preset has nothing else playable,
			// except a Spotify URI mis-typed as radio, which is healed instead.
			if playableSpotifyURI(p.URI) {
				p.Type = "spotify"
			} else {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
					"error": "This selection can't be saved as a preset (no playable stream). Pick a radio station or a Spotify playlist and try again.",
					"code":  "stream-url-missing",
				})
				return
			}
		} else if !isHTTPURL(p.StreamURL) {
			if uri := legacySpotifyURI(p.StreamURL); uri != "" {
				p.Type, p.URI, p.StreamURL = "spotify", uri, ""
			} else {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
					"error": "This selection can't be saved as a preset (no playable stream). Pick a radio station or a Spotify playlist and try again.",
					"code":  "stream-url-invalid",
				})
				return
			}
		}
		// A stream URL that points at THIS agent's own /stream/<n> proxy is a
		// poisoned save (#252): an older client stored the box-visible proxy
		// location instead of the station's origin URL, permanently
		// clobbering the preset (recall then trips the SSRF dial guard with
		// AUDIO_ERROR_BAD_URL). Heal it from the referenced slot's stored
		// origin; refuse when there is nothing left to heal from.
		if p.Type != "spotify" && p.StreamURL != "" {
			if ref, self := selfProxySlot(p.StreamURL); self {
				healed := false
				for _, src := range s.presets.All() {
					if src.Slot != ref || src.StreamURL == "" {
						continue
					}
					if _, srcSelf := selfProxySlot(src.StreamURL); srcSelf {
						break // the stored entry is poisoned too: origin lost
					}
					s.logger.Warn("preset save: healed a self-proxy stream URL from the referenced slot's stored origin (#252)",
						"slot", slot, "ref", ref)
					p.StreamURL = src.StreamURL
					if p.Codec == "" {
						p.Codec = src.Codec
					}
					if p.Bitrate == 0 {
						p.Bitrate = src.Bitrate
					}
					if p.Name == "" {
						p.Name = src.Name
					}
					if p.Art == "" {
						p.Art = src.Art
					}
					healed = true
					break
				}
				if !healed {
					writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
						"error": "This save would store the speaker's own proxy address instead of the station. Play the station again (or re-search it) and save then.",
						"code":  "stream-url-self-proxy",
					})
					return
				}
			}
		}
		// The URL survived every structural gate above, so it is a well-formed
		// http(s) link to something. Last question: is that something actually a
		// stream? A field bundle carried three presets pointing at the station's
		// HOME PAGE, which save cleanly and can never play. The probe refuses only
		// on positive evidence of a web page and allows every uncertain answer, so
		// a station that is merely down or speaks legacy ICY still saves.
		if p.Type != "spotify" && p.StreamURL != "" {
			if looksLikeWebPage(r.Context(), p.StreamURL) {
				s.logger.Warn("preset save refused: the URL answers as a web page, not a stream",
					"slot", slot, "name", p.Name, "url", p.StreamURL)
				writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
					"error": "That address is the station's website, not its stream, so the key would never play. Search for the station in ST Reborn and save it from the search result.",
					"code":  "stream-url-is-webpage",
				})
				return
			}
		}
		// Stamp the account a Spotify preset belongs to (go-librespot's current
		// login) so a later recall can switch back to it on a multi-account box
		// (#27). Two cases: (a) no account yet, fill it from the current login;
		// (b) the preset being saved IS the content go-librespot is playing right
		// now, so the live account owns it and must win even over a stale account
		// carried in from an earlier save. Case (b) fixes the report that a preset
		// saved from a second household member's Spotify session kept the first
		// member's account (jensukk) because the old value was never refreshed
		// (ST30, 2026-07-14). A save for a NON-playing preset keeps its stored
		// account, so a bulk rename never clobbers another account's preset.
		// Account + cover are best-effort enrichment: use a fresh background
		// context, not r.Context(), so a client that disconnects right after the
		// PUT (e.g. a raw one-shot request) does not cancel them mid-fetch.
		// savingLiveContext: the preset being saved IS the content go-librespot
		// is playing right now. Compare NORMALIZED contexts: the engine may
		// announce the ephemeral station wrapper for the playlist the save
		// path already unwrapped above.
		savingLiveContext := p.Type == "spotify" && p.URI != "" &&
			s.spotifyContext != nil && normalizeSpotifyURI(s.spotifyContext()) == p.URI
		if p.Type == "spotify" && s.spotifyUser != nil {
			if p.Account == "" || savingLiveContext {
				uctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				if u := s.spotifyUser(uctx); u != "" && u != p.Account {
					if p.Account != "" {
						s.logger.Info("preset save: refreshed Spotify account to the live playing account", "slot", slot, "from", p.Account, "to", u)
					}
					p.Account = u
				}
				cancel()
			}
		}
		// Carry the LIVE shuffle state onto a preset saved from the running
		// playback, so a playlist the user listens to shuffled recalls
		// shuffled. Without this a long-press save always produced a resume
		// preset that replayed the identical track order on every press (live
		// ST30 2026-08-19, slot 4 "Rock"). Upgrade-only: an explicit shuffle
		// carried in by the client (e.g. the box-to-box preset copy) is kept,
		// and a failed read never clears anything.
		if savingLiveContext && !p.Shuffle && s.spotifyShuffle != nil {
			shctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			if s.spotifyShuffle(shctx) {
				p.Shuffle = true
				s.logger.Info("preset save: carried the live shuffle state onto the preset", "slot", slot)
			}
			cancel()
		}
		// Give a Spotify preset a stable tile logo (the playlist image, #24) and a
		// real name (the playlist title), so the box display and the tile show
		// e.g. "Jens Chill" instead of a bare "Spotify". Only fills empties / a
		// placeholder name.
		if p.Type == "spotify" && p.URI != "" && s.spotifyMeta != nil &&
			(p.Art == "" || p.Name == "" || p.Name == "Spotify") {
			cctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			cover, title := s.spotifyMeta(cctx, p.URI)
			if p.Art == "" && cover != "" {
				p.Art = cover
			}
			if (p.Name == "" || p.Name == "Spotify") && title != "" {
				p.Name = title
			}
			cancel()
		}
		// Dedup: a given playlist/station lives on at most ONE preset. Saving it
		// here removes it from any other slot first (matched by Spotify URI, or
		// by stream URL for radio), so the same content cannot occupy two
		// buttons (user request; also avoids two Spotify presets colliding).
		for _, other := range s.presets.All() {
			if other.Slot == slot {
				continue
			}
			dup := (p.Type == "spotify" && p.URI != "" && other.URI == p.URI) ||
				(p.Type != "spotify" && p.StreamURL != "" && other.StreamURL == p.StreamURL)
			if !dup {
				continue
			}
			_ = s.presets.RemoveSlot(other.Slot)
			s.logger.Info("preset dedup: removed duplicate from other slot", "kept", slot, "removed", other.Slot)
			if s.boxHost != "" {
				rmCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = boxcli.RemovePreset(rmCtx, s.boxHost, other.Slot)
				cancel()
			}
		}
		// Every accepted write is logged with WHO wrote it. The #758 bundle
		// showed a slot rewritten six times in twenty minutes (a phone save
		// fighting a desktop self-heal) and could not name a single writer,
		// because a successful PUT left no trace; silent success paths are
		// forensic holes.
		prevName := ""
		for _, old := range s.presets.All() {
			if old.Slot == slot {
				prevName = old.Name
				break
			}
		}
		if err := s.presets.SetSlot(p); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.logger.Info("preset write accepted",
			"slot", slot, "was", prevName, "now", p.Name, "type", p.Type,
			"stream", p.StreamURL, "from", r.RemoteAddr, "ua", r.Header.Get("User-Agent"))
		// Sync to the box so hardware buttons know the correct slot.
		// Bose gets the stream proxy URL, not the real CDN.
		// This way the stream survives token expiry.
		if s.boxHost != "" {
			boxCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			// Spotify presets store the live Ogg stream as the box-side location
			// so the box's own activation on a hardware press attaches cleanly
			// instead of failing on /stream/<slot> (no Spotify source) and
			// flashing "service unavailable" (#22).
			proxyURL := boxPresetURL(slot, p.Type == "spotify")
			if err := s.writeBoxPreset(boxCtx, slot, p.Name, proxyURL, p.Art, p.Type == "spotify"); err != nil {
				s.logger.Warn("box preset sync failed", "slot", slot, "err", err)
			}
			cancel()
		}
		writeJSON(w, http.StatusOK, p)
	case http.MethodDelete:
		if err := s.presets.RemoveSlot(slot); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Same forensic line as the PUT: a vanished preset must name its deleter.
		s.logger.Info("preset delete accepted",
			"slot", slot, "from", r.RemoteAddr, "ua", r.Header.Get("User-Agent"))
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
