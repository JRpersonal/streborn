package webui

// The cover of a Spotify key saved while the speaker's OWN Spotify plays it
// (#1870, 2026-11-13).
//
// A key saved from what plays gets its cover from Spotify's public lookup
// first, and when that has none (a private playlist), from the song STR's
// engine plays. Both miss when the speaker's built-in Spotify Connect plays
// the playlist: the lookup cannot see a private playlist, and the engine plays
// nothing. shorty310's bundle shows exactly that key stored with no art, while
// STR Remote showed a cover for it. That cover is the one the speaker itself
// reports in its now_playing, which is where this one comes from too: the
// playlist's own picture when the firmware names one (containerArt), the
// playing song's otherwise (art). It is only taken when the speaker's
// ContentItem is the very playlist being saved.

import (
	"context"
	"encoding/base64"
	"strings"
	"time"
)

// boxSpotifyCover returns the cover the speaker shows while its own Spotify
// plays uri, or "" when it plays something else, cannot be read, or shows no
// picture.
func (s *Server) boxSpotifyCover(uri string) string {
	if uri == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	doc, err := s.boxNowPlayingDoc(ctx)
	if err != nil {
		return ""
	}
	now, ok := parseBoxNowItem(doc)
	if !ok || now.Source != "SPOTIFY" {
		return ""
	}
	if normalizeSpotifyURI(boxSpotifyContainerURI(now.Location)) != uri {
		return ""
	}
	for _, art := range []string{now.ContainerArt, now.Art} {
		if isHTTPURL(art) {
			return art
		}
	}
	return ""
}

// boxSpotifyContainerURI decodes the context URI out of the location the
// speaker's own Spotify reports ("/playback/container/<base64 URI>"). STR's
// own locations use the URL-safe alphabet (legacySpotifyURI); the firmware's
// use the standard one, which can carry '+' and '/', so the whole tail is
// tried with every alphabet first. legacySpotifyURI cuts at the first '/',
// which turns a standard-alphabet tail into a cut-off URI, so it only gets
// what the whole tail does not decode (a location with a path after it).
func boxSpotifyContainerURI(location string) string {
	const marker = "/playback/container/"
	i := strings.Index(location, marker)
	if i < 0 {
		return ""
	}
	enc := location[i+len(marker):]
	if j := strings.IndexAny(enc, "?#"); j >= 0 {
		enc = enc[:j]
	}
	for _, d := range []*base64.Encoding{base64.RawStdEncoding, base64.StdEncoding, base64.RawURLEncoding, base64.URLEncoding} {
		if b, err := d.DecodeString(enc); err == nil && strings.HasPrefix(string(b), "spotify:") {
			return string(b)
		}
	}
	return legacySpotifyURI(location)
}
