// Package recallintent carries why a preset recall happens from the code that
// starts it (internal/webui) to the engine that runs it (internal/spotify),
// across the function seams between the two, as a context value.
package recallintent

import "context"

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
