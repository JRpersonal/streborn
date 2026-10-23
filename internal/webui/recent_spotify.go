// recent_spotify.go: the live Spotify song hook for Recently played (#135).
//
// Recently played groups songs under a card (the playlist). Until this file
// existed, the card was only ever opened by STR's own recall (a key press or a
// play from the app, playback.go), and a song arriving without an open card was
// dropped. That silently excluded the most common way people play Spotify on
// the speaker: picking the STR Connect device in the Spotify app on the phone.
// On the maintainer's ST30 (2026-10-24) the engine logged track 23, 24, 25
// while the newest Recently played entry was six days old. Songs and the
// playlist they came from belong in the list whoever started them, so a song
// without a card now opens one from the context the engine announced, and a
// song under a different context than the open card's starts a new card (the
// user switched playlists in the app).

package webui

import (
	"context"
	"time"

	"github.com/JRpersonal/streborn/internal/recent"
)

// SpotifyNowPlaying is a song change as the Spotify engine reports it to
// NoteRecentSpotifyTrack (cmd/agent maps the engine's own value onto it, since
// webui does not import internal/spotify). Track is the only required field.
type SpotifyNowPlaying struct {
	Track, Artist, Cover string
	// TrackURI is the song's spotify:track: URI when the engine named it.
	TrackURI string
	// ContextURI is the playlist/album the engine announced for the song, as
	// announced ("" when it never announced one). It is normalised here.
	ContextURI string
}

const (
	// recentSpotifySessionKey is the card for Spotify songs the engine plays
	// without naming a playlist (a single track, or the stale-context case the
	// engine documents on its TrackChange). One such card at a time: a new
	// session without a context replaces the older one, like any card.
	recentSpotifySessionKey = "spotify:session"
	// recentSpotifySessionName is that card's name and the name of a playlist
	// card whose title could not be resolved. "Spotify" rather than a URI, so
	// the list never shows an identifier to the user.
	recentSpotifySessionName = "Spotify"
	// recentSpotifyMetaCacheMax bounds the per-playlist title/cover cache. 32
	// playlists is far more than one listening day; when full the cache is
	// simply dropped and refilled.
	recentSpotifyMetaCacheMax = 32
	// recentSpotifyMetaDefaultTimeout bounds one oEmbed lookup. The hook runs
	// on the engine's event reader, so a slow lookup delays the next event by
	// at most this, once per new playlist.
	recentSpotifyMetaDefaultTimeout = 4 * time.Second
	// recentSpotifyAccountTimeout bounds the one engine /status call that names
	// the account a card is filed under.
	recentSpotifyAccountTimeout = 2 * time.Second
)

// recentSpotifyMeta is one resolved playlist: its title and cover.
type recentSpotifyMeta struct{ name, art string }

// NoteRecentSpotifyTrack files a live Spotify song into Recently played. Wired
// to the Spotify manager's onTrack hook.
//
// Which card the song lands under:
//   - an open card whose context matches the song's (or a recall card that has
//     not seen a context yet): that card, as before (#135);
//   - no open card, or the song names a different playlist than the open card:
//     a new card for the song's playlist, named and pictured from a preset
//     holding the same playlist, else from Spotify's oEmbed, else "Spotify";
//   - no context at all and no open card: the "Spotify" session card with the
//     song's cover as its art.
//
// A recall card keeps its key (the preset's URI) and so its Play button in
// both apps; a card opened here for a playlist that sits on a key uses that
// same URI, so the two merge into one card and the Play button appears there
// too. A playlist on no key, and the session card, stay history plus a save
// button, exactly as the apps already treat a card without a slot.
func (s *Server) NoteRecentSpotifyTrack(np SpotifyNowPlaying) {
	if s.recent == nil || np.Track == "" {
		return
	}
	ctxURI := normalizeSpotifyURI(np.ContextURI)
	s.recentMu.Lock()
	c := s.recentSpotifyCard
	switch {
	case c.key == "":
		// No card open: opened below.
	case c.key == recentSpotifySessionKey:
		// A playlist appeared under the session card: it gets its own card.
		if ctxURI != "" {
			c = recentCardCtx{}
		}
	case c.spotifyCtx == "":
		// A recall card learns its context from the first song under it. Learn
		// the engine's spelling rather than deriving it from the preset URI, so
		// a wrapper or user-scoped form the engine announces for the same
		// playlist does not read as a switch.
		if ctxURI != "" {
			c.spotifyCtx = ctxURI
			s.recentSpotifyCard = c
		}
	case ctxURI != "" && ctxURI != c.spotifyCtx:
		// The user switched playlists in the Spotify app: new card below. A
		// song WITHOUT a context stays under the open card, since the engine's
		// context cannot be told apart from a stale one in that case and the
		// open card is the best guess.
		c = recentCardCtx{}
	}
	s.recentMu.Unlock()
	if c.key == "" {
		c = s.recentOpenSpotifyCard(np, ctxURI)
	}
	// Store artist-first ("Artist - Title"). The view's formatTrack reads " - " as
	// the Shoutcast "Artist - Title" order, so this renders the artist on the lead
	// line exactly like radio, instead of mislabelling the song title as artist.
	full := np.Track
	if np.Artist != "" {
		full = np.Artist + " - " + np.Track
	}
	url := c.url
	if c.key == recentSpotifySessionKey && np.TrackURI != "" {
		// The session card has no playlist to replay, so each row points at
		// its own song: the apps read the card's URL off the newest row, and a
		// save then keeps the song that is playing, not the session's first.
		url = np.TrackURI
	}
	s.recent.Add(recent.Entry{Source: "spotify", CardKey: c.key, CardName: c.name, CardArt: c.art, CardURL: url, Track: full, Account: c.account})
}

// recentOpenSpotifyCard opens the card for a song the engine plays without an
// STR recall and remembers it as the active Spotify card. ctxURI is the
// normalised context, "" for the session card.
func (s *Server) recentOpenSpotifyCard(np SpotifyNowPlaying, ctxURI string) recentCardCtx {
	var c recentCardCtx
	how := "session"
	if ctxURI == "" {
		// The session card's replay target is the song itself: the save button
		// then keeps the song on a key (a track URI is a valid recall), and the
		// apps show no Play button because no key holds "spotify:session".
		c = recentCardCtx{key: recentSpotifySessionKey, name: recentSpotifySessionName, art: np.Cover, url: np.TrackURI}
	} else {
		key, name, art, src := s.recentSpotifyPlaylistCard(ctxURI)
		how = src
		if name == "" {
			name = recentSpotifySessionName
		}
		if art == "" {
			art = np.Cover
		}
		c = recentCardCtx{key: key, name: name, art: art, url: key, spotifyCtx: ctxURI}
	}
	if s.spotifyUser != nil {
		actx, cancel := context.WithTimeout(context.Background(), recentSpotifyAccountTimeout)
		c.account = s.spotifyUser(actx)
		cancel()
	}
	s.recentMu.Lock()
	s.recentSpotifyCard = c
	s.recentMu.Unlock()
	if s.logger != nil {
		s.logger.Info("recently played: Spotify is playing without an STR recall, card opened from the engine's context",
			"card", c.key, "name", c.name, "namedBy", how, "track", np.Track)
	}
	return c
}

// recentSpotifyPlaylistCard names a playlist card: the key to file it under,
// its title and cover, and where they came from ("preset", "oembed", "cache"
// or "fallback" when nothing answered). A preset holding the same playlist
// wins, so the card carries the preset's URI spelling as its key and merges
// with the recall card of that key; without one the engine's oEmbed lookup is
// asked once per playlist and cached.
func (s *Server) recentSpotifyPlaylistCard(ctxURI string) (key, name, art, src string) {
	if s.presets != nil {
		for _, p := range s.presets.All() {
			if p.Type == "spotify" && p.URI != "" && normalizeSpotifyURI(p.URI) == ctxURI {
				return p.URI, p.Name, p.Art, "preset"
			}
		}
	}
	s.recentMu.Lock()
	m, ok := s.recentSpotifyMetaCache[ctxURI]
	s.recentMu.Unlock()
	if ok {
		return ctxURI, m.name, m.art, "cache"
	}
	if s.spotifyMeta == nil {
		return ctxURI, "", "", "fallback"
	}
	timeout := s.recentSpotifyMetaTimeout
	if timeout <= 0 {
		timeout = recentSpotifyMetaDefaultTimeout
	}
	mctx, cancel := context.WithTimeout(context.Background(), timeout)
	art, name = s.spotifyMeta(mctx, ctxURI)
	cancel()
	if name == "" && art == "" {
		// Not cached: the next song of this playlist asks again, so a blip
		// during the first song does not leave the card nameless for good.
		return ctxURI, "", "", "fallback"
	}
	s.recentMu.Lock()
	if len(s.recentSpotifyMetaCache) >= recentSpotifyMetaCacheMax {
		s.recentSpotifyMetaCache = nil
	}
	if s.recentSpotifyMetaCache == nil {
		s.recentSpotifyMetaCache = make(map[string]recentSpotifyMeta)
	}
	s.recentSpotifyMetaCache[ctxURI] = recentSpotifyMeta{name: name, art: art}
	s.recentMu.Unlock()
	return ctxURI, name, art, "oembed"
}
