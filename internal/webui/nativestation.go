package webui

// Reading a native LOCAL_INTERNET_RADIO location back.
//
// OrionStationLocation (lir.go) is how STR writes a station for the speaker:
// base64 JSON in a "/station?data=" location, with the stream and the artwork
// pointing at this agent's own proxies because the speaker fetches both itself.
// When the speaker hands such a location BACK (its own hold-to-store gesture
// PUTs the playing item to marge, its /presets list reports the slot), the
// agent has to recover what the station actually is: the origin stream and
// the origin image, not the loopback wrappers. This is the one place that
// unwinds them, so the marge keeper and the reconcile agree on the answer.

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

// NativeStation is what a native radio location describes.
type NativeStation struct {
	// Name is the station name carried in the descriptor.
	Name string
	// StreamURL is the stream exactly as the descriptor names it. It may be
	// one of this agent's proxies; see ProxySlot and OriginStreamURL.
	StreamURL string
	// OriginStreamURL is the station's own stream URL when the descriptor
	// makes it recoverable: the ad-hoc raw proxy unwrapped, or a plain origin
	// URL passed through. Empty when the stream is a per-slot proxy (the
	// origin then lives in that slot of the preset store) or unreadable.
	OriginStreamURL string
	// ProxySlot is the preset slot when StreamURL is this agent's own
	// /stream/<slot> proxy, 0 otherwise.
	ProxySlot int
	// Art is the origin artwork URL with STR's own wrappers unwound: the art
	// proxy decoded, the /icon.png stand-in dropped to "".
	Art string
}

// DecodeNativeStation unpacks a "/station?data=<base64 JSON>" location (bare
// or behind the ORION adapter path) into the station it describes. ok is false
// for any other location shape or an unreadable payload.
func DecodeNativeStation(loc string) (NativeStation, bool) {
	const p = "/station?data="
	i := strings.Index(loc, p)
	if i < 0 {
		return NativeStation{}, false
	}
	raw := strings.TrimSpace(loc[i+len(p):])
	// STR writes the unpadded URL-safe alphabet; older builds and the firmware
	// itself have used others, so accept them all, like handleOrionStation.
	var payload []byte
	for _, dec := range []*base64.Encoding{
		base64.RawURLEncoding, base64.URLEncoding, base64.StdEncoding, base64.RawStdEncoding,
	} {
		if b, err := dec.DecodeString(raw); err == nil {
			payload = b
			break
		}
	}
	if payload == nil {
		return NativeStation{}, false
	}
	var st struct {
		StreamURL string `json:"streamUrl"`
		Name      string `json:"name"`
		ImageURL  string `json:"imageUrl"`
	}
	if err := json.Unmarshal(payload, &st); err != nil || st.StreamURL == "" {
		return NativeStation{}, false
	}
	out := NativeStation{
		Name:      strings.TrimSpace(st.Name),
		StreamURL: strings.TrimSpace(st.StreamURL),
		Art:       healSelfArtProxy(st.ImageURL),
	}
	if slot, self := selfProxySlot(out.StreamURL); self {
		out.ProxySlot = slot
		return out, true
	}
	// STR's own PER-SLOT SPOTIFY stream is one of its keys too, and it has to
	// be recognised as one here.
	//
	// Without this the URL falls through as if it were a station origin, and a
	// hold-to-store on a playing Spotify preset rewrote that key as a radio
	// preset whose stream URL was the agent's own Spotify proxy: the playlist
	// URI gone, the type wrong, and a "station" that only resolves while that
	// slot happens to be playing Spotify. Two of a reporter's three speakers
	// carried exactly that damage (bundle 2026-09-09, slots 3 and 4 as
	// type=radio pointing at .../spotify/stream-N.ogg), against a third whose
	// slot 4 was still a proper spotify preset with its playlist URI.
	//
	// Resolved as a proxy slot, the branch above keeps the preset the store
	// already holds, which is the correct answer for a key held down while its
	// own content plays.
	if slot := slotFromSpotifyStreamURL(out.StreamURL); slot > 0 && sameAgentAuthority(out.StreamURL) {
		out.ProxySlot = slot
		return out, true
	}
	if origin := unwrapRawStreamProxy(out.StreamURL); isHTTPURL(origin) {
		out.OriginStreamURL = origin
	}
	return out, true
}

// rawStreamProxyPath is the ad-hoc stream proxy route (boxurl.RawStream).
var rawStreamProxyPath = regexp.MustCompile(`^/stream/raw$`)

// unwrapRawStreamProxy unwinds "/stream/raw?u=<base64 URL>" wrappers (an
// ad-hoc app play routes the station through it) back to the origin URL, a few
// levels deep in case of a double wrap. Any other URL comes back unchanged.
func unwrapRawStreamProxy(raw string) string {
	for i := 0; i < 5; i++ {
		u, err := url.Parse(raw)
		if err != nil || !rawStreamProxyPath.MatchString(u.Path) {
			return raw
		}
		enc := u.Query().Get("u")
		if enc == "" {
			return raw
		}
		var inner string
		for _, d := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.StdEncoding} {
			if b, err := d.DecodeString(enc); err == nil {
				inner = string(b)
				break
			}
		}
		if !isHTTPURL(inner) {
			return raw
		}
		raw = inner
	}
	return raw
}

// NativeLocationIsOwnSlotProxy reports whether a native radio location the
// speaker holds in slot describes THAT slot's own stream proxy, the form STR
// writes. A slot the speaker stored itself (its hold-to-store gesture keeps
// the playing item as is) carries whatever the station was playing from
// instead: the ad-hoc raw proxy of an app play, or another key's proxy. The
// reconcile rewrites such a slot once onto its own form. An unreadable
// location answers true, so that nothing is rewritten on a guess.
func NativeLocationIsOwnSlotProxy(loc string, slot int) bool {
	st, ok := DecodeNativeStation(loc)
	if !ok {
		return true
	}
	return st.ProxySlot == slot
}
