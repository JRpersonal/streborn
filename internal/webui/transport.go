// Pause, resume and stop transport endpoints.

package webui

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/upnp"
)

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if s.renderer == nil {
		http.Error(w, "renderer not configured", http.StatusServiceUnavailable)
		return
	}
	// A pause is also a deliberate "stop pulling" intent: the box stops reading
	// from the proxy, which fires the same disconnect path. Suppress the resume.
	s.NoteUserStop()
	s.NoteExplicitStop()
	// Where the track stands, read BEFORE the pause so a resume has somewhere to
	// go back to. A speaker that has thrown its source away cannot be asked
	// afterwards, and radio simply reports nothing, which stores a zero and
	// disables the seek. See repairResume.
	s.notePausePosition(r.Context())
	// A source the speaker plays ITSELF (a native radio station) does not run on
	// the UPnP transport, so a UPnP Pause would succeed against an idle
	// transport and the music would carry on. See transportsource.go.
	if s.transportKeyFallback(r.Context(), "PAUSE") {
		writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
		return
	}
	if err := s.renderer.Pause(r.Context()); err != nil {
		// Pausing while the speaker is idle makes the box answer with a
		// UPnP "Action request came in wrong state" fault. Pause is an
		// idempotent intent: if there is nothing playing the desired
		// state already holds, so treat it as a no-op instead of
		// surfacing a raw SOAP fault to the user.
		if isWrongTransportState(err) {
			writeJSON(w, http.StatusOK, map[string]string{"status": "not_playing"})
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

// handleResume resumes playback from a UPnP PAUSED state with a plain
// AVTransport Play (what the Bose remote's own play/pause does), so a paused
// network-library track continues from its position instead of restarting.
// /api/play always re-pushes SetAVTransportURI, which restarts a finite track,
// and Pause/Stop were the only transport controls STR exposed, so a paused NAS
// track could not be resumed from the app (#202). If the box is no longer
// paused (it left PAUSED after a standby/timeout, surfacing as a "wrong state"
// fault), fall back to re-pushing the last stream.
func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if s.renderer == nil {
		http.Error(w, "renderer not configured", http.StatusServiceUnavailable)
		return
	}
	status, err := s.resumePlayback(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// Accepting the Play is not the same as coming back. Measured on a Portable
	// (2026-08-11) with a paused network-library track: Pause reached
	// PAUSE_STATE, the Play answered without an error, and the speaker went to
	// INVALID_SOURCE and stayed silent. Nothing in the old handler could tell,
	// because it reported what the SOAP call returned and never asked the box.
	// So the answer goes out now and the box is watched afterwards.
	if status == "playing" {
		s.watchResume()
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}

// resumePlayback issues the resume itself and reports the status for the reply.
// Split out so the settle check below runs with boxCmdMu RELEASED: the repair it
// may trigger recalls a preset slot, which takes that same lock.
func (s *Server) resumePlayback(ctx context.Context) (string, error) {
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	// A user-initiated resume cancels the deliberate-stop intent so the guarded
	// auto-re-push is allowed again for the rest of the session; it also anchors
	// the standby-flip discriminator like any other explicit play (#419).
	s.NoteUserPlay()
	// A native station paused by a remote key resumes with one too: the UPnP
	// Play would start STR's own idle transport instead, which on some chassis
	// yanks the box off the station it was paused on.
	if s.transportKeyFallback(ctx, "PLAY") {
		return "playing", nil
	}
	if err := s.renderer.Play(ctx); err != nil {
		if isWrongTransportState(err) {
			// The box is no longer in PAUSED. Re-push the last stream so the user
			// still gets audio (from the start for a finite track).
			s.lastPlayMu.Lock()
			lp := s.lastPlay
			s.lastPlayMu.Unlock()
			if lp != nil {
				if perr := s.renderer.PlayURLMime(ctx, lp.boxURL, lp.title, lp.art, lp.mime); perr == nil {
					return "playing", nil
				}
			}
			return "not_playing", nil
		}
		return "", err
	}
	return "playing", nil
}

// How long the speaker gets to come back after a resume before STR puts the
// content back itself. Long enough to cover a slow re-attach and the lag in
// now_playing (it trails the real state by a second or so), short enough that a
// listener who pressed play is not left in silence wondering.
// Variables, not constants, so the tests can shrink the window instead of
// sleeping through it.
var (
	resumeSettleWindow = 6 * time.Second
	resumeSettleStep   = 750 * time.Millisecond

	// Seams, same reason as in hushforupload.go: the real reader reaches the
	// firmware on a fixed port, so without them every test case would take the
	// "unreadable" path and assert nothing.
	resumeReadNowPlaying = func(ctx context.Context, host string) nowPlayingSnapshot {
		return fetchNowPlaying(ctx, host)
	}
	resumeRepair = func(ctx context.Context, s *Server) { s.repairResume(ctx) }
)

// watchResume polls the speaker after a resume and repairs it if it never
// starts playing. Runs in the background so the caller's request is not held
// open for the settle window.
//
// The verdict is the state at the END of the window, not the first sign of
// life. A speaker that fails this way does not stay silent: it accepts the
// play, reports PLAY_STATE for a second or two, and only then drops to
// INVALID_SOURCE. Standing down on the first playing read therefore missed the
// exact failure it was written for (measured, twice, on a Portable). Waiting
// out the window costs a few seconds and also covers the opposite case, a slow
// re-attach that is still coming up.
func (s *Server) watchResume() {
	startedAt := time.Now()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var last nowPlayingSnapshot
		playingSeen := 0
		for deadline := time.Now().Add(resumeSettleWindow); time.Now().Before(deadline); {
			time.Sleep(resumeSettleStep)
			// The user changed their mind mid-window: their action wins, this
			// one is stale. Deliberately explicitStopAfter and NOT
			// userStoppedRecently: a speaker collapsing out of a resume emits a
			// STOP_STATE, which the broader signal counts as the user stopping,
			// so reading that here would stand down precisely when the repair is
			// needed. Measured on a Portable, and it is why the first version of
			// this watcher did nothing at all.
			if s.explicitStopAfter(startedAt) {
				return
			}
			last = resumeReadNowPlaying(ctx, s.boxHost)
			if isPlayingStatus(last.PlayStatus) {
				playingSeen++
			}
		}
		if isPlayingStatus(last.PlayStatus) {
			return
		}
		s.logger.Warn("resume: the speaker did not hold the play, putting the content back",
			"source", last.Source, "playStatus", last.PlayStatus, "item", last.ItemName,
			"playingReadsBeforeItDropped", playingSeen)
		resumeRepair(ctx, s)
	}()
}

// repairResume puts back what the speaker was playing before the pause, using
// the same route the rest of STR trusts for that kind of content.
//
// A Spotify preset goes through the clean slot recall rather than a bare
// re-point: re-pointing the box at a live Ogg mid-stream hands it a stream with
// no headers where it expects a track boundary, which is the wedge the hardware
// skip path already learned to avoid. Anything else is the URL STR last pushed,
// so a library track continues from its start rather than not at all.
func (s *Server) repairResume(ctx context.Context) {
	s.lastPlayMu.Lock()
	lp := s.lastPlay
	s.lastPlayMu.Unlock()
	if lp == nil || lp.boxURL == "" {
		s.logger.Warn("resume repair: nothing recorded to put back")
		return
	}
	if strings.Contains(lp.boxURL, "spotify/stream") {
		if slot := slotFromSpotifyStreamURL(lp.boxURL); slot > 0 {
			s.logger.Info("resume repair: recalling the Spotify preset cleanly", "slot", slot)
			s.recallSlotClean(ctx, slot)
			return
		}
	}
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	at := s.pausePosition()
	s.logger.Info("resume repair: pushing the last item back", "title", lp.title, "seekTo", at.String())
	if err := s.renderer.PlayURLTrack(ctx, lp.boxURL, lp.title, lp.art, lp.mime, upnp.TrackMeta{
		Duration: s.queueTrackLength(),
		Seekable: isPlainHTTPURL(lp.boxURL) && lp.mime != "",
	}); err != nil {
		s.logger.Warn("resume repair: the re-push failed", "err", err, "title", lp.title)
		return
	}
	// The re-push starts the file from the beginning, which is not what the user
	// asked for: they paused in the middle. Seek back to where they were. Only
	// possible because the item now goes to the box as a finite, seekable file
	// (see upnp.TrackMeta); on anything without a position this is skipped, and a
	// refused seek is not fatal, the track simply plays from its start.
	if at <= 0 {
		return
	}
	if err := s.renderer.Seek(ctx, at); err != nil {
		s.logger.Warn("resume repair: the speaker refused the seek, playing from the start",
			"err", err, "seekTo", at.String())
		return
	}
	s.logger.Info("resume repair: continued where it was paused", "at", at.String())
}

// notePausePosition remembers how far into the current track the speaker was.
// Best-effort: radio and anything the box reports nothing for store a zero,
// which simply means a later repair does not seek.
func (s *Server) notePausePosition(ctx context.Context) {
	if s.renderer == nil {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	rel, _, ok := s.renderer.PositionInfo(rctx)
	s.pausePosMu.Lock()
	if ok {
		s.pausePos = rel
	} else {
		s.pausePos = 0
	}
	s.pausePosMu.Unlock()
}

func (s *Server) pausePosition() time.Duration {
	s.pausePosMu.Lock()
	defer s.pausePosMu.Unlock()
	return s.pausePos
}

// queueTrackLength is the length of the track the queue believes is playing, or
// zero outside a queue.
func (s *Server) queueTrackLength() time.Duration {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	return s.queueTrackDur
}

// isWrongTransportState reports whether a UPnP AVTransport error was the
// box rejecting the action because the renderer is not in a state that
// allows it. Bose answers Pause/Stop with this when nothing is playing,
// using errorCode 501 and the text "Action request came in wrong state"
// (the AVTransport spec also defines 701 for the same situation).
func isWrongTransportState(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "wrong state") ||
		strings.Contains(msg, "<errorCode>501</errorCode>") ||
		strings.Contains(msg, "<errorCode>701</errorCode>")
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if s.renderer == nil {
		http.Error(w, "renderer not configured", http.StatusServiceUnavailable)
		return
	}
	// Mark this as a deliberate stop BEFORE issuing it, so the disconnect the
	// stop triggers does not race the auto-re-push into restarting the stream.
	s.NoteUserStop()
	s.NoteExplicitStop()
	// A stop ends any active library queue (no auto-advance after the user stops).
	s.stopQueue()
	// Same as Pause: a station the speaker fetches itself is not on the UPnP
	// transport, so the stop has to go to the box's own player.
	if s.transportKeyFallback(r.Context(), "STOP") {
		writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
		return
	}
	if err := s.renderer.Stop(r.Context()); err != nil {
		// Same idempotent treatment as Pause: stopping an already-idle
		// box yields a "wrong state" UPnP fault that the user need not
		// see.
		if isWrongTransportState(err) {
			writeJSON(w, http.StatusOK, map[string]string{"status": "not_playing"})
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// Verify the renderer actually stopped: a wedged renderer ACKs Stop yet
	// keeps playing (observed live on a Portable, 2026-07-10 - transport
	// stayed PLAYING while the source machine sat at INVALID_SOURCE). One
	// re-issued Stop, then an honest answer, so callers can escalate (reboot
	// hint) instead of trusting a blind 200.
	if state, ok := s.verifyRendererStopped(r.Context()); !ok {
		s.logger.Warn("stop: renderer ignored Stop and keeps playing (control wedge, a reboot usually clears it)", "transportState", state)
		writeJSON(w, http.StatusOK, map[string]string{"status": "stopped", "renderer": "still-playing"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

// verifyRendererStopped polls the renderer's transport state briefly after a
// Stop, re-issuing the Stop once if it still reports PLAYING. Returns the
// last observed state and whether the renderer left PLAYING. Best-effort: an
// unreadable state counts as stopped (no false alarms on boxes whose
// GetTransportInfo is flaky).
func (s *Server) verifyRendererStopped(ctx context.Context) (string, bool) {
	retried := false
	state := ""
	for i := 0; i < 4; i++ {
		time.Sleep(600 * time.Millisecond)
		tctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		st, err := s.renderer.TransportState(tctx)
		cancel()
		if err != nil {
			return state, true
		}
		state = st
		if st != "PLAYING" {
			return st, true
		}
		if !retried {
			retried = true
			sctx, cancel := context.WithTimeout(ctx, 4*time.Second)
			_ = s.renderer.Stop(sctx)
			cancel()
		}
	}
	return state, false
}
