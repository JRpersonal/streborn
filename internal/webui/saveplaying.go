package webui

import (
	"encoding/xml"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/JRpersonal/streborn/internal/presets"
)

// Saving what plays from the phone page (#1407).
//
// A hold on a key of the phone page saves what the speaker plays. Spotify and
// the native services (Pandora, iHeartRadio) the page handles itself, and a
// station it started from its own Find tab moments ago it knows best. Everything
// else used to fall to "Nothing suitable is playing", or worse, to the last
// Find-tab station however long ago that was: a station or a library song
// started from the desktop app or a hardware key could not be kept by holding.
//
// The decision here is the desktop app's (savePresetCase, saveCurrentToSlot),
// made where the facts live: the speaker's now-playing, the agent's queue,
// the agent's own record of what it last pushed, and the preset store.
//
//  1. a music-library folder plays as the queue: the folder preset;
//  2. the single library song the agent pushed: the library preset;
//  3. one of the keys' own streams (/stream/<n>, also inside a native
//     station descriptor): a copy of that key;
//  4. otherwise the stream itself, STR's proxy unwrapped, named and pictured
//     from the speaker's now-playing (or from the agent's record of the play).
//
// Every write goes through the per-slot PUT, so the one-station-one-key
// refusal (#836) reaches the page as the same 409 its move offer reads.

// savePlayingUA marks the internal PUT in the "preset write accepted" line.
const savePlayingUA = "STR phone page hold"

// errSavePlayingUnreadable: the speaker's now-playing could not be read.
var errSavePlayingUnreadable = errors.New("the speaker's now-playing could not be read")

// boxNowItem is what the speaker reports it plays.
type boxNowItem struct {
	Source   string
	Location string
	Name     string
	Art      string
}

// parseBoxNowItem pulls source, location, a name and the art out of a
// now_playing document. ok is false for an unreadable document.
func parseBoxNowItem(b []byte) (boxNowItem, bool) {
	var doc struct {
		Source string `xml:"source,attr"`
		Item   struct {
			Source   string `xml:"source,attr"`
			Location string `xml:"location,attr"`
			ItemName string `xml:"itemName"`
		} `xml:"ContentItem"`
		Station string `xml:"stationName"`
		Track   string `xml:"track"`
		Art     string `xml:"art"`
	}
	if err := xml.Unmarshal(b, &doc); err != nil {
		return boxNowItem{}, false
	}
	src := strings.TrimSpace(doc.Source)
	if src == "" {
		src = strings.TrimSpace(doc.Item.Source)
	}
	name := strings.TrimSpace(doc.Item.ItemName)
	if name == "" {
		name = strings.TrimSpace(doc.Station)
	}
	if name == "" {
		name = strings.TrimSpace(doc.Track)
	}
	return boxNowItem{
		Source:   strings.ToUpper(src),
		Location: strings.TrimSpace(doc.Item.Location),
		Name:     name,
		Art:      strings.TrimSpace(doc.Art),
	}, true
}

// savePlayingRefusal is a save that has nothing to store, with the code the
// page branches on.
type savePlayingRefusal struct {
	Code    string
	Message string
}

func (e *savePlayingRefusal) Error() string { return e.Code + ": " + e.Message }

// SavePlaying stores what the speaker plays right now on slot, for a hold on
// the phone page. r is the page's request, for the log and the display notice.
func (s *Server) SavePlaying(r *http.Request, slot int) (HoldSaved, error) {
	ctx := r.Context()
	doc, err := s.boxNowPlayingDoc(ctx)
	if err != nil {
		return HoldSaved{}, errSavePlayingUnreadable
	}
	now, ok := parseBoxNowItem(doc)
	if !ok {
		return HoldSaved{}, errSavePlayingUnreadable
	}
	loc := now.Location
	if loc == "" {
		return HoldSaved{}, &savePlayingRefusal{Code: "nothing-playing",
			Message: "The speaker is not playing anything that can be kept on a key."}
	}
	// The page decides these itself, with rules this path must not bend.
	if now.Source == "SPOTIFY" || looksLikeSpotifyStreamURL(loc) {
		return HoldSaved{}, &savePlayingRefusal{Code: "spotify-playing",
			Message: "Spotify plays, and its context is saved by the page's own Spotify path."}
	}
	if _, native := presets.NativeServiceLabel(presets.NormalizeNativeSource(now.Source)); native {
		return HoldSaved{}, &savePlayingRefusal{Code: "native-playing",
			Message: "A native service plays; it is saved as type native."}
	}
	ua := r.Header.Get("User-Agent")
	if ua == "" {
		ua = savePlayingUA
	}
	remote := r.RemoteAddr

	upnp := now.Source == "UPNP"
	// 1. A folder: the speaker only names the current track, the queue knows
	// the folder (#1030).
	if upnp {
		if q, ok := s.LiveQueuePreset(slot); ok {
			prev, had := s.presets.Get(slot)
			if had && prev.Type == "queue" && samePresetItems(prev, q) && prev.Name == q.Name && prev.Source == q.Source {
				s.NoteQueueSavedSlot(slot)
				return HoldSaved{Slot: slot, Kind: "queue", Name: prev.Name, Unchanged: true}, nil
			}
			if err := s.storeQueuePreset(ctx, q); err != nil {
				return HoldSaved{}, err
			}
			s.NoteQueueSavedSlot(slot)
			s.noticeAppSave(ua, prev, had, q)
			s.clearHeldRefusal(slot)
			s.logger.Info("queue preset save: stored the playing folder on a key",
				"slot", q.Slot, "name", q.Name, "source", q.Source, "tracks", len(q.Items),
				"shuffle", q.Shuffle, "from", remote, "ua", ua)
			return HoldSaved{Slot: slot, Kind: "queue", Name: q.Name}, nil
		}
		// 2. The single library song the agent pushed, with its server (#1065).
		if p, ok := s.liveLibraryFilePreset(slot, loc); ok {
			return s.saveThroughPut(ctx, p, "library", false, ua, remote)
		}
	}

	// 3. A key's own stream: copy that key, the speaker's name and art for it
	// are only the proxy's.
	ref := 0
	if n, self := selfProxySlot(loc); self {
		ref = n
	}
	st, isStation := DecodeNativeStation(loc)
	if isStation && st.ProxySlot > 0 {
		ref = st.ProxySlot
	}
	if ref > 0 {
		src, have := s.presets.Get(ref)
		if !have {
			return HoldSaved{}, &savePlayingRefusal{Code: "source-preset-empty",
				Message: "The key that is playing holds nothing to copy."}
		}
		if ref == slot {
			return HoldSaved{Slot: slot, Kind: "copy", Name: src.Name, Unchanged: true}, nil
		}
		src.Slot = slot
		return s.saveThroughPut(ctx, src, "copy", false, ua, remote)
	}

	// 4. The stream itself.
	stream, name, art := loc, now.Name, now.Art
	if isStation {
		stream = st.OriginStreamURL
		if stream == "" {
			stream = st.StreamURL
		}
		if st.Name != "" {
			name = st.Name
		}
		if st.Art != "" {
			art = st.Art
		}
	}
	lp := s.play.current()
	recorded := lp != nil && !lp.fromBox && lastPlayMatches(lp.boxURL, loc, st.StreamURL, stream)
	if recorded {
		// The agent's own record of the play knows the name, the whole logo
		// chain and the station facts the app or the page started it with.
		// The speaker's descriptor holds one logo guess and no homepage or
		// codec, which saved a directory's site icon in place of the
		// station's logo (#1467).
		if t := strings.TrimSpace(lp.title); t != "" {
			name = t
		}
		if lp.art != "" {
			art = lp.art
		}
	}
	stream = unwrapRawStreamProxy(stream)
	if !isHTTPURL(stream) || isOwnProxyURL(stream) {
		return HoldSaved{}, &savePlayingRefusal{Code: "nothing-saveable",
			Message: "What the speaker plays has no stream that can be kept on a key."}
	}
	p := presets.Preset{Slot: slot, Type: "radio", StreamURL: stream, Name: name, Art: art}
	if recorded {
		p.Homepage, p.Codec, p.Bitrate = lp.homepage, lp.codec, lp.bitrate
		// No bitrate came with the play: the one the stream proxy measured
		// for the stream playing right now, as the app's own save used.
		if p.Bitrate == 0 && s.streamProxy != nil {
			p.Bitrate = s.streamProxy.CurrentBitrate()
		}
	}
	// A song file on a registered media server is a library key: the server's
	// name makes a press hand the file to the speaker directly instead of
	// through the radio proxy, which cannot play a finite file (#139).
	if server := s.libraryServerNameFor(stream); server != "" && mimeFromURL(stream) != "" {
		p.Source = server
	}
	if p.Name == "" {
		p.Name = savePlayingFallbackName(stream)
	}
	return s.saveThroughPut(ctx, p, "stream", false, ua, remote)
}

// lastPlayMatches reports whether the agent's record of the play (recorded,
// the box-facing URL handlePlay pushed) is what the speaker plays. The speaker
// reports it as loc in one of three forms: the URL itself (a UPnP push or a
// direct library play), or a native station descriptor whose stream is that
// URL (stationStream) and whose origin (origin) is the stream inside it. The
// raw proxy is unwrapped on both sides, so a record and a report that wrap the
// same station differently still match.
func lastPlayMatches(recorded, loc, stationStream, origin string) bool {
	if recorded == "" {
		return false
	}
	if recorded == loc || (stationStream != "" && recorded == stationStream) {
		return true
	}
	inner := unwrapRawStreamProxy(recorded)
	for _, c := range []string{loc, stationStream, origin} {
		if c != "" && unwrapRawStreamProxy(c) == inner {
			return true
		}
	}
	return false
}

// isOwnProxyURL reports whether stream is still one of this agent's own
// proxies after unwrapping (an undecodable raw form, say): never a station.
func isOwnProxyURL(stream string) bool {
	u, err := url.Parse(stream)
	if err != nil || !sameAgentAuthority(stream) {
		return false
	}
	return strings.HasPrefix(u.Path, "/stream/") || strings.HasPrefix(u.Path, "/spotify/")
}

// savePlayingFallbackName names a stream the speaker reports no name for: a
// file by its file name, a station by its host.
func savePlayingFallbackName(stream string) string {
	if mimeFromURL(stream) != "" {
		return libraryFileNameFromURL(stream)
	}
	if u, err := url.Parse(stream); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return "Station"
}

// handleSavePlaying answers POST /api/box/save-playing {"slot":N}: store what
// the speaker plays on key N (the phone page's hold, #1407). 200 carries
// {slot, kind, name, unchanged}; a refusal of the preset rules comes back with
// the PUT's own status and body (409 already-on-slot with the other key), and
// "nothing to save" as 422 with a code.
func (s *Server) handleSavePlaying(w http.ResponseWriter, r *http.Request) {
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
	saved, err := s.SavePlaying(r, req.Slot)
	var refusal *savePlayingRefusal
	var putRefusal *HoldSaveError
	switch {
	case err == nil:
		s.logger.Info("save what plays: stored on a key",
			"slot", saved.Slot, "kind", saved.Kind, "name", saved.Name, "unchanged", saved.Unchanged,
			"from", r.RemoteAddr)
		writeJSON(w, http.StatusOK, map[string]any{
			"slot": saved.Slot, "kind": saved.Kind, "name": saved.Name, "unchanged": saved.Unchanged,
		})
	case errors.Is(err, errSavePlayingUnreadable):
		s.logger.Warn("save what plays refused", "slot", req.Slot, "code", "nowplaying-unreadable",
			"from", r.RemoteAddr)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": "The speaker could not be asked what it is playing. Try again in a moment.",
			"code":  "nowplaying-unreadable",
		})
	case errors.As(err, &refusal):
		s.logger.Info("save what plays refused", "slot", req.Slot, "code", refusal.Code,
			"reason", refusal.Message, "from", r.RemoteAddr)
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": refusal.Message, "code": refusal.Code,
		})
	case errors.As(err, &putRefusal):
		// The PUT already logged its own refusal line.
		body := map[string]any{"code": putRefusal.Code}
		if putRefusal.Message != "" {
			body["error"] = putRefusal.Message
		}
		if putRefusal.OtherSlot > 0 {
			body["slot"] = putRefusal.OtherSlot
			body["name"] = putRefusal.OtherName
		}
		status := putRefusal.Status
		if status < 400 {
			status = http.StatusUnprocessableEntity
		}
		writeJSON(w, status, body)
	default:
		s.logger.Warn("save what plays failed", "slot", req.Slot, "err", err, "from", r.RemoteAddr)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
