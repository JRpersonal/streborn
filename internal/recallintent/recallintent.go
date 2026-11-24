// Package recallintent carries why a preset recall happens from the code that
// starts it (internal/webui) to the engine that runs it (internal/spotify),
// across the function seams between the two, as a context value.
package recallintent

import (
	"context"
	"time"
)

type keepTrackKey struct{}

// WithKeepTrack marks a recall that must keep the track that plays.
//
// Pressing the key of the playlist that plays right now moves on to the next
// song (Jens, 2026-10-16). A recall STR makes on its own to bring a speaker
// back after a skip is not that press: the listener just chose the song with
// Next or Back, and moving on from it skipped a song nobody asked to skip
// (Jens' ST30, 2026-11-21: Back landed two songs further on).
func WithKeepTrack(ctx context.Context) context.Context {
	return context.WithValue(ctx, keepTrackKey{}, true)
}

// KeepTrack reports whether ctx carries WithKeepTrack.
func KeepTrack(ctx context.Context) bool {
	v, _ := ctx.Value(keepTrackKey{}).(bool)
	return v
}

type pressSinceKey struct{}

// WithPressSince marks a recall that answers the second half of a double press
// (#1110) whose first half arrived at t. That first half already ran row 1's
// recall, and when row 1 is no Spotify key, that recall paused the engine to
// switch the speaker away from Spotify. A pause STR put on the engine for the
// first half since t belongs to this same press: it is not the listener asking
// for the music to stop, so pressing twice for the playlist that played until
// then still moves on to its next track (Jens' ST30, 2026-11-22).
func WithPressSince(ctx context.Context, t time.Time) context.Context {
	return context.WithValue(ctx, pressSinceKey{}, t)
}

// PressSince returns the time WithPressSince put on ctx, and whether it did.
func PressSince(ctx context.Context) (time.Time, bool) {
	t, ok := ctx.Value(pressSinceKey{}).(time.Time)
	return t, ok && !t.IsZero()
}
