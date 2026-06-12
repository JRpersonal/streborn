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
)

// Authority is the agent's loopback host:port as seen from the box itself.
const Authority = "127.0.0.1:8888"

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

// Preset returns the box-side location stored in a preset slot: the per-slot
// Spotify Ogg URL for Spotify presets, the stream-proxy slot URL otherwise.
func Preset(slot int, isSpotify bool) string {
	if isSpotify {
		return SpotifySlot(slot)
	}
	return StreamSlot(slot)
}
