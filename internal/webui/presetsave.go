package webui

// The per-slot preset save behind PUT /api/presets/<n>, as a service.
//
// The HTTP handler (presets_http.go) decodes the body and renders the answer;
// the hold-to-save path (holdsave.go) and the phone page's "save what plays"
// (saveplaying.go) call savePresetSlot directly, so a held key obeys every
// rule an app save does without a second copy of them and without driving
// the handler through a fake ResponseWriter.

import (
	"context"
	"net/http"
	"time"

	"github.com/JRpersonal/streborn/anonymise"
	"github.com/JRpersonal/streborn/internal/presets"
)

// presetSaveRequest is what the per-slot PUT knows about its caller beyond
// the preset itself.
type presetSaveRequest struct {
	Slot int
	// Preset is the decoded body. Its Slot is overwritten with Slot.
	Preset presets.Preset
	// Live is ?save=live: a real "save what plays" gesture, the only PUT
	// that may move a Spotify key onto the live account.
	Live bool
	// UserAgent and RemoteAddr are what the PUT logs and decides its display
	// notice on (holdSaveUA shows no app notice).
	UserAgent  string
	RemoteAddr string
}

// presetRefusal is why a preset save did not store anything: the status and
// the exact body the handler renders, plus the typed fields the in-process
// callers map onto HoldSaveError. The bodies are pinned by the desktop app's
// regexes on already-on-slot (groups.js, main.js, presetmove.js), the phone
// remote's presetConflict and handleSavePlaying, which re-emits them.
type presetRefusal struct {
	Status int
	// Code is the "code" field of a JSON refusal; empty for a plain-text
	// body (the 500s and the 503/400 preconditions).
	Code string
	// Error is the "error" field, or the whole body of a plain-text refusal.
	Error string
	// Slot and Name are the other key for already-on-slot; zero otherwise.
	Slot int
	Name string
	// JSON is the exact JSON body to render; nil means plain text (http.Error
	// with Error).
	JSON any
}

func textPresetRefusal(status int, msg string) *presetRefusal {
	return &presetRefusal{Status: status, Error: msg}
}

// jsonPresetRefusal lifts code, error, slot and name out of the body so the
// callers can branch without parsing; the body goes out as is.
func jsonPresetRefusal(status int, body map[string]any) *presetRefusal {
	ref := &presetRefusal{Status: status, JSON: body}
	if c, ok := body["code"].(string); ok {
		ref.Code = c
	}
	if e, ok := body["error"].(string); ok {
		ref.Error = e
	}
	if n, ok := body["slot"].(int); ok {
		ref.Slot = n
	}
	if n, ok := body["name"].(string); ok {
		ref.Name = n
	}
	return ref
}

// writePresetRefusal renders a refusal onto the wire: http.Error for the
// text ones, writeJSON for the JSON ones, byte for byte what the handler
// wrote before the service existed.
func writePresetRefusal(w http.ResponseWriter, ref *presetRefusal) {
	if ref.JSON == nil {
		http.Error(w, ref.Error, ref.Status)
		return
	}
	writeJSON(w, ref.Status, ref.JSON)
}

// presetUnavailable is the pair of answers every per-slot request gets
// before its verb is looked at: no store, or a slot outside 1-12.
func (s *Server) presetUnavailable(slot int) *presetRefusal {
	if s.presets == nil {
		return textPresetRefusal(http.StatusServiceUnavailable, "presets store not initialized")
	}
	if !presets.ValidSlot(slot) {
		return textPresetRefusal(http.StatusBadRequest, invalidSlotMsg)
	}
	return nil
}

// savePresetSlot stores req.Preset on req.Slot under every preset rule and
// returns the stored preset (the 200 body) or the refusal.
func (s *Server) savePresetSlot(ctx context.Context, req presetSaveRequest) (presets.Preset, *presetRefusal) {
	if ref := s.presetUnavailable(req.Slot); ref != nil {
		return presets.Preset{}, ref
	}
	slot, p := req.Slot, req.Preset
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
	// A library key saved from a listing this agent served (the phone page)
	// gets the track's object id, so a server that renumbers its file URLs
	// does not kill it (#919, libraryreresolve.go).
	s.fillLibraryRefs(&p)
	// A Pandora or iHeartRadio station (type native) is the speaker's own
	// item and has no stream to gate; see nativeservice.go.
	if p.Type == presets.TypeNative {
		return s.saveNativePreset(ctx, req, p)
	}
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
			return presets.Preset{}, jsonPresetRefusal(http.StatusUnprocessableEntity, map[string]any{
				"error": "This folder can't be saved as a preset: it has no playable tracks. Open a folder with audio files and try again.",
				"code":  "queue-empty",
			})
		}
		// A whole-library folder can carry thousands of tracks; the store caps
		// the persisted list at presets.MaxQueueItems so one preset cannot fill
		// the box's flash and strand the next OTA (juelo's 2600-track preset,
		// 2026-08-31). Log the trim; SetSlot below does the actual capping.
		if n := len(p.Items); n > presets.MaxQueueItems {
			s.logger.Info("queue preset capped to fit NAND", "slot", slot, "requested", n, "kept", presets.MaxQueueItems)
		}
		prevQ, hadQ := s.presets.Get(slot)
		if err := s.storeQueuePreset(ctx, p); err != nil {
			return presets.Preset{}, textPresetRefusal(http.StatusInternalServerError, err.Error())
		}
		s.noticeAppSave(req.UserAgent, prevQ, hadQ, p)
		s.clearHeldRefusal(slot)
		return p, nil
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
			return presets.Preset{}, jsonPresetRefusal(http.StatusUnprocessableEntity, map[string]any{
				"error": "This Spotify selection can't be saved to a preset: it has no replayable playlist, album or track. Open a playlist or album and try again.",
				"code":  "spotify-uri-unplayable",
			})
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
			return presets.Preset{}, jsonPresetRefusal(http.StatusUnprocessableEntity, map[string]any{
				"error": "This selection can't be saved as a preset (no playable stream). Pick a radio station or a Spotify playlist and try again.",
				"code":  "stream-url-missing",
			})
		}
	} else if !isHTTPURL(p.StreamURL) {
		if uri := legacySpotifyURI(p.StreamURL); uri != "" {
			p.Type, p.URI, p.StreamURL = "spotify", uri, ""
		} else {
			return presets.Preset{}, jsonPresetRefusal(http.StatusUnprocessableEntity, map[string]any{
				"error": "This selection can't be saved as a preset (no playable stream). Pick a radio station or a Spotify playlist and try again.",
				"code":  "stream-url-invalid",
			})
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
				return presets.Preset{}, jsonPresetRefusal(http.StatusUnprocessableEntity, map[string]any{
					"error": "This save would store the speaker's own proxy address instead of the station. Play the station again (or re-search it) and save then.",
					"code":  "stream-url-self-proxy",
				})
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
		if looksLikeWebPage(ctx, p.StreamURL) {
			s.logger.Warn("preset save refused: the URL answers as a web page, not a stream",
				"slot", slot, "name", p.Name, "url", p.StreamURL)
			return presets.Preset{}, jsonPresetRefusal(http.StatusUnprocessableEntity, map[string]any{
				"error": "That address is the station's website, not its stream, so the key would never play. Search for the station in ST Reborn and save it from the search result.",
				"code":  "stream-url-is-webpage",
			})
		}
	}
	// A radio station gets the desktop app's whole logo chain, whichever
	// client saved it: the phone page sends the one radio-browser favicon it
	// has, often http or empty, and its keys came out without the logo the
	// app's save of the same station shows (#1467). A media-server file
	// (Source set) keeps the cover it came with.
	if p.Type == "radio" && p.Source == "" && p.StreamURL != "" {
		p.Art = completeStationArt(p.Art, p.Homepage, p.StreamURL)
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
	// Case (b) only applies to a real save gesture (?save=live, sent by the
	// app's and the phone remote's "save what plays" paths). Every other
	// PUT (rename, move, backup restore, copy, stash) re-writes a preset it
	// read from the store, and while that key plays it plays under whatever
	// account the speaker fell back to: a recall whose account switch the
	// other person's still-connected Spotify app overrode plays a public
	// playlist under the wrong account without a sound of difference. Case
	// (b) then quietly rewrote the key to that account, so a household with
	// two accounts ended up with every key on one of them (ST30, 2026-10-09).
	// Account + cover are best-effort enrichment: use a fresh background
	// context, not the request's, so a client that disconnects right after
	// the PUT (e.g. a raw one-shot request) does not cancel them mid-fetch.
	// savingLiveContext: the preset being saved IS the content go-librespot
	// is playing right now. Compare NORMALIZED contexts: the engine may
	// announce the ephemeral station wrapper for the playlist the save
	// path already unwrapped above.
	savingLiveContext := p.Type == "spotify" && p.URI != "" &&
		s.spotifyContext != nil && normalizeSpotifyURI(s.spotifyContext()) == p.URI
	liveSave := req.Live
	if p.Type == "spotify" && s.spotifyUser != nil {
		if p.Account == "" || (savingLiveContext && liveSave) {
			uctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			if u := s.spotifyUser(uctx); u != "" && u != p.Account {
				if p.Account != "" {
					s.logger.Info("preset save: refreshed Spotify account to the live playing account", "slot", slot, "from", anonymise.MaskAccount(p.Account), "to", anonymise.MaskAccount(u))
				}
				p.Account = u
			}
			cancel()
		}
	}
	if p.Type == "spotify" {
		// Which account a key ends up on was invisible in a diagnostic, so
		// "this key was saved from the other account" could not be checked
		// once the save had left the log window (ST30, 2026-10-09). Logged before
		// the preset rules run, so a refusal line can still follow it.
		s.logger.Info("preset save: Spotify key about to be stored (the preset rules may still refuse it)", "slot", slot, "account", anonymise.MaskAccount(p.Account),
			"liveSave", liveSave, "playingNow", savingLiveContext)
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
	// Same for repeat, and for the same reason: the engine keeps repeat per
	// session, so a playlist the user had looping went back to stopping at
	// the end on the next press and he had to set it again in the Spotify
	// app, every evening (Patrick, 2026-09-09 and again on the 15th).
	// Upgrade-only in the same way: an explicit repeat carried in by the
	// client survives, and a failed read never clears one.
	if savingLiveContext && !p.Repeat && s.spotifyRepeat != nil {
		rctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if s.spotifyRepeat(rctx) {
			p.Repeat = true
			s.logger.Info("preset save: carried the live repeat state onto the preset", "slot", slot)
		}
		cancel()
	}
	// A single library song saved without its length (an app that does not
	// send it) gets the one learned when the file played here, so the key
	// ends on time when it is pressed (#978).
	if s.fillLibraryPresetLength(&p) {
		s.logger.Info("preset save: library track stored with the length learned from its play",
			"slot", slot, "trackSec", p.DurationSec)
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
	// A given playlist/station lives on at most ONE key. If the station being
	// saved is already on ANOTHER slot, REFUSE rather than deleting that slot.
	// The old behaviour silently removed the other slot, and that is what wiped
	// a user's keys one after another (#836): a hold-save that captured an
	// already-saved station (the box's now-playing lagged the native switch,
	// so the app saved the previously playing station) deleted the key that
	// station was on. Refusing is loss-free; the frontend turns this 409 into a
	// "Sender liegt schon auf Taste N" note. A same-slot re-save (metadata
	// self-heal, or overwriting the key with itself) is skipped, so it is never
	// blocked.
	for _, other := range s.presets.All() {
		if other.Slot == slot {
			continue
		}
		dup := (p.Type == "spotify" && p.URI != "" && other.URI == p.URI) ||
			(p.Type != "spotify" && p.StreamURL != "" && other.StreamURL == p.StreamURL)
		if !dup {
			continue
		}
		s.logger.Info("preset save refused: this station is already on another slot",
			"slot", slot, "existingSlot", other.Slot, "name", other.Name,
			"from", req.RemoteAddr, "ua", req.UserAgent)
		s.noticeAppRefused(req.UserAgent, other.Slot)
		return presets.Preset{}, jsonPresetRefusal(http.StatusConflict, map[string]any{
			"code": "already-on-slot",
			"slot": other.Slot,
			"name": other.Name,
		})
	}
	// Every accepted write is logged with WHO wrote it. The #758 bundle
	// showed a slot rewritten six times in twenty minutes (a phone save
	// fighting a desktop self-heal) and could not name a single writer,
	// because a successful PUT left no trace; silent success paths are
	// forensic holes.
	prevName := ""
	prev, had := s.presets.Get(slot)
	if had {
		prevName = prev.Name
	}
	if err := s.presets.SetSlot(p); err != nil {
		return presets.Preset{}, textPresetRefusal(http.StatusInternalServerError, err.Error())
	}
	s.noticeAppSave(req.UserAgent, prev, had, p)
	s.clearHeldRefusal(slot)
	s.logger.Info("preset write accepted",
		"slot", slot, "was", prevName, "now", p.Name, "type", p.Type,
		"stream", p.StreamURL, "from", req.RemoteAddr, "ua", req.UserAgent)
	// Sync to the box so hardware buttons know the correct slot.
	// Bose gets the stream proxy URL, not the real CDN.
	// This way the stream survives token expiry.
	if s.boxHost != "" {
		boxCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
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
	return p, nil
}
