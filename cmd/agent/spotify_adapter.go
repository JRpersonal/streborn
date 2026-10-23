// spotifyEngine adapts the go-librespot manager (internal/spotify) to the
// webui.SpotifyEngine interface and its optional sub-interfaces, so main.go
// wires the whole Spotify control plane with one webui.WithSpotify call.
//
// Most methods forward 1:1. The three that do not (Play, Streaming, Skip)
// carry the glue that used to live in closures in main.go, with the reasoning
// kept next to the code.

package main

import (
	"context"
	"net/http"
	"time"

	"github.com/JRpersonal/streborn/internal/spotify"
	"github.com/JRpersonal/streborn/internal/webui"
)

type spotifyEngine struct {
	m *spotify.Manager
}

// The adapter implements the base interface and all three optional
// sub-interfaces, so every Spotify field of the server is set on the speaker,
// exactly as the per-field options set them before.
var (
	_ webui.SpotifyEngine          = (*spotifyEngine)(nil)
	_ webui.SpotifyCredentialSync  = (*spotifyEngine)(nil)
	_ webui.SpotifyStatusPages     = (*spotifyEngine)(nil)
	_ webui.SpotifyEngineLifecycle = (*spotifyEngine)(nil)
)

func (e *spotifyEngine) SwitchedAway(ctx context.Context)                   { e.m.SwitchedAway(ctx) }
func (e *spotifyEngine) UserStopped(ctx context.Context, reason string)     { e.m.UserStopped(ctx, reason) }
func (e *spotifyEngine) ServeStream(w http.ResponseWriter, r *http.Request) { e.m.ServeOgg(w, r) }

func (e *spotifyEngine) Play(ctx context.Context, uri, account string, shuffle, repeat bool) error {
	return e.m.PlayAccount(ctx, uri, account, spotify.PlayOptions{Shuffle: shuffle, Repeat: repeat})
}

func (e *spotifyEngine) CurrentUser(ctx context.Context) string { return e.m.CurrentUsername(ctx) }
func (e *spotifyEngine) PlayingContext() string                 { return e.m.PlayingContext() }
func (e *spotifyEngine) Shuffling(ctx context.Context) bool     { return e.m.ShufflingContext(ctx) }
func (e *spotifyEngine) Repeating(ctx context.Context) bool     { return e.m.RepeatingContext(ctx) }

func (e *spotifyEngine) Meta(ctx context.Context, uri string) (cover, title string) {
	return e.m.PlaylistMeta(ctx, uri)
}

// Streaming must mean audio is actually flowing, not merely that the box
// holds the connection open: a stalled sink (attached, zero audio pages) used
// to satisfy the recall verify, so a failed Spotify recall reported success
// while the user stared at a spinner until the box timed out (field
// 2026-07-27). A stall counts as not-streaming so the verify keeps working on
// it and the failure becomes visible.
func (e *spotifyEngine) Streaming() bool {
	return e.m.Streaming() && !e.m.StreamStalled()
}

// Skip arms the skip-cut first, so the boundary this skip produces drops the
// old track's unsent tail instead of playing it out.
func (e *spotifyEngine) Skip(ctx context.Context, forward bool) error {
	e.m.NoteSkip()
	if forward {
		return e.m.Next(ctx)
	}
	return e.m.Prev(ctx)
}

func (e *spotifyEngine) SkipBoundary() time.Time { return e.m.LastSkipBoundary() }

// ArmRecallCut and BoundaryStaleKB form the recall boundary cut: armed with
// SetRecalling so a preset re-press never feeds the box the old track's audio
// during the load preamble; the stale-KB probe lets the recall re-push stand
// down when the cut kept the box clean.
func (e *spotifyEngine) ArmRecallCut()                       { e.m.ArmRecallCut() }
func (e *spotifyEngine) BoundaryStaleKB() int64              { return e.m.LastBoundaryStaleKB() }
func (e *spotifyEngine) Ready() bool                         { return e.m.Ready() }
func (e *spotifyEngine) CanRecall(ctx context.Context) bool  { return e.m.CanRecall(ctx) }
func (e *spotifyEngine) PremiumRequired() bool               { return e.m.PremiumRequired() }
func (e *spotifyEngine) SetRecalling()                       { e.m.SetRecalling() }
func (e *spotifyEngine) SuppressActivate(d time.Duration)    { e.m.SuppressActivate(d) }
func (e *spotifyEngine) ExpectReattach(window time.Duration) { e.m.ExpectReattach(window) }

// webui.SpotifyCredentialSync
func (e *spotifyEngine) ExportCred() ([]byte, error) { return e.m.ExportCredential() }
func (e *spotifyEngine) ImportCred(ctx context.Context, data []byte) error {
	return e.m.ImportCredential(ctx, data)
}

// webui.SpotifyStatusPages
func (e *spotifyEngine) ServeInfo(w http.ResponseWriter, r *http.Request)    { e.m.ServeInfo(w, r) }
func (e *spotifyEngine) ServeQuality(w http.ResponseWriter, r *http.Request) { e.m.ServeQuality(w, r) }

// webui.SpotifyEngineLifecycle
func (e *spotifyEngine) Reload() bool { return e.m.ReloadBinary() }
func (e *spotifyEngine) Stop() bool   { return e.m.StopEngine() }
