package spotify

import "time"

// The now-playing reply must describe ONE thing.
//
// A cold recall sets lastContext to the new playlist the moment it starts, but
// the song fields keep describing the old one until the engine has loaded the
// new context: the cache holds the old track, and go-librespot's /status keeps
// reporting it until the paused load lands. /spotify/info therefore answered,
// for several seconds after a preset switch, with the NEW context next to the
// OLD track, artist and cover, and the desktop app printed exactly that:
// `Playlist: "Purpose for Pain" · Rush - Tom Sawyer` (discussion #1077).
//
// While a recall for context X is pending, the song fields are reported empty
// until the engine shows a track that belongs to X. Outside a recall nothing
// changes: an idle engine still lets the display keep the cached track (see
// ServeInfo), which is the only reason that cache is served at all.

// recallDisplayWindow bounds the gate. A recall that never confirms (the engine
// resumed the very same track, or an event was missed) must not blank the song
// for good; after this the reply falls back to the ungated behaviour.
const recallDisplayWindow = 20 * time.Second

// armRecallDisplayLocked marks a cold recall of uri as pending. It snapshots the
// track playing before the recall, so a later report of a DIFFERENT track can
// confirm the switch. Caller holds m.mu and calls this before curTrackURI is
// cleared.
func (m *Manager) armRecallDisplayLocked(uri string, now time.Time) {
	m.recallWant = normalizeContextURI(uri)
	m.recallFromTrack = m.curTrackURI
	m.recallFromName = m.curName
	m.recallCtxSeen = false
	m.recallWantUntil = now.Add(recallDisplayWindow)
}

// clearRecallDisplayLocked ends the gate. Caller holds m.mu.
func (m *Manager) clearRecallDisplayLocked() {
	m.recallWant = ""
	m.recallFromTrack = ""
	m.recallFromName = ""
	m.recallCtxSeen = false
	m.recallWantUntil = time.Time{}
}

// noteRecallContextLocked records a will_play announcement. A match with the
// pending context does not confirm on its own (/status may still name the old
// track for a moment); it makes the NEXT metadata event authoritative, because
// that event describes the track the engine just loaded from it. Caller holds
// m.mu.
func (m *Manager) noteRecallContextLocked(ctxURI string) {
	if m.recallWant != "" && normalizeContextURI(ctxURI) == m.recallWant {
		m.recallCtxSeen = true
	}
}

// noteRecallTrackLocked looks at a track the engine reported and ends the gate
// once it is the recalled context's. fromMetadata is true for a pushed metadata
// event, false for a /status read. Caller holds m.mu.
func (m *Manager) noteRecallTrackLocked(trackURI, name string, fromMetadata bool) {
	if m.recallWant == "" || name == "" {
		return
	}
	if fromMetadata && m.recallCtxSeen {
		m.clearRecallDisplayLocked()
		return
	}
	changed := false
	switch {
	case trackURI != "" && m.recallFromTrack != "":
		changed = trackURI != m.recallFromTrack
	default:
		changed = name != m.recallFromName
	}
	if changed {
		m.clearRecallDisplayLocked()
	}
}

// recallDisplayPendingLocked reports whether the song fields must stay empty
// because a recall is still loading. Expires the gate past its window. Caller
// holds m.mu.
func (m *Manager) recallDisplayPendingLocked(now time.Time) bool {
	if m.recallWant == "" {
		return false
	}
	if !now.Before(m.recallWantUntil) {
		m.clearRecallDisplayLocked()
		return false
	}
	return true
}
