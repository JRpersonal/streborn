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
	"context"
	"encoding/base64"
	"html"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

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

// likedSongsAliasURI is Liked Songs as Spotify Connect announces it: one
// playlist id for EVERY account. In the #1983 bundle STR's engine reported it
// for one household member and the speaker's own Spotify client reported the
// very same id while another member played theirs.
const likedSongsAliasURI = "spotify:playlist:37i9dQZF1F5p3rmiWPIYgZ"

// accountBoundSpotifyURI reports whether a context URI names a different list
// for every account although the URI is the same for all of them: Liked Songs,
// as the alias above or as the bare collection. For those the URI alone does
// not say whose list plays. spotify:user:<id>:collection carries its owner and
// is compared like any other URI. Same rule as accountBoundSpotifyContext in
// the desktop app (spotifynowplaying.js).
func accountBoundSpotifyURI(uri string) bool {
	u := normalizeSpotifyURI(uri)
	return u == likedSongsAliasURI || u == "spotify:collection" || strings.HasPrefix(u, "spotify:collection:")
}

// presetsCollide reports whether saving p would land on the same thing other
// already holds, so the save is refused (loss-free) rather than silently
// deleting other. A radio station collides by stream URL, a Spotify key by its
// URI. The exception is an account-bound Spotify context (Liked Songs, the bare
// collection): every account's Liked Songs carry the same alias URI
// (37i9dQZF1F5p3rmiWPIYgZ), so URI alone would refuse a second household member
// their own Liked Songs key just because someone else saved theirs (#1983).
// When both keys name an account, such a context collides only for the SAME
// account; when either account is unknown (the speaker's own Spotify client
// reports none, the same blind spot as the key highlight) it falls back to URI
// alone, so two indistinguishable Liked Songs keys still cannot be created.
// Normal playlists, albums and tracks are unchanged.
func presetsCollide(p, other presets.Preset) bool {
	switch {
	case p.Type == "spotify" && p.URI != "" && other.URI == p.URI:
		if accountBoundSpotifyURI(p.URI) && p.Account != "" && other.Account != "" {
			return p.Account == other.Account
		}
		return true
	case p.Type != "spotify" && p.StreamURL != "" && other.StreamURL == p.StreamURL:
		return true
	default:
		return false
	}
}

// spotifySlotForURI is the lowest Spotify key holding uri, or 0. The store
// refuses one playlist on two keys (#836), so a second match is an older
// store; the lowest wins so the highlight does not hop between polls.
//
// playingAccount names the account that plays, or is nil when nothing can
// name it (the speaker's own Spotify client reports none). It is only asked
// for a key holding Liked Songs, whose URI is the same for every account:
// such a key is named only when the account that plays is known to be the one
// the key was saved under (#1983). It is asked at most once.
func spotifySlotForURI(list []presets.Preset, uri string, playingAccount func() string) int {
	want := normalizeSpotifyURI(uri)
	if want == "" {
		return 0
	}
	bound := accountBoundSpotifyURI(want)
	account, asked := "", false
	best := 0
	for _, p := range list {
		if p.Type != "spotify" || p.URI == "" || p.Slot < 1 {
			continue
		}
		if normalizeSpotifyURI(p.URI) != want {
			continue
		}
		if bound {
			if !asked && playingAccount != nil {
				account = strings.TrimSpace(playingAccount())
			}
			asked = true
			if saved := strings.TrimSpace(p.Account); saved == "" || saved != account {
				continue
			}
		}
		if best == 0 || p.Slot < best {
			best = p.Slot
		}
	}
	return best
}

// playingSpotifySlot names the Spotify key that holds what plays, or 0.
// location is the now-playing location (XML-unescaped), engineContext the
// context STR's engine plays and engineAccount the account it is logged in
// with (nil: unknown).
func playingSpotifySlot(location, engineContext string, engineAccount func() string, list []presets.Preset) int {
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
		return spotifySlotForURI(list, engineContext, engineAccount)
	}
	// Anything else that names a Spotify context is the speaker's own
	// client; the engine may still hold an older, paused context then, and
	// must not be asked (#1077 issue 15). That client names no account
	// (sourceAccount is the placeholder "SpotifyConnectUserName"), and the
	// engine's login says nothing about who plays there.
	return spotifySlotForURI(list, containerSpotifyURI(location), nil)
}

// playingKeyAccountTTL is how long the engine's login is reused for the
// playing-key header. Every /api/status poll of every client asks while Liked
// Songs plays on the engine, and each fresh answer is a request to the engine.
const playingKeyAccountTTL = 10 * time.Second

// playingKeyAccount is the account STR's engine is logged in with, for the
// Liked Songs rule in spotifySlotForURI. "" when it cannot be told.
func (s *Server) playingKeyAccount() string {
	if s.spotifyUser == nil {
		return ""
	}
	s.playingKeyAcctMu.Lock()
	defer s.playingKeyAcctMu.Unlock()
	if !s.playingKeyAcctAt.IsZero() && time.Since(s.playingKeyAcctAt) < playingKeyAccountTTL {
		return s.playingKeyAcct
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.playingKeyAcct, s.playingKeyAcctAt = s.spotifyUser(ctx), time.Now()
	return s.playingKeyAcct
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
	if n := playingSpotifySlot(loc, engine, s.playingKeyAccount, s.presets.All()); n > 0 {
		w.Header().Set(playingKeyHeader, strconv.Itoa(n))
	}
}
