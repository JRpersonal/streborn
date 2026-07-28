package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// positionTimeout bounds the AVTransport query. The progress bar is polled
// every couple of seconds, so a slow box must fail fast and leave the bar
// where it is rather than stack requests.
const positionTimeout = 3 * time.Second

// handlePosition reports where the speaker is inside the current track, for
// the progress bar in the app and on the phone remote (#399).
//
// Position comes from the renderer's own AVTransport, which is the only party
// that knows: STR pushes a stream and the box decodes it, so the box's clock is
// the truth. Radio has no end, and the box then reports a duration of zero;
// that is not an error, it is the "position known, length unknown" case, and
// the UI shows elapsed time without a bar. A firmware that does not implement
// the query at all answers ok=false and the UI shows nothing, exactly as before.
func (s *Server) handlePosition(w http.ResponseWriter, r *http.Request) {
	if s.renderer == nil {
		http.Error(w, "no renderer", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), positionTimeout)
	defer cancel()
	rel, dur, ok := s.renderer.PositionInfo(ctx)
	out := map[string]any{
		"ok":          ok,
		"positionSec": int(rel.Seconds()),
		"durationSec": int(dur.Seconds()),
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}
