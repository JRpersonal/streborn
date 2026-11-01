// maxvolume.go: a ceiling the speaker's volume never stays above.
//
// Asked for by a parent on 2026-11-01: a SoundTouch in a child's room that
// should not be turned up to party level. Built the same way as the start
// volume (startvolume.go): one NAND file per speaker, off by default, set from
// the app with a marker on the volume bar.
//
// Two layers, because no single one sees every way the volume goes up:
//
//   - Every volume STR itself writes for a person (the app and phone sliders,
//     the start volume, an alarm, an announcement, the Spotify app) is capped
//     BEFORE it reaches the speaker, so those never get loud even for a moment.
//   - Everything else (the speaker's own keys, the IR remote, the Bose app,
//     another program on the Bose API) is caught afterwards: the speaker
//     reports every change as a volumeUpdated frame, and a level above the
//     ceiling is turned straight back down. Those can be loud for the fraction
//     of a second the round trip takes; nothing on the agent side can stop a
//     key press inside the firmware before it acts.
//
// A STOCK speaker (or one whose agent is down) has no ceiling at all. The
// limit lives with the agent, which is also why it is per speaker.

package webui

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// defaultMaxVolumePath is the NAND file holding the ceiling. Absent, empty or
// 0 means off, which is the default.
const defaultMaxVolumePath = "/mnt/nv/streborn/max-volume"

// maxVolumeRecheck is how long after turning the speaker down the agent looks
// again. A held volume key keeps raising the level while the first write is
// on its way, and the frames for those steps arrive while the write is still
// in flight, so one write is not always the last word.
const maxVolumeRecheck = 400 * time.Millisecond

// maxVolumeRounds bounds the turn-down loop for one burst. A key held for a
// long time starts a new burst when the frames resume, so this only stops a
// speaker that refuses the write from being hammered.
const maxVolumeRounds = 5

// maxVolume returns the configured ceiling and whether it is set at all.
func (s *Server) maxVolume() (int, bool) {
	path := s.maxVolumePath
	if path == "" {
		path = defaultMaxVolumePath
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

// capVolume returns v, or the ceiling when v is above it. Used on every level
// STR writes for a person, so the speaker never goes up first and comes back
// down a moment later.
func (s *Server) capVolume(v int) int {
	if limit, ok := s.maxVolume(); ok && v > limit {
		return limit
	}
	return v
}

// MaxVolume is capVolume's ceiling for other packages (the Spotify engine
// caps the Spotify app's slider with it). ok=false means no ceiling.
func (s *Server) MaxVolume() (int, bool) { return s.maxVolume() }

// enforceMaxVolume turns the speaker back down when it reported a level above
// the ceiling. Called for every volumeUpdated frame; never blocks.
//
// One turn-down at a time: a held key emits a burst of frames, and starting a
// write for each one would queue a dozen identical writes behind the speaker's
// command lock. Frames that arrive while a write is in flight only update the
// latest level, which the running loop reads when it looks again.
func (s *Server) enforceMaxVolume(level int) {
	limit, ok := s.maxVolume()
	if !ok || level < 0 {
		return
	}
	s.maxVolumeMu.Lock()
	s.maxVolumeLast = level
	if level <= limit || s.maxVolumeBusy || (s.boxHost == "" && s.ownLevelSetFn == nil) {
		s.maxVolumeMu.Unlock()
		return
	}
	s.maxVolumeBusy = true
	s.maxVolumeMu.Unlock()
	go s.turnDownToMax(level, limit)
}

func (s *Server) turnDownToMax(level, limit int) {
	defer func() {
		s.maxVolumeMu.Lock()
		s.maxVolumeBusy = false
		s.maxVolumeMu.Unlock()
	}()
	recheck := s.maxVolumeRecheck
	if recheck == 0 {
		recheck = maxVolumeRecheck
	}
	for round := 0; round < maxVolumeRounds; round++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := s.setOwnLevel(ctx, limit)
		cancel()
		if err != nil {
			s.logger.Warn("max volume: could not turn the speaker back down", "err", err, "level", level, "max", limit)
			return
		}
		if round == 0 {
			s.logger.Info("max volume: turned the speaker back down", "from", level, "to", limit)
		}
		time.Sleep(recheck)
		s.maxVolumeMu.Lock()
		last := s.maxVolumeLast
		s.maxVolumeMu.Unlock()
		if last <= limit {
			return
		}
		level = last
	}
	s.logger.Warn("max volume: the speaker kept going above the ceiling", "level", level, "max", limit, "rounds", maxVolumeRounds)
}

// handleMaxVolume reads or sets the per-box ceiling.
//
// GET  -> {supported: true, volume: N}  (0 = off)
// POST {"volume": N}                     (0 or absent = off, 1..100 = ceiling)
//
// Setting a ceiling below the current level turns the speaker down at once,
// so the setting does what it says the moment it is saved.
func (s *Server) handleMaxVolume(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	path := s.maxVolumePath
	if path == "" {
		path = defaultMaxVolumePath
	}
	switch r.Method {
	case http.MethodGet:
		vol, ok := s.maxVolume()
		if !ok {
			vol = 0
		}
		writeJSON(w, http.StatusOK, map[string]any{"supported": true, "volume": vol})
	case http.MethodPost:
		var body struct {
			Volume int `json:"volume"`
		}
		if !decodeJSONRequest(w, r, 4096, &body) {
			return
		}
		if body.Volume < 0 || body.Volume > 100 {
			http.Error(w, "volume must be 0 (off) or 1..100", http.StatusBadRequest)
			return
		}
		if err := persistFlagFile(path, strconv.Itoa(body.Volume)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = exec.Command("sync").Run()
		s.logger.Info("max volume set", "vol", body.Volume)
		if body.Volume > 0 {
			go s.applyMaxVolumeNow()
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "volume": body.Volume})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// applyMaxVolumeNow reads the speaker's level once and turns it down when a
// freshly saved ceiling is below it. The frames only report changes, so a
// speaker already too loud would otherwise stay that way until somebody
// touched the volume.
func (s *Server) applyMaxVolumeNow() {
	if s.boxHost == "" && s.announceVolumeFn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	level := s.readVolume(ctx)
	if level < 0 {
		s.logger.Warn("max volume: could not read the speaker's level")
		return
	}
	s.enforceMaxVolume(level)
}
