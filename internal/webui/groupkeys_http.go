package webui

import (
	"net/http"

	"github.com/JRpersonal/streborn/internal/groupkeys"
)

// handleGroupKeys serves the per-speaker group-key document (#863): the saved
// group templates and which thumbs key forms which of them.
//
// GET /api/groupkeys -> the whole document.
// PUT /api/groupkeys <- the whole document; validated, then persisted on NAND.
//
// LAN only, like every other write to the speaker's configuration. The PUT
// replaces the document wholesale (the app always sends all of it), which is
// the one NAND write this feature has; a key press never writes.
func (s *Server) handleGroupKeys(w http.ResponseWriter, r *http.Request) {
	if s.groupKeys == nil {
		http.Error(w, "group keys not configured", http.StatusServiceUnavailable)
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.groupKeys.Get())
	case http.MethodPut:
		var d groupkeys.Document
		if !decodeJSONRequest(w, r, 1<<16, &d) {
			return
		}
		if err := d.Validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.groupKeys.Set(d); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.logger.Info("group keys: saved from the app", "templates", len(d.Templates), "bindings", len(d.Bindings))
		writeJSON(w, http.StatusOK, s.groupKeys.Get())
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
