package webui

import (
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"strings"

	"github.com/JRpersonal/streborn/internal/oled"
	"github.com/JRpersonal/streborn/internal/presets"
)

// handleDisplayNotices reads and sets the per-speaker switch for STR's short
// notices on the speaker display (internal/oled notice.go): a saved key, an
// empty key, keys restored after a reboot. GET answers {enabled, supported}
// (supported: the panel is one STR can draw on, so the app shows the
// setting); POST or PUT {"enabled":bool} persists it. On by default. Same
// shape as /api/box/display-splash, its own flag file.
func (s *Server) handleDisplayNotices(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"enabled": oled.NoticesEnabled(), "supported": oled.Supported()})
	case http.MethodPost, http.MethodPut:
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
		if err := persistFlagFile(oled.NoticesFlagPath, val); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = exec.Command("sync").Run()
		s.logger.Info("display notices set", "enabled", *body.Enabled)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": *body.Enabled})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleIdentify answers POST /api/box/identify: "find this speaker". The
// display shows HELLO and the speaker's name with pulsing rings for a few
// seconds. It is the answer to a button the user just pressed, so it shows
// even when the notices are switched off. A speaker without a panel STR can
// draw on answers {"shown":false,"reason":"no display"}, still 200: that is
// an answer, not a failure.
func (s *Server) handleIdentify(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if !oled.Supported() {
		writeJSON(w, http.StatusOK, map[string]any{"shown": false, "reason": "no display"})
		return
	}
	if oled.GameActive() {
		writeJSON(w, http.StatusOK, map[string]any{"shown": false, "reason": "busy"})
		return
	}
	name := s.remoteDisplayName()
	s.logger.Info("identify requested", "name", name, "from", r.RemoteAddr)
	oled.Show(oled.Identify(name))
	writeJSON(w, http.StatusOK, map[string]any{"shown": true})
}

// noticeAppSave puts "SAVED N / FROM STR APP" on the display when a save
// from the app or the phone page put a different item on key slot. A write
// that only renames, re-states, or heals the metadata of what the key
// already held shows nothing, and neither does the internal write of a held
// key (that path shows its own notice, cmd/agent/holdsave.go).
func (s *Server) noticeAppSave(r *http.Request, prev presets.Preset, had bool, now presets.Preset) {
	if r.Header.Get("User-Agent") == holdSaveUA {
		return
	}
	if had && sameKeyItem(prev, now) {
		return
	}
	oled.Show(oled.SavedFromApp(now.Slot))
}

// noticeAppRefused puts "ON KEY M / ALREADY SAVED" on the display for a save
// the one-station-one-key rule refused (#836).
func (s *Server) noticeAppRefused(r *http.Request, otherSlot int) {
	if r.Header.Get("User-Agent") == holdSaveUA {
		return
	}
	oled.Show(oled.AlreadyOnKey(otherSlot))
}

// sameKeyItem reports whether two presets play the same thing, whatever
// their names and artwork say.
func sameKeyItem(a, b presets.Preset) bool {
	if a.IsNative() || b.IsNative() {
		return a.IsNative() && b.IsNative() && presets.SameNativeItem(a.Native, b.Native)
	}
	if !strings.EqualFold(a.Type, b.Type) || a.StreamURL != b.StreamURL || a.URI != b.URI || len(a.Items) != len(b.Items) {
		return false
	}
	for i := range a.Items {
		if a.Items[i].URL != b.Items[i].URL {
			return false
		}
	}
	return true
}
