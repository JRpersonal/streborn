package webui

import (
	"fmt"
	"net/http"
	"os"
	"strings"
)

// Sticky play mode (shuffle + repeat) for the play queue.
//
// The Zufall/Wiederholen toggles only ever acted on the LIVE queue: the next
// preset press rebuilt the queue with the slot's stored shuffle and repeat
// off, so the choice silently un-ticked itself. Asked for by an owner running
// genre folders off a Synology NAS on keys 2-6 of four speakers (mail,
// 2026-08-20): the mode should stay until it is switched off again.
//
// The last explicitly chosen mode is persisted as a small NAND flag file and
// applied to every queue start that carries no choice of its own (hardware
// preset presses, and app starts from an app that omits the fields). An app
// start that DOES carry explicit values wins and becomes the new sticky mode.
const defaultPlayModePath = "/mnt/nv/streborn/play-mode"

func (s *Server) playModeFilePath() string {
	if s.playModePath != "" {
		return s.playModePath
	}
	return defaultPlayModePath
}

// loadPlayMode reads the persisted mode. ok is false when no choice was ever
// persisted (fresh install), in which case callers keep their old defaults.
func (s *Server) loadPlayMode() (shuffle bool, rep repeatMode, ok bool) {
	b, err := os.ReadFile(s.playModeFilePath())
	if err != nil {
		return false, repeatOff, false
	}
	for _, f := range strings.Fields(string(b)) {
		switch {
		case f == "shuffle=1":
			shuffle = true
		case strings.HasPrefix(f, "repeat="):
			rep = parseRepeat(strings.TrimPrefix(f, "repeat="))
		}
	}
	return shuffle, rep, true
}

// savePlayMode persists the mode. Best-effort: a full NAND must never break
// the play itself, so the error is logged and swallowed.
func (s *Server) savePlayMode(shuffle bool, rep repeatMode) {
	sv := "shuffle=0"
	if shuffle {
		sv = "shuffle=1"
	}
	body := fmt.Sprintf("%s repeat=%s\n", sv, rep.String())
	if err := writeFileAtomic(s.playModeFilePath(), []byte(body)); err != nil {
		s.logger.Warn("play mode: could not persist", "err", err)
	}
}

// handleQueueMode answers GET /api/queue/mode with the sticky mode, so a UI
// can show the real default before any queue is running.
func (s *Server) handleQueueMode(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	shuffle, rep, ok := s.loadPlayMode()
	writeJSON(w, http.StatusOK, map[string]any{
		"shuffle": shuffle,
		"repeat":  rep.String(),
		"stored":  ok,
	})
}
