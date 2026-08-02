// recall.go: preset recall and playback control — Play and its resume/
// shuffle staging, transport commands, session gating, and the activation
// plumbing that points the box at the Spotify stream.

package spotify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// PlayOptions tunes a Spotify context recall.
type PlayOptions struct {
	// Shuffle starts the context on a random track with shuffle enabled, the
	// behaviour a preset saved with "shuffle" wants. When false (the default),
	// recall RESUMES the context on the last track that played from it (the
	// Spotify Connect "continue where you left off" behaviour) with shuffle OFF,
	// so the speaker's remote next/prev walk the playlist in order.
	Shuffle bool
}

// Play asks go-librespot to start playing a Spotify context (playlist/album/
// track) on this device, using its own cached credential. This is the
// autonomous-recall path: the agent calls it on a Spotify preset press, with no
// app involved.
//
// The recall calls Play FIRST, then points the box at /spotify/stream.ogg.
// With the sink-gated drain (see runOnce) go-librespot blocks on its full
// output pipe until the box attaches, so the pipe holds the track's start
// incl. the Ogg headers; the box therefore receives the stream from the
// beginning and can decode it.
//
// Shuffle is driven by opts, NOT forced on. The previous code force-enabled
// shuffle on EVERY recall, skipped to a random track, and never turned shuffle
// off again, so every preset press landed on a random song AND the remote's
// next key jumped around a still-shuffled queue (Patrick + Jens, 2026-06-25).
// Now: a default (non-shuffle) recall resumes the context where the user left
// off and keeps shuffle off; a shuffle preset starts on a fresh random track.
func (m *Manager) Play(ctx context.Context, uri string, opts PlayOptions) error {
	// Unwrap an ephemeral autoplay STATION context (spotify:station:playlist:X
	// -> spotify:playlist:X) at the single choke point every recall path -
	// app, hardware key, resume - flows through. Station contexts are
	// session-bound: recalled later they load a foreign or expired station
	// (field 2026-07-26: 51-track skip storms on every press of such a
	// preset). Mirrors webui's save-side normalizeSpotifyURI, which heals the
	// store; this heals whatever still arrives un-normalized.
	if rest, ok := strings.CutPrefix(strings.TrimSpace(uri), "spotify:station:"); ok && rest != "" {
		uri = "spotify:" + rest
	}
	// Mark a recall in progress so ServeOgg does not resume the OLD (mid) track
	// when the box attaches; this path drives the chosen track from its start.
	m.SetRecalling()
	// Point the resume tracker at the context we are loading right now and drop
	// the previous track. The will_play event that normally sets lastContext can
	// lag or be missed, so without this a metadata/status event arriving after
	// the recall window would record the NEW context against the OLD track and
	// corrupt the resume store (review, 2026-06-25).
	m.mu.Lock()
	m.lastContext = uri
	m.curTrackURI = ""
	m.mu.Unlock()
	// Default recall resumes on the last track that played from this context
	// (skip_to_uri). A shuffle preset ignores the resume point and starts random.
	resumeURI := ""
	if !opts.Shuffle {
		resumeURI = m.resume.trackFor(uri)
	}
	// Load the context PAUSED so the speaker never hears the wrong (non-resumed /
	// non-shuffled) track. skip_to_uri positions the queue on the resume track
	// before any audio flows; an empty skip_to_uri starts at the context's first
	// track. We then wait for the context to load, set the desired shuffle state,
	// and resume, so audio starts cleanly on the intended track from its start.
	// The box buffers on the cached Ogg headers during the short paused window,
	// the same way it already buffers during a cold load.
	playReq := map[string]any{"uri": uri, "paused": true}
	if resumeURI != "" {
		playReq["skip_to_uri"] = resumeURI
	}
	playAt := time.Now()
	playBody, _ := json.Marshal(playReq)
	if err := m.apiPostC(ctx, m.playClient, "/player/play", string(playBody)); err != nil {
		// The API 500 is bare; the reason (e.g. Spotify's audio-key denial on
		// a non-Premium account, #311) only appears on go-librespot's stderr.
		// Attach it so the app's error message explains itself.
		if hint := m.playDenialHint(); hint != "" {
			return fmt.Errorf("%w: %s", err, hint)
		}
		return err
	}
	// Belt-and-braces: stay paused even if this go-librespot build ignores the
	// paused flag in /player/play.
	_ = m.apiPost(ctx, "/player/pause", "")
	// shuffle_context is a no-op against an unloaded context (live: cold preset 6
	// then skipped to the deterministic 2nd track), so wait for the track to load.
	loaded := m.waitContextLoaded(ctx, 5*time.Second)
	// A resume-point recall passes skip_to_uri to seek to the track the user last
	// heard from this context. On a volatile context (a Spotify auto-generated
	// Radio / Daily Mix playlist whose track set drifts between sessions) that track
	// is frequently gone; go-librespot logs "failed seeking to track in context ...
	// could not find track" and loads nothing, so the box attaches to a silent
	// stream, the firmware throws UpnpRcvdContentItemInWrongState and detaches: the
	// intermittent "preset plays no music" seen on tight boxes (ST30, 2026-07-14).
	// Detect it (no track loaded, or the seek-failure line since this play) and
	// re-issue WITHOUT skip_to_uri so playback starts from the top of the context
	// instead of stalling. Shuffle recalls never set skip_to_uri, so they are
	// unaffected.
	if resumeURI != "" && (!loaded || m.seekFailedSince(playAt)) {
		m.logger.Warn("spotify: resume track not found in context, replaying from the top", "uri", uri, "resumeTrack", resumeURI)
		fromTop, _ := json.Marshal(map[string]any{"uri": uri, "paused": true})
		if err := m.apiPostC(ctx, m.playClient, "/player/play", string(fromTop)); err != nil {
			m.logger.Debug("spotify: replay-from-top after seek fail failed", "err", err)
		} else {
			_ = m.apiPost(ctx, "/player/pause", "")
			m.waitContextLoaded(ctx, 5*time.Second)
		}
	}
	// Set shuffle EXPLICITLY to the desired state every recall. Setting it to
	// false is what clears a stale shuffle left on by a previous shuffled recall
	// (the cross-recall stickiness that made an unshuffled preset still shuffle
	// and the remote next jump to a random song).
	if err := m.apiPost(ctx, "/player/shuffle_context",
		fmt.Sprintf(`{"shuffle_context":%t}`, opts.Shuffle)); err != nil {
		m.logger.Debug("spotify: shuffle_context failed", "err", err, "shuffle", opts.Shuffle)
	}
	if opts.Shuffle {
		// shuffle_context only randomises the UPCOMING queue (the current track
		// stays the context's first), so one skip lands on a random track. Still
		// paused, so nothing reaches the speaker yet.
		if err := m.apiPost(ctx, "/player/next", ""); err != nil {
			m.logger.Debug("spotify: skip-to-random after shuffle failed", "err", err)
		}
	}
	// Resume: audio now flows, starting on the chosen track from its beginning.
	if err := m.apiPost(ctx, "/player/resume", ""); err != nil {
		m.logger.Debug("spotify: resume after recall failed", "err", err)
	}
	m.logger.Info("spotify: recall play", "uri", uri, "shuffle", opts.Shuffle, "resumeTrack", resumeURI != "")
	// Debounce the will_play context change this recall triggers (this path
	// already drives the box separately, so no extra re-point needed).
	m.mu.Lock()
	m.lastActivate = time.Now()
	m.mu.Unlock()
	return nil
}

// waitContextLoaded polls go-librespot's /status until a track is loaded (the
// context is ready) or max elapses. Used by Play before shuffle_context, which
// is a no-op against an unloaded context. Returns true once a track is loaded,
// false if max/ctx elapsed with none (a stalled context, e.g. a resume seek that
// found no track), so the caller can recover.
func (m *Manager) waitContextLoaded(ctx context.Context, max time.Duration) bool {
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		if data, err := m.apiGet(ctx, "/status"); err == nil {
			var st struct {
				Track *struct {
					Name string `json:"name"`
				} `json:"track"`
			}
			if json.Unmarshal(data, &st) == nil && st.Track != nil && st.Track.Name != "" {
				return true
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
	return false
}

// seekFailedSince reports whether go-librespot logged a resume-seek failure
// ("could not find track") after t. Mirrors playDenialHint's one-beat retry: the
// stderr line lands right after /player/play returns, so give a slow pipe a moment
// before concluding the seek was fine.
func (m *Manager) seekFailedSince(t time.Time) bool {
	for attempt := 0; attempt < 2; attempt++ {
		m.mu.Lock()
		at := m.lastSeekFailAt
		m.mu.Unlock()
		if at.After(t) {
			return true
		}
		if attempt == 0 {
			time.Sleep(300 * time.Millisecond)
		}
	}
	return false
}

// Next and Prev skip tracks. Wired to the SoundTouch remote's next/prev keys:
// the box cannot skip a UPnP source itself (it emits QPLAY_SKIP_*_FAILED), so
// STR catches that and skips here instead. The new track reaches the box after
// its buffer drains.
func (m *Manager) Next(ctx context.Context) error { return m.apiPost(ctx, "/player/next", "") }
func (m *Manager) Prev(ctx context.Context) error { return m.apiPost(ctx, "/player/prev", "") }

// Pause and Resume mirror the obvious controls.
func (m *Manager) Pause(ctx context.Context) error {
	return m.apiPost(ctx, "/player/pause", "")
}

func (m *Manager) Resume(ctx context.Context) error {
	return m.apiPost(ctx, "/player/resume", "")
}

// SwitchedAway is called when the user deliberately points the box at a
// non-Spotify source (a radio preset, an ad-hoc station). It suppresses the #14
// auto-attach for a window so the still-connected go-librespot session does not
// yank the box back to Spotify, and pauses go-librespot so the playlist does not
// keep advancing silently in the background. Starting Spotify again from the app
// or recalling a Spotify preset un-pauses it. No-op when Spotify is not running.
func (m *Manager) SwitchedAway(ctx context.Context) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.suppressActivateUntil = time.Now().Add(10 * time.Second)
	running := m.sink != nil || m.cmd != nil
	m.mu.Unlock()
	if !running {
		return
	}
	if err := m.Pause(ctx); err != nil {
		m.logger.Debug("spotify: pause on source-switch failed", "err", err)
	}
}

// PlayingContext returns the Spotify context URI (playlist/album) go-librespot is
// currently playing, or "" if none. The preset-save path uses it to decide when
// the live account is authoritative: saving a preset for the content that is
// playing right now should stamp the account that is actually playing it, even
// over a stale account carried in from an earlier save.
func (m *Manager) PlayingContext() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastContext
}

// ErrNoSpotifySession is returned by PlayAccount when the speaker holds no live
// Spotify session and none could be re-established (no persisted credential, or
// the credential no longer authenticates, e.g. another controller took the
// account's single live session). The caller maps it to an actionable "tap this
// speaker in Spotify once" hint rather than letting the box buffer into nothing.
var ErrNoSpotifySession = errors.New("spotify: no live device session for recall")

// SessionActive reports whether go-librespot currently holds a live, authenticated
// device session (an active Connect device that can accept /player/play), as
// opposed to merely having a persisted credential on disk (LoggedIn). go-librespot
// auto-loads the persisted zeroconf credential at process start and re-auths on
// its own, but a cold start, a dropped AP connection (the "did not receive last
// pong ack" case), or a takeover can leave it momentarily logged out even though
// credentials.json exists. Recall checks this, not just LoggedIn.
func (m *Manager) SessionActive(ctx context.Context) bool {
	return m.currentUsername(ctx) != ""
}

// ensureSession makes go-librespot hold a live device session before a recall.
// No-op (true) when a session is already active. When a persisted credential
// exists but the session is dead (cold start / dropped AP), it restarts
// go-librespot so it reloads and re-authenticates from the cached credential,
// then waits (bounded) for an active session. Returns false when the box was
// never logged in, or when no session could be re-established within the window.
//
// Validated live on a taigan box: after an AP drop go-librespot logs "loading
// previously persisted zeroconf credentials" then "authenticated AP" with no
// fresh tap, which is exactly what this restart triggers. The one case it cannot
// recover is a credential invalidated by a takeover (Spotify's single-session
// rule); there it returns false and the caller shows the tap-once hint instead of
// looping restarts.
func (m *Manager) ensureSession(ctx context.Context) bool {
	if m.SessionActive(ctx) {
		return true
	}
	if !m.LoggedIn() {
		return false // never logged in: actionable as "tap this speaker once"
	}
	m.logger.Warn("spotify: persisted credential present but no live session; restarting go-librespot to re-auth before recall")
	m.mu.Lock()
	restart := m.runCancel
	m.mu.Unlock()
	if restart != nil {
		restart() // supervise loop relaunches go-librespot, which reloads the credential
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(500 * time.Millisecond):
		}
		if m.SessionActive(ctx) {
			m.logger.Info("spotify: live session re-established for recall")
			return true
		}
	}
	m.logger.Warn("spotify: could not re-establish a live session for recall")
	return false
}

// CanRecall reports whether a Spotify preset recall can proceed: either
// go-librespot holds a LIVE session right now (SessionActive, e.g. the user just
// streamed to the box from their phone) OR a reusable credential is persisted on
// disk (LoggedIn, so ensureSession can restart go-librespot and re-auth from it).
//
// Recall must gate on this, NOT on LoggedIn alone. A box with a live-but-never-
// persisted zeroconf session (go-librespot authenticated the phone but wrote no
// credential to state.json) reports LoggedIn()==false yet plays Spotify fine;
// gating on LoggedIn alone refused recall on exactly such a box (Patrick, ST10
// rhino, 2026-06-24: streamed Spotify, go-librespot running, box flipped to
// source=SPOTIFY, yet the recall bailed "speaker not logged into Spotify"). Only
// when BOTH are false is the recall genuinely impossible, so the "tap this
// speaker in Spotify once" hint is correct. PlayAccount->ensureSession then
// handles the live vs cold-restart decision from here.
func (m *Manager) CanRecall(ctx context.Context) bool {
	return m.SessionActive(ctx) || m.LoggedIn()
}

// PlayAccount switches to the preset's account (if needed) then plays the URI
// with the given options (shuffle vs resume). This is the recall entry point
// used by both the hardware-button and the desktop/API paths, so both honour
// the preset's shuffle flag and the per-context resume point identically.
func (m *Manager) PlayAccount(ctx context.Context, uri, account string, opts PlayOptions) error {
	// Diagnostic: log the live session state at the recall boundary so a bundle
	// disambiguates "never logged in" vs "dead session" vs "playing fine" without
	// guesswork (every Spotify-recall investigation hit this blind spot).
	m.logger.Info("spotify: recall start", "uri", uri, "wantAccount", account,
		"sessionUser", m.currentUsername(ctx), "loggedIn", m.LoggedIn())
	if account != "" {
		if _, err := m.SwitchAccount(ctx, account); err != nil {
			m.logger.Warn("spotify: account switch failed, playing with current account", "account", account, "err", err)
		}
	}
	// Even with no account switch (single-account / already-active case), make sure
	// go-librespot actually holds a live session before /player/play. Otherwise the
	// box buffers forever and detaches: the "recall finds an empty account" failure
	// that works on a box with a live session but fails on one whose session went
	// cold. ensureSession restarts go-librespot to reload the persisted credential
	// for the cold/dropped case; a box that is genuinely not logged in (or whose
	// credential a takeover invalidated) yields ErrNoSpotifySession so the caller
	// shows the tap-once hint instead of silently playing nothing.
	if !m.ensureSession(ctx) {
		return ErrNoSpotifySession
	}
	return m.Play(ctx, uri, opts)
}

// noteResume records the current track as the resume point for the current
// context, so a later default (non-shuffle) recall of that context continues on
// it. No-op during an in-flight recall (the track is still settling) and when
// the context or track URI is unknown. Called after every metadata/status
// update; the resume store itself ignores unchanged tracks, so this is cheap.
func (m *Manager) noteResume() {
	if m.resume == nil {
		return
	}
	// Take the recall-in-flight check and the context/track snapshot under one
	// lock, so the pair recorded is exactly the state at this instant (no
	// recheck-then-relock window). note() ignores an empty or non-spotify pair.
	m.mu.Lock()
	if time.Now().Before(m.recallUntil) {
		m.mu.Unlock()
		return
	}
	ctxURI, trackURI := m.lastContext, m.curTrackURI
	m.mu.Unlock()
	if ctxURI == "" || trackURI == "" {
		return
	}
	m.resume.note(ctxURI, trackURI)
}

// SetRecalling marks a preset recall as in progress for the next few seconds, so
// ServeOgg does not resume the old (mid-position) track when the box attaches;
// Play drives the new track from its start instead. Called at the very start of
// a recall, before the box attaches.
func (m *Manager) SetRecalling() {
	m.mu.Lock()
	now := time.Now()
	m.recallUntil = now.Add(8 * time.Second)
	// Keep the engine playing across the whole recall + verify window (the
	// hardware verify re-points up to ~25 s after the press). A shorter window
	// than recallUntil would let the drain pause the engine mid-flap and strand
	// it again. See engineHotUntil.
	if t := now.Add(30 * time.Second); t.After(m.engineHotUntil) {
		m.engineHotUntil = t
	}
	m.mu.Unlock()
}

// engineHot reports whether the drain should keep go-librespot playing even
// with no box sink attached (a recall is in flight; the box may be flapping
// through its own 1036 teardown and will re-attach shortly).
func (m *Manager) engineHot() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return time.Now().Before(m.engineHotUntil)
}

// SuppressActivate holds maybeActivate/repointBox off for d, so STR does not
// auto-repoint the box at the Spotify stream during a window where a different,
// deliberate recovery is in flight. The hardware-skip recovery uses it: when the
// box runs its own failed native skip it tears the UPnP source down while
// go-librespot is still playing, which would otherwise trip maybeActivate into
// re-pointing the box at the slot-less stream and racing the clean slot recall
// (box then attaches mid-restart and wedges on a 3102 decoder error). Extends the
// window rather than shrinking it, so it never cuts an existing suppression short.
func (m *Manager) SuppressActivate(d time.Duration) {
	m.mu.Lock()
	if t := time.Now().Add(d); t.After(m.suppressActivateUntil) {
		m.suppressActivateUntil = t
	}
	m.mu.Unlock()
}

// recalling reports whether a recall is currently in progress.
func (m *Manager) recalling() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return time.Now().Before(m.recallUntil)
}

// recallRestartedRecently reports whether a cross-account SwitchAccount restarted
// go-librespot within the current recall window. ServeOgg uses it to resume the
// engine on a re-attach only for that case (the restart gap leaves it paused),
// while a same-account preset switch defers to Play and does not replay the old
// track. Scoped to the recall window so a stale restart never triggers it.
func (m *Manager) recallRestartedRecently() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.recallRestartAt.IsZero() && time.Since(m.recallRestartAt) < 10*time.Second
}

// SetOnActivate wires the callback that points the box at the Spotify stream
// when the user starts playback from the Spotify app while the box is on
// another source (#14).
// SetGroupSlaveIPsFn wires a provider for the LAN IPs of the multiroom
// followers this box leads, so a Spotify Connect volume change is mirrored to
// the whole group, not just the master.
func (m *Manager) SetGroupSlaveIPsFn(fn func() []string) {
	m.mu.Lock()
	m.groupSlaveIPsFn = fn
	m.mu.Unlock()
}

func (m *Manager) SetOnActivate(f func(context.Context)) {
	m.mu.Lock()
	m.onActivate = f
	m.mu.Unlock()
}

// maybeActivate fires onActivate when go-librespot has become active/playing but
// no box is attached to the Ogg stream (the box is on another source). Debounced
// so a burst of events triggers at most one box switch. No-op when a box is
// already attached (e.g. a normal preset recall already pointed it here).
func (m *Manager) maybeActivate() {
	m.mu.Lock()
	cb := m.onActivate
	if cb == nil || m.sink != nil || time.Since(m.lastActivate) < 5*time.Second ||
		time.Now().Before(m.suppressActivateUntil) {
		m.mu.Unlock()
		return
	}
	m.lastActivate = time.Now()
	m.mu.Unlock()
	m.logger.Info("spotify: app playback detected with box on another source, switching box to Spotify stream")
	go cb(context.Background())
}

// repointBox re-points the box at the Spotify stream even if it is already
// attached, so a playlist switch from the app flushes the box buffer and plays
// the new stream promptly. Debounced and shares lastActivate with maybeActivate.
func (m *Manager) repointBox() {
	m.mu.Lock()
	cb := m.onActivate
	if cb == nil || time.Since(m.lastActivate) < 5*time.Second ||
		time.Now().Before(m.suppressActivateUntil) {
		m.mu.Unlock()
		return
	}
	m.lastActivate = time.Now()
	// Keep the engine playing across the re-point, exactly as SetRecalling does
	// for a preset recall. The UPnP push below makes the box tear its current
	// Ogg fetch down and open a new one; for the ~1 s in between there is no
	// sink, and without this the drain takes its "no consumer" branch and
	// PAUSES go-librespot. The box then re-attaches to a paused engine, gets
	// the previous track's headers and no audio, and the Bose transport gives
	// up 30 s later with AUDIO_ERROR_BAD_URL. That is the "Spotify stops after
	// about half an hour" report: half an hour is simply how long an album
	// runs before Spotify moves to the next context and triggers this path
	// (field bundle 2026-07-27, seven boxes, every occurrence). PR #454 closed
	// the identical hole for hardware presets and missed this one.
	if t := m.lastActivate.Add(30 * time.Second); t.After(m.engineHotUntil) {
		m.engineHotUntil = t
	}
	m.mu.Unlock()
	m.logger.Info("spotify: playlist context changed, re-pointing box to play the new stream")
	go cb(context.Background())
}
