// startvolume.go: a fixed level the speaker returns to when it starts playing
// after a rest.
//
// Asked for by a Portable and ST20 owner on 2026-09-26. His speakers do not
// keep their level over a power cycle, so every morning starts at whatever the
// firmware happened to keep. Mine do keep it, which is why I first told him it
// was not a problem; he was right and I was wrong.
//
// Deliberately narrow. It applies to the automatic resume after the speaker was
// off or asleep, and nowhere else: a volume the user set while the music plays
// is theirs and must never be overwritten. Off by default, because a speaker
// that quietly changes its own volume is worse than one that forgets.

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// defaultStartVolumePath is the NAND file holding the level. Absent or empty
// means off, which is the default.
const defaultStartVolumePath = "/mnt/nv/streborn/start-volume"

// startVolumeApplyDelay is how long after playback starts the level is set.
//
// Not before: the firmware settles its own volume as the source comes up, and
// a value written into that window is overwritten by the box itself. This is
// the same lesson the fleet tests learned the hard way, where a volume set
// before play simply did not stick.
const startVolumeApplyDelay = 1200 * time.Millisecond

// startVolume returns the configured level and whether it is set at all.
func (s *Server) startVolume() (int, bool) {
	path := s.startVolumePath
	if path == "" {
		path = defaultStartVolumePath
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || v < 1 || v > 100 {
		// 0 is the honest way to store "off", and anything out of range is a
		// damaged file rather than an instruction.
		return 0, false
	}
	return v, true
}

// applyStartVolume sets the configured level shortly after an automatic resume
// has started playing. A no-op when the feature is off, which is the default.
//
// Called only from the wake/resume paths. It must never run on a play the user
// started, because then it would fight their own volume knob.
func (s *Server) applyStartVolume(reason string) {
	vol, ok := s.startVolume()
	if !ok || s.boxHost == "" {
		return
	}
	go func() {
		time.Sleep(startVolumeApplyDelay)
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		if err := levelsClient(s.boxHost).SetVolume(ctx, vol); err != nil {
			s.logger.Warn("start volume: could not set the level", "err", err, "vol", vol, "reason", reason)
			return
		}
		s.logger.Info("start volume: set the speaker to its start level", "vol", vol, "reason", reason)
	}()
}

// handleStartVolume reads or sets the per-box start level.
//
// GET  -> {supported: true, volume: N}  (0 = off)
// POST {"volume": N}                     (0 or absent = off, 1..100 = level)
func (s *Server) handleStartVolume(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	path := s.startVolumePath
	if path == "" {
		path = defaultStartVolumePath
	}
	switch r.Method {
	case http.MethodGet:
		vol, ok := s.startVolume()
		if !ok {
			vol = 0
		}
		writeJSON(w, http.StatusOK, map[string]any{"supported": true, "volume": vol})
	case http.MethodPost:
		var body struct {
			Volume int `json:"volume"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Volume < 0 || body.Volume > 100 {
			http.Error(w, "volume must be 0 (off) or 1..100", http.StatusBadRequest)
			return
		}
		if err := persistFlagFile(path, strconv.Itoa(body.Volume)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = exec.Command("sync").Run()
		s.logger.Info("start volume set", "vol", body.Volume)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "volume": body.Volume})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
