package webui

import (
	"net/http"
	"strings"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/presets"
)

// GET /api/nowplaying: what the speaker plays, as JSON, for integrations.
//
// /api/status hands out the speaker's own now_playing XML, and it has to stay
// that way: the desktop app and the phone remote regex-parse it, and an older
// app must keep working against a newer agent. Home automation asked for JSON
// instead of a second XML parser (integration feedback, 2026-11). This is that
// JSON, built from the SAME answer /api/status serves: the same micro-cache,
// the same single bounded read of the speaker when the cache is cold, the same
// stale fallback. No extra read of the speaker, no timer, no polling of its
// own; a client that polls both endpoints still costs the speaker one read per
// statusCacheTTL.

// nowPlayingJSON is the /api/nowplaying answer. Every field the speaker did
// not report, or STR cannot tell, is left out.
type nowPlayingJSON struct {
	// Source is the speaker's source ("UPNP", "LOCAL_INTERNET_RADIO",
	// "SPOTIFY", "BLUETOOTH", "AUX", "STANDBY", ...).
	Source string `json:"source,omitempty"`
	// PlayStatus is PLAY_STATE, PAUSE_STATE, STOP_STATE or BUFFERING_STATE.
	PlayStatus string `json:"playStatus,omitempty"`
	// Title is the track, or the item's name when the speaker reports no
	// track (a radio station without a live title).
	Title    string `json:"title,omitempty"`
	Artist   string `json:"artist,omitempty"`
	Album    string `json:"album,omitempty"`
	Station  string `json:"station,omitempty"`
	ItemName string `json:"itemName,omitempty"`
	Art      string `json:"art,omitempty"`
	Location string `json:"location,omitempty"`
	// PositionSec and DurationSec are the speaker's <time> element, when it
	// carries one (a track with a known length).
	PositionSec *int `json:"positionSec,omitempty"`
	DurationSec *int `json:"durationSec,omitempty"`
	// PresetSlot is the STR key (1-12) that holds what plays, when STR can
	// tell; the same answer that lights a key in the apps.
	PresetSlot int `json:"presetSlot,omitempty"`
	// Muted is the speaker's mute flag from its own bus; absent while the
	// agent has not heard it yet.
	Muted *bool `json:"muted,omitempty"`
	// Stale and AgeSec: the speaker did not answer and this is the last
	// answer it gave, AgeSec seconds old. Stale is set past statusStaleAfter,
	// the same point /api/status sends X-STR-Status-Stale.
	Stale  bool `json:"stale,omitempty"`
	AgeSec *int `json:"ageSec,omitempty"`
}

// handleNowPlaying answers GET /api/nowplaying. Same reachability as
// /api/status (served on the agent's LAN port, no extra gate).
func (s *Server) handleNowPlaying(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	if s.boxHost == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "box host not configured"})
		return
	}
	a, err := s.readStatusAnswer(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "the speaker did not answer", "detail": err.Error(),
		})
		return
	}
	if a.code != http.StatusOK {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": "the speaker refused the now-playing read", "speakerStatus": a.code,
		})
		return
	}
	out := nowPlayingFrom(boxapi.ParseNowPlaying(a.body))
	out.PresetSlot = s.playingPresetSlot(out.PlayStatus, out.Location)
	if muted, known := s.boxMuted(); known {
		out.Muted = &muted
	}
	if a.fallback {
		age := a.ageSec
		out.AgeSec = &age
		out.Stale = a.stale
	}
	writeJSON(w, http.StatusOK, out)
}

// nowPlayingFrom projects a parsed now_playing onto the JSON answer.
func nowPlayingFrom(np *boxapi.NowPlaying) nowPlayingJSON {
	out := nowPlayingJSON{
		Source:     strings.TrimSpace(np.Source),
		PlayStatus: np.Status(),
		Artist:     strings.TrimSpace(np.Artist),
		Album:      strings.TrimSpace(np.Album),
		Station:    strings.TrimSpace(np.StationName),
		ItemName:   strings.TrimSpace(np.ContentItem.ItemName),
		Art:        strings.TrimSpace(np.Art),
		Location:   strings.TrimSpace(np.ContentItem.Location),
	}
	out.Title = strings.TrimSpace(np.Track)
	if out.Title == "" {
		out.Title = out.ItemName
	}
	if out.Art == "" {
		out.Art = strings.TrimSpace(np.ContentItem.ContainerArt)
	}
	if np.HasTime {
		pos, total := int(np.Position.Seconds()), int(np.Total.Seconds())
		out.PositionSec = &pos
		if total > 0 {
			out.DurationSec = &total
		}
	}
	return out
}

// playingPresetSlot names the STR key that holds what plays, or 0. Only a
// speaker that plays, buffers or is paused has a key playing: a stopped one
// still reports the station's location, and the apps leave its key dark.
//
// The rules are the apps' (markPlaying in the phone remote): STR's per-key
// stream proxy, a key holding exactly this URL or this Pandora/iHeartRadio
// station, a native station whose descriptor names a key's stream, the
// Spotify key the playing-key header names, and a folder key the running
// queue was started from. Nothing here reads the speaker.
func (s *Server) playingPresetSlot(playStatus, loc string) int {
	switch playStatus {
	case "PLAY_STATE", "BUFFERING_STATE", "PAUSE_STATE":
	default:
		return 0
	}
	if s.presets == nil {
		return 0
	}
	list := s.presets.All()
	if n := presetSlotForLocation(loc, list); n > 0 {
		return n
	}
	if n := s.playingSpotifyKey(loc); n > 0 {
		return n
	}
	return s.queuePlayingSlot()
}

// presetSlotForLocation matches a now-playing location against the keys, or
// returns 0. Pure: the location and the list are all it looks at.
func presetSlotForLocation(loc string, list []presets.Preset) int {
	if loc == "" {
		return 0
	}
	held := func(n int) int {
		for _, p := range list {
			if p.Slot == n {
				return n
			}
		}
		return 0
	}
	byStream := func(u string) int {
		if u == "" {
			return 0
		}
		for _, p := range list {
			if p.StreamURL != "" && p.StreamURL == u {
				return p.Slot
			}
		}
		return 0
	}
	if n, ok := selfProxySlot(loc); ok {
		return held(n)
	}
	for _, p := range list {
		if p.IsNative() && p.Native.Location == loc {
			return p.Slot
		}
	}
	if n := byStream(loc); n > 0 {
		return n
	}
	if st, ok := DecodeNativeStation(loc); ok {
		if st.ProxySlot > 0 {
			return held(st.ProxySlot)
		}
		return byStream(st.OriginStreamURL)
	}
	return 0
}

// queuePlayingSlot is the folder key the running queue plays from, or 0: the
// key its card names while that key still holds the tracks, else the key it
// was saved on or one that holds exactly its tracks. The same answer
// GET /api/queue gives as card_slot / slot.
func (s *Server) queuePlayingSlot() int {
	if s.queue == nil || !s.queue.isActive() {
		return 0
	}
	s.recentMu.Lock()
	card := s.recentQueueCard.key
	s.recentMu.Unlock()
	if n, ok := queueSlotCardNum(card); ok && s.cardSlotHolds(n) {
		return n
	}
	return s.queueKeySlot()
}
