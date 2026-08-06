// Pause, resume and stop transport endpoints.

package webui

import (
	"context"
	"net/http"
	"strings"
	"time"
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
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	if s.renderer == nil {
		http.Error(w, "renderer not configured", http.StatusServiceUnavailable)
		return
	}
	// A user-initiated resume cancels the deliberate-stop intent so the guarded
	// auto-re-push is allowed again for the rest of the session; it also anchors
	// the standby-flip discriminator like any other explicit play (#419).
	s.NoteUserPlay()
	// A native station paused by a remote key resumes with one too: the UPnP
	// Play would start STR's own idle transport instead, which on some chassis
	// yanks the box off the station it was paused on.
	if s.transportKeyFallback(r.Context(), "PLAY") {
		writeJSON(w, http.StatusOK, map[string]string{"status": "playing"})
		return
	}
	if err := s.renderer.Play(r.Context()); err != nil {
		if isWrongTransportState(err) {
			// The box is no longer in PAUSED. Re-push the last stream so the user
			// still gets audio (from the start for a finite track).
			s.lastPlayMu.Lock()
			lp := s.lastPlay
			s.lastPlayMu.Unlock()
			if lp != nil {
				if perr := s.renderer.PlayURLMime(r.Context(), lp.boxURL, lp.title, lp.art, lp.mime); perr == nil {
					writeJSON(w, http.StatusOK, map[string]string{"status": "playing"})
					return
				}
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "not_playing"})
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "playing"})
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
