// Which Spotify key is playing, for the phone remote's key highlight.
//
// The desktop app lights a Spotify key in three cases (renderPresets in
// desktop-app/frontend/src/main.js, with spotifySlotForContext and
// nativeSpotifySlot in spotifynowplaying.js):
//
//   - STR's engine plays the key's per-slot stream (/spotify/stream-<n>.ogg);
//   - STR's engine plays its generic stream (/spotify/stream.ogg), which is
//     what a playlist started from the Spotify app on the "(STR)" entry plays,
//     and the engine's context is the URI the key holds (#1217);
//   - the speaker's OWN Spotify client (the plain speaker entry in the Spotify
//     app) plays, and the context it names in its location,
//     /playback/container/<base64 spotify:...>, is the URI the key holds
//     (#1077 issue 18).
//
// The phone page only ever knew the first, so a key saved from what plays
// stayed dark there while the desktop app showed it playing (#1077 issues 19
// and 20). The phone cannot see the engine's context without a second request
// per poll, and every rule written twice drifts, so the agent decides and
// hands the answer out with /api/status, in a response header: the body has
// to stay the speaker's own XML, because every client regex-parses it.
//
// Matched on the URI only, never on the name: a preset literally named
// "Spotify" must not light up for whatever plays (#1077).

package webui

import (
	"encoding/base64"
	"html"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/JRpersonal/streborn/internal/presets"
)

// playingKeyHeader carries the key /api/status found playing, 1 to 12 (7 to
// 12 is the second row, #1110), and is
// absent when no Spotify key holds what plays.
const playingKeyHeader = "X-STR-Playing-Key"

var perSlotSpotifyStreamRe = regexp.MustCompile(`/spotify/stream-(\d+)\.ogg`)

// containerSpotifyURI recovers the spotify: URI from a now-playing location of
// the form /playback/container/<base64 spotify:...>, or "". The speaker's own
// Spotify client writes it in standard base64, STR's legacy form is URL-safe,
// so both alphabets are tried, padded and not.
func containerSpotifyURI(loc string) string {
	const marker = "/playback/container/"
	i := strings.Index(loc, marker)
	if i < 0 {
		return ""
	}
	enc := loc[i+len(marker):]
	if j := strings.IndexAny(enc, "?#"); j >= 0 {
		enc = enc[:j]
	}
	for _, d := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := d.DecodeString(enc); err == nil && strings.HasPrefix(string(b), "spotify:") {
			return string(b)
		}
	}
	// A path segment after the container (".../container/<b64>/more").
	return legacySpotifyURI(loc)
}

// spotifySlotForURI is the lowest Spotify key holding uri, or 0. The store
// refuses one playlist on two keys (#836), so a second match is an older
// store; the lowest wins so the highlight does not hop between polls.
func spotifySlotForURI(list []presets.Preset, uri string) int {
	want := normalizeSpotifyURI(uri)
	if want == "" {
		return 0
	}
	best := 0
	for _, p := range list {
		if p.Type != "spotify" || p.URI == "" || p.Slot < 1 {
			continue
		}
		if normalizeSpotifyURI(p.URI) != want {
			continue
		}
		if best == 0 || p.Slot < best {
			best = p.Slot
		}
	}
	return best
}

// playingSpotifySlot names the Spotify key that holds what plays, or 0.
// location is the now-playing location (XML-unescaped), engineContext the
// context STR's engine plays.
func playingSpotifySlot(location, engineContext string, list []presets.Preset) int {
	if strings.Contains(location, "/spotify/stream") {
		if m := perSlotSpotifyStreamRe.FindStringSubmatch(location); m != nil {
			n, _ := strconv.Atoi(m[1])
			for _, p := range list {
				if p.Slot == n && p.Type == "spotify" {
					return n
				}
			}
		}
		// The generic engine stream: the engine's context decides. An
		// empty context proves nothing and names no key.
		return spotifySlotForURI(list, engineContext)
	}
	// Anything else that names a Spotify context is the speaker's own
	// client; the engine may still hold an older, paused context then, and
	// must not be asked (#1077 issue 15).
	return spotifySlotForURI(list, containerSpotifyURI(location))
}

// setPlayingKeyHeader adds the playing Spotify key to an /api/status answer.
func (s *Server) setPlayingKeyHeader(w http.ResponseWriter, body []byte) {
	if s.presets == nil {
		return
	}
	m := nowPlayingLocationRe.FindSubmatch(body)
	if m == nil {
		return
	}
	loc := html.UnescapeString(string(m[1]))
	if !strings.Contains(loc, "/spotify/stream") && !strings.Contains(loc, "/playback/container/") {
		return
	}
	engine := ""
	if s.spotifyContext != nil {
		engine = s.spotifyContext()
	}
	if n := playingSpotifySlot(loc, engine, s.presets.All()); n > 0 {
		w.Header().Set(playingKeyHeader, strconv.Itoa(n))
	}
}
