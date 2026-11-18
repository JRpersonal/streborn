package spotify

import (
	"encoding/json"
	"errors"
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
	// A report after real audio has played is a new episode even inside the
	// run gap: whatever was refused before, something played in between.
	fresh := m.engineKeyDeniedAt.IsZero() || now.Sub(m.engineKeyDeniedAt) > keyRefusalRunGap ||
		!m.engineKeyDeniedAt.After(m.keyRefusalClearedAt)
	m.engineKeyDeniedAt = now
	m.engineKeyDeniedMsg = detail
	// From now on this engine is known to stop by itself, so the agent's own
	// pause at the end of a refusal run is not needed.
	m.engineStopsOnKeyDenial = true
	m.mu.Unlock()
	if fresh {
		m.logger.Warn("spotify: the engine stopped because Spotify refused the audio key for track after track; the speaker's own Spotify entry is unaffected",
			"detail", detail)
		// Once per episode: remember which account it was, so the apps can
		// keep saying so after the short AudioKeyRefused window has passed.
		m.noteKeyRefusalEpisode()
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

// noteKeyRefusalStreamedLocked ends the short-lived refusal state once the
// engine delivers real audio again, and reports whether the apps were being
// told about a refusal until now. Called from noteAccountStreamed, the same
// signal that clears the sticky per-account record: a refused key decrypts
// nothing, so a first audio page proves the engine plays (accountrefused.go).
//
// Field report, discussion #1930 (ST10): a few stream attempts were refused,
// then six preset presses in a row all played within two seconds, and the
// apps kept saying Spotify refuses the account over a track that was playing,
// for the rest of the ten-minute window.
//
// What it resets, and why that keeps the back-off honest:
//   - keyRefusalRun goes back to zero, because the run means "refused in a
//     row" and a track that plays breaks the row. An older engine that is
//     refused again needs a fresh run of keyRefusalRunTrips, and that run
//     trips the notice, the episode record and the agent's pause again.
//   - keyRefusalClearedAt is stamped, and AudioKeyRefused only counts an
//     engine key_denied report made after it. A newer engine that refuses
//     again right after is reported at once. The engine's own back-off is
//     cleared by the engine itself once a track plays (see the top of this
//     file), so nothing here has to tell it.
//
// engineKeyDeniedAt, lastKeyRefusalAt and keyRefusalGaveUpAt keep their
// values: the recall path asks engineKeyDeniedSince and playDenialHint about
// a play that just failed, and those questions are about what happened, not
// about what the apps should show.
//
// Cheap, because it runs on the drain path: a few field reads under the lock
// the caller already holds. Caller holds m.mu.
func (m *Manager) noteKeyRefusalStreamedLocked(now time.Time) bool {
	if m.keyRefusalRun == 0 && !m.engineKeyDeniedAt.After(m.keyRefusalClearedAt) {
		return false
	}
	showing := m.audioKeyRefusedLocked(now)
	m.keyRefusalRun = 0
	m.keyRefusalClearedAt = now
	return showing
}

// keyDeniedItemHint is the message for a play that failed on a single refused
// audio key, from an engine that does not count a run itself.
const keyDeniedItemHint = "Spotify refused to deliver the audio for this item (audio key denied). " +
	"This usually means the Spotify account on the speaker has no Premium subscription, " +
	"which STR's Spotify engine requires; occasionally the item itself is not licensed for streaming."

// isKeyDenialHint reports whether a playDenialHint answer names a refused audio
// key. Its third answer, the connection that was not ready yet, does not.
func isKeyDenialHint(hint string) bool {
	return hint == keyDeniedBackoffHint || hint == keyDeniedItemHint
}

// audioKeyDeniedError is a play that failed because Spotify refused the audio
// key, whichever of the two hints explains it. The recall paths must not answer
// it with another play: each one asks for a key per track again and feeds the
// throttle that keeps the refusal alive.
//
// A caller outside this package tells it by the AudioKeyDenied method (see
// IsAudioKeyDenied), not by the wording. The app recall used to look for the
// text "audio key denied", which only keyDeniedItemHint carries, so a play
// refused during the engine's back-off was retried (discussion #1947).
type audioKeyDeniedError struct {
	msg   string
	cause error
}

func (e *audioKeyDeniedError) Error() string        { return e.msg }
func (e *audioKeyDeniedError) Unwrap() error        { return e.cause }
func (e *audioKeyDeniedError) AudioKeyDenied() bool { return true }

// NewAudioKeyDeniedError wraps cause (nil when the engine accepted the play and
// refused the load) as a refused-audio-key play failure explained by hint.
func NewAudioKeyDeniedError(cause error, hint string) error {
	if cause == nil {
		return &audioKeyDeniedError{msg: "spotify play refused: " + hint}
	}
	return &audioKeyDeniedError{msg: cause.Error() + ": " + hint, cause: cause}
}

// IsAudioKeyDenied reports whether err, or an error it wraps, is a play that
// failed on a refused audio key. It asks for the AudioKeyDenied method rather
// than the type, so internal/webui, which does not import this package, makes
// the same test with the same three lines.
func IsAudioKeyDenied(err error) bool {
	var denied interface{ AudioKeyDenied() bool }
	return errors.As(err, &denied) && denied.AudioKeyDenied()
}
