// A Spotify key that was stored without a cover or without a name gets them
// the first time it plays (discussion #1870).
//
// The save fills both from Spotify's lookup (presetsave.go). When that lookup
// has no answer, the key used to stay "Spotify" with the green glyph for good:
// a re-save of the same playlist repeats the same lookup with the same result,
// and nothing else ever looked again. A reporter had exactly that on two
// speakers, for one playlist, while the same speakers named other playlists
// fine; a third-party remote showed a cover on the key while it played (the
// playing song's), which made the STR apps' glyph look like a bug.
//
// So the song hook does two things for the key that holds the playing
// playlist:
//
//   - a key still called "Spotify" (or nothing), or one without a cover, asks
//     the lookup once more, at most once per playlist per agent run;
//   - a key that still has no cover after that takes the playing song's album
//     cover. It is a picture out of the playlist rather than the playlist's
//     own, but it is stored once and then stays, so unlike the live now-playing
//     art it neither changes nor lags on the tile.
//
// NAND: the store is written only when a field actually changed, which for one
// key happens at most twice in its life (cover, name). A key that has both
// costs one look at the in-memory store per song and nothing else. No timer,
// no polling: the trigger is the engine's own song change.
//
// A name the user typed is never touched: only the "Spotify" placeholder the
// save paths write, and an empty name, count as missing.

package webui

import (
	"context"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
)

const (
	// spotifyKeyPlaceholderName is what the save paths call a Spotify key
	// whose playlist title they could not read.
	spotifyKeyPlaceholderName = "Spotify"
	// spotifyKeyMetaTriedMax bounds the "already asked in this run" set. It is
	// dropped when full, which costs at most one more lookup per playlist.
	spotifyKeyMetaTriedMax = 32
)

// spotifyKeyNeedsName reports whether a Spotify key's name is missing.
func spotifyKeyNeedsName(name string) bool {
	name = strings.TrimSpace(name)
	return name == "" || name == spotifyKeyPlaceholderName
}

// noteSpotifySongForKeys is the song hook's part for the preset keys: it
// remembers the playing song's cover for a save that follows, and completes
// the key that holds the playing playlist. ctxURI is the normalised context,
// "" when the engine named none (then nothing is done: without a context the
// song cannot be tied to a key).
func (s *Server) noteSpotifySongForKeys(np SpotifyNowPlaying, ctxURI string) {
	if ctxURI == "" {
		return
	}
	cover := ""
	if isHTTPURL(np.Cover) {
		cover = np.Cover
	}
	s.recentMu.Lock()
	s.spotifySongCtx, s.spotifySongCover = ctxURI, cover
	s.recentMu.Unlock()
	if s.presets == nil {
		return
	}
	p, ok := spotifyKeyForContext(s.presets.All(), ctxURI)
	if !ok {
		return
	}
	needName, needArt := spotifyKeyNeedsName(p.Name), strings.TrimSpace(p.Art) == ""
	if !needName && !needArt {
		return
	}
	metaCover, metaTitle := "", ""
	if s.spotifyMeta != nil && s.spotifyKeyMetaFirstTry(ctxURI) {
		timeout := s.recentSpotifyMetaTimeout
		if timeout <= 0 {
			timeout = recentSpotifyMetaDefaultTimeout
		}
		mctx, cancel := context.WithTimeout(context.Background(), timeout)
		metaCover, metaTitle = s.spotifyMeta(mctx, p.URI)
		cancel()
	}
	// Read the key again: the lookup took a moment, and a save, a rename or a
	// delete from an app must win over what was read before it.
	cur, ok := s.presets.Get(p.Slot)
	if !ok || cur.Type != "spotify" || cur.URI != p.URI {
		return
	}
	artFrom, renamed := "", false
	if strings.TrimSpace(cur.Art) == "" {
		switch {
		case isHTTPURL(metaCover):
			cur.Art, artFrom = metaCover, "playlist"
		case cover != "":
			cur.Art, artFrom = cover, "playing song"
		}
	}
	prevName := cur.Name
	if title := strings.TrimSpace(metaTitle); spotifyKeyNeedsName(cur.Name) && title != "" && !spotifyKeyNeedsName(title) {
		cur.Name, renamed = title, true
	}
	if artFrom == "" && !renamed {
		return
	}
	if err := s.presets.SetSlot(cur); err != nil {
		if s.logger != nil {
			s.logger.Warn("spotify key: could not store the cover or name learned while it played", "slot", cur.Slot, "err", err)
		}
		return
	}
	if s.logger != nil {
		s.logger.Info("spotify key: completed from what plays, it was stored without a cover or a name",
			"slot", cur.Slot, "was", prevName, "now", cur.Name, "renamed", renamed, "coverFrom", artFrom)
	}
	// The open Recently played card was started with the old label.
	s.recentMu.Lock()
	if c := s.recentSpotifyCard; c.key == cur.URI {
		if spotifyKeyNeedsName(c.name) {
			c.name = cur.Name
		}
		if c.art == "" {
			c.art = cur.Art
		}
		s.recentSpotifyCard = c
	}
	s.recentMu.Unlock()
	// The speaker's own key entry carries the name (never the cover for a
	// Spotify key, writeBoxPreset), so only a new name goes there. Off the
	// engine's event reader, best-effort like the rename's push: the store
	// already holds the name and the next preset sync repairs a failed write.
	if renamed && s.boxHost != "" {
		go func(p presets.Preset) {
			boxCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.writeStorePresetToBox(boxCtx, p); err != nil && s.logger != nil {
				s.logger.Warn("box preset sync failed after a Spotify key got its name", "slot", p.Slot, "err", err)
			}
		}(cur)
	}
}

// spotifyKeyForContext is the lowest Spotify key holding the normalised
// context ctxURI. The store keeps one playlist on at most one key (#836); the
// lowest wins for an older store, as for the key highlight.
func spotifyKeyForContext(list []presets.Preset, ctxURI string) (presets.Preset, bool) {
	var best presets.Preset
	found := false
	for _, p := range list {
		if p.Type != "spotify" || p.URI == "" || p.Slot < 1 || normalizeSpotifyURI(p.URI) != ctxURI {
			continue
		}
		if !found || p.Slot < best.Slot {
			best, found = p, true
		}
	}
	return best, found
}

// spotifyKeyMetaFirstTry reports whether the lookup for ctxURI has not been
// made in this agent run yet, and marks it as made. A playlist Spotify does
// not name is asked about once, not once per song.
func (s *Server) spotifyKeyMetaFirstTry(ctxURI string) bool {
	s.recentMu.Lock()
	defer s.recentMu.Unlock()
	if _, done := s.spotifyKeyMetaTried[ctxURI]; done {
		return false
	}
	if len(s.spotifyKeyMetaTried) >= spotifyKeyMetaTriedMax {
		s.spotifyKeyMetaTried = nil
	}
	if s.spotifyKeyMetaTried == nil {
		s.spotifyKeyMetaTried = make(map[string]struct{})
	}
	s.spotifyKeyMetaTried[ctxURI] = struct{}{}
	return true
}

// spotifyPlayingSongCover is the album cover of the song the engine last
// announced under the context uri, "" when the last song belonged to another
// context or carried no cover. The save uses it for a key whose playlist has
// no cover of its own to look up.
func (s *Server) spotifyPlayingSongCover(uri string) string {
	want := normalizeSpotifyURI(uri)
	if want == "" {
		return ""
	}
	s.recentMu.Lock()
	defer s.recentMu.Unlock()
	if s.spotifySongCtx != want {
		return ""
	}
	return s.spotifySongCover
}
