// titleswap.go: the HTTP side of the per-station "artist and title are the
// wrong way round" flag. The flag itself and the swap live in the stream
// proxy (internal/streamproxy/titleswap.go), where the StreamTitle is parsed,
// so every consumer of the live title gets the corrected one.

package webui

import (
	"errors"
	"net/http"

	"github.com/JRpersonal/streborn/internal/streamproxy"
)

// handleTitleSwap reads and sets the flag.
//
// GET  -> {"stations":[...],"swapped":bool}  (swapped: the station playing now)
// POST {"swap":true|false,"station":"<optional stream url>"}
//
//	-> {"ok":true,"station":"...","swapped":bool,"title":"<live title now>"}
//
// Without "station" the POST acts on the station being proxied right now,
// which is what the app's toggle does; 409 when nothing is.
func (s *Server) handleTitleSwap(w http.ResponseWriter, r *http.Request) {
	if s.streamProxy == nil {
		http.Error(w, "stream proxy not available", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"stations": s.streamProxy.TitleSwapStations(),
			"swapped":  s.streamProxy.TitleSwapped(""),
		})
	case http.MethodPost:
		if !isLocalLAN(r.RemoteAddr) {
			http.Error(w, "only allowed from LAN", http.StatusForbidden)
			return
		}
		var in struct {
			Swap    bool   `json:"swap"`
			Station string `json:"station"`
		}
		if !decodeJSONRequest(w, r, 4<<10, &in) {
			return
		}
		station, title, err := s.streamProxy.SetTitleSwap(in.Station, in.Swap)
		switch {
		case errors.Is(err, streamproxy.ErrTitleSwapNoStation):
			http.Error(w, err.Error(), http.StatusConflict)
			return
		case errors.Is(err, streamproxy.ErrTitleSwapBadStation):
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		case errors.Is(err, streamproxy.ErrTitleSwapFull):
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		case err != nil:
			s.logger.Warn("title swap not stored", "station", station, "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"station": station,
			"swapped": in.Swap,
			"title":   title,
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
