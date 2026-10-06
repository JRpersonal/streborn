package webui

import (
	"encoding/json"
	"io"
	"net/http"
	"os/exec"

	"github.com/JRpersonal/streborn/internal/oled"
)

// handleDisplaySplash reads and sets the per-speaker switch for STR's logo
// animation on the Portable's OLED (internal/oled). GET answers
// {enabled, supported} (supported: the panel is one the splash can draw on,
// so the app shows the setting); POST {"enabled":bool} persists it. On by
// default.
func (s *Server) handleDisplaySplash(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"enabled": oled.Enabled(), "supported": oled.Supported()})
	case http.MethodPost:
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil || body.Enabled == nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		val := "0"
		if *body.Enabled {
			val = "1"
		}
		if err := persistFlagFile(oled.FlagPath, val); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = exec.Command("sync").Run()
		s.logger.Info("display splash set", "enabled", *body.Enabled)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": *body.Enabled})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
