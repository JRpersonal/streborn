package spotify

import (
	"context"
	"time"
)

// HoldsContext reports whether the engine has the context uri loaded with a
// track in it right now: it announced that context itself (will_play) and its
// /status names a loaded track.
//
// The recall verify asks this before its last-resort full re-Play. That
// re-Play exists for a playlist that never loaded at all (a cold-boot auth
// race). When the playlist did load and only the box failed to attach, for
// example after the speaker refused its own copy of the key from standby
// (1036), a re-Play throws the loaded song away: a shuffle preset then picks a
// NEW random song, and the Spotify app shows the first one skipped although
// nobody heard it (maintainer's ST30, 2026-11-02). Re-pointing the box keeps
// the song that is loaded.
//
// False on any doubt (no announcement, another context, no track, the engine
// not answering), so a recall that really failed still gets its re-Play.
func (m *Manager) HoldsContext(ctx context.Context, uri string) bool {
	want := normalizeContextURI(uri)
	m.mu.Lock()
	announced := m.engineContext
	m.mu.Unlock()
	if want == "" || announced == "" || normalizeContextURI(announced) != want {
		return false
	}
	sctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, _, _, state := m.liveNowPlayingState(sctx)
	return state == liveStatusTrack
}
