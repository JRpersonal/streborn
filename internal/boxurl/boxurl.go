// Package boxurl centralizes the agent-loopback URLs that the box's UPnP
// renderer fetches preset and stream audio from. The agent runs on the box, so
// 127.0.0.1:8888 reaches the agent's own webui / stream proxy. These URLs were
// stamped out by hand at a dozen sites across cmd/agent, internal/webui and the
// frontend; a mismatch between two of them was the v0.7.21 self-proxy
// regression. Every Go producer now builds them here, so the path scheme has a
// single source of truth.
package boxurl

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// Authority is the agent's loopback host:port as seen from the box itself.
const Authority = "127.0.0.1:8888"

// orionStationBase is the absolute Orion "custom station" endpoint. The box
// redirects content.api.bose.io to the agent (via /etc/hosts + the marge TLS
// listener), so a native LOCAL_INTERNET_RADIO preset whose location points here
// is resolved by the agent's own respondOrionStation.
const orionStationBase = "https://content.api.bose.io/core02/svc-bmx-adapter-orion/prod/orion/station?data="

// OrionStation builds the native LOCAL_INTERNET_RADIO preset location for a slot:
// an absolute Orion station URL that embeds the per-slot stream-proxy URL. The
// box self-activates this natively (no login-gated UPNP 1036) and GETs it against
// the agent, which answers with a BmxPlaybackResponse pointing back at the same
// stream proxy. RawURLEncoding keeps the base64 free of +/=/ that URL query
// decoding would mangle. Name/art are left empty: the box shows the preset's own
// itemName and the live ICY title.
func OrionStation(slot int) string {
	payload := fmt.Sprintf(`{"name":"","imageUrl":"","streamUrl":%q}`, StreamSlot(slot))
	return orionStationBase + base64.RawURLEncoding.EncodeToString([]byte(payload))
}

// IsOrionStation reports whether loc is a native Orion station location (built by
// OrionStation). Used by the recall + reconcile to recognise STR's own native
// radio presets and let the box self-activate them instead of the UPnP push.
func IsOrionStation(loc string) bool {
	return strings.Contains(loc, "svc-bmx-adapter-orion")
}

// StreamSlot is the stream-proxy URL for preset slot n (radio / HTTP presets).
// The proxy behind it resolves the real station redirect and reconnects on CDN
// token expiry without the box noticing.
func StreamSlot(slot int) string {
	return fmt.Sprintf("http://%s/stream/%d", Authority, slot)
}

// SpotifySlot is the per-slot Spotify Ogg URL for preset slot n. Each slot gets
// its own path so two Spotify presets do not collide on one box location (#22).
func SpotifySlot(slot int) string {
	return fmt.Sprintf("http://%s/spotify/stream-%d.ogg", Authority, slot)
}

// SpotifyDefault is the non-slot Spotify Ogg URL used for ad-hoc Spotify play.
// The .ogg suffix is required: the Bose renderer keys playability off the URL
// extension and rejects an extensionless Ogg stream (INVALID_SOURCE).
func SpotifyDefault() string {
	return fmt.Sprintf("http://%s/spotify/stream.ogg", Authority)
}

// RawStream wraps an arbitrary upstream URL in the ad-hoc stream-proxy route so
// the box plays HTTPS sources (which Bose UPnP cannot) through the proxy and
// token expiry is handled transparently.
func RawStream(rawURL string) string {
	return fmt.Sprintf("http://%s/stream/raw?u=%s", Authority,
		base64.RawURLEncoding.EncodeToString([]byte(rawURL)))
}

// NativeRadioPresets, when true, stores radio presets on the box as a native
// LOCAL_INTERNET_RADIO Orion location (the box self-activates it with no
// login-gated UPNP 1036, and STR's recall steps aside / falls back). Default
// FALSE: the native source mount is still flaky across firmwares (the box does
// not reliably fetch servicesAvailability + mount the source), so the proven UPNP
// stream-proxy location + the 1036 recovery is the reliable default until native
// is hardware-verified per model. Flipped on per box via the STICK_NATIVE_RADIO
// env var (wired in cmd/agent). All the native machinery (marge Orion/BMX
// handlers, recall native-first + UPnP fallback, boxcli native write, reconcile
// discriminator) stays in place and is a no-op while this is false.
var NativeRadioPresets = false

// Preset returns the box-side location stored in a preset slot: the per-slot
// Spotify Ogg URL for Spotify presets, the native Orion station location for
// radio when NativeRadioPresets is on, otherwise the proven stream-proxy slot URL.
func Preset(slot int, isSpotify bool) string {
	if isSpotify {
		return SpotifySlot(slot)
	}
	if NativeRadioPresets {
		return OrionStation(slot)
	}
	return StreamSlot(slot)
}
