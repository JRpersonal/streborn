package spotify

import (
	"encoding/json"
	"strings"
	"time"
)

// Newer engine builds (go-librespot fork, JRpersonal/go-librespot#9, #480)
// handle a run of audio-key refusals themselves: on the third refusal in a row
// they stop playback instead of skipping on, emit ONE playback_error event
// (stage "session", kind "key_denied") and log "stopping playback: spotify
// refused the audio key for several tracks in a row ...". For a back-off of
// 30 s doubling to 5 min (cleared once a track plays) they then refuse every
// new load with the same kind and the same wording.
//
// Older engines, still deployed on speakers, skip through the playlist and are
// caught by the refusal-run counter in noteLibrespotLine instead. Everything
// here is additive: an old engine never says any of it.

// engineKeyDeniedSentinel is the text of the engine's ErrAudioKeyDenied. It is
// in both the "stopping playback" line and the back-off "not loading" line.
const engineKeyDeniedSentinel = "spotify refused the audio key for several tracks in a row"

// engineKeyDeniedKind is the playback_error kind for a session-level refusal.
const engineKeyDeniedKind = "key_denied"

// keyDeniedShowFor is how long the app keeps saying so after the engine last
// reported a session-level refusal. Same window AudioKeyRefused always used.
const keyDeniedShowFor = 10 * time.Minute

// isEngineKeyDeniedLine reports whether a lowercased engine log line is the
// newer engine's session-level refusal (the stop, or a load held back after it).
func isEngineKeyDeniedLine(lc string) bool {
	return strings.Contains(lc, engineKeyDeniedSentinel)
}

// noteEngineKeyDenied records that the engine itself gave up on a run of
// refused audio keys (or held a load back because of one). Called from the
// playback_error event and from the log line, so it is idempotent: the first
// report of an episode logs, the rest only refresh the timestamp.
func (m *Manager) noteEngineKeyDenied(detail string) {
	now := time.Now()
	m.mu.Lock()
	fresh := m.engineKeyDeniedAt.IsZero() || now.Sub(m.engineKeyDeniedAt) > keyRefusalRunGap
	m.engineKeyDeniedAt = now
	m.engineKeyDeniedMsg = detail
	// From now on this engine is known to stop by itself, so the agent's own
	// pause at the end of a refusal run is not needed.
	m.engineStopsOnKeyDenial = true
	m.mu.Unlock()
	if fresh {
		m.logger.Warn("spotify: the engine stopped because Spotify refused the audio key for track after track; the speaker's own Spotify entry is unaffected",
			"detail", detail)
	}
}

// handleEnginePlaybackError takes the data of an engine playback_error event.
// Only the session-level key refusal is acted on; every other kind is the
// engine's own business and shows up in its log as before. Older engines never
// send key_denied.
func (m *Manager) handleEnginePlaybackError(data json.RawMessage) {
	var pe struct {
		Kind    string `json:"kind"`
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &pe) == nil && pe.Kind == engineKeyDeniedKind {
		m.noteEngineKeyDenied(pe.Message)
	}
}

// engineKeyDeniedSince reports whether the engine has reported a session-level
// refusal at or after t, so a recall that loaded nothing can say why.
func (m *Manager) engineKeyDeniedSince(t time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.engineKeyDeniedAt.IsZero() && !m.engineKeyDeniedAt.Before(t)
}

// keyDeniedBackoffHint is the message for a play the engine refused because it
// is still backing off after a run of refused audio keys.
const keyDeniedBackoffHint = "Spotify refused the audio key for several tracks in a row, so the speaker's Spotify engine " +
	"stopped and is waiting a few minutes before asking again. " +
	"This usually means Spotify does not let this engine stream with this account right now; " +
	"the speaker's own Spotify entry is unaffected."
