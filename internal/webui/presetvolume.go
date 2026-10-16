// presetvolume.go: a key that plays at its own volume (#1323).
//
// Asked for as "a separate volume per preset, set on its own and kept after
// switching on": the news station on key 1 quiet, the music on key 2 louder,
// without reaching for the knob each time. The level is stored with the
// preset (presets.Preset.Volume), so it is on the speaker's NAND and survives
// a power cycle like the key itself.
//
// It is applied once per press, after the key has started playing. Not
// before: the firmware settles its own volume while a source comes up, and a
// level written into that window is overwritten by the box (the lesson
// startvolume.go already learned). A level the user sets while the key plays
// is theirs; nothing here touches it again until the next press.
//
// Off by default: a key without a stored level leaves the volume alone, which
// is what every key did before this.

package webui

import (
	"context"
	"net/http"
	"time"
)

// Watch timing for the per-key volume. Vars so tests can shorten them.
var (
	// presetVolumeWatchFor is how long a press may take to start playing. A
	// recall that wakes the speaker from standby can take 20 s and more.
	presetVolumeWatchFor  = 30 * time.Second
	presetVolumeWatchPoll = 500 * time.Millisecond
	// presetVolumeSettle is the wait after playback starts before the level
	// is written, and again before it is checked.
	presetVolumeSettle = startVolumeApplyDelay
	// presetVolumeRecheck is when the level is read back once: a firmware
	// that settles its source late puts its own level back, and then the
	// key's level is written a second time.
	presetVolumeRecheck = 3 * time.Second
)

// ApplyPresetVolume sets the speaker to the level stored with slot once the
// key plays. why names the path that recalled the key, for the log. A no-op
// for a key without a stored level.
func (s *Server) ApplyPresetVolume(slot int, why string) {
	if s.presets == nil {
		return
	}
	p, ok := s.presets.Get(slot)
	if !ok || p.Volume <= 0 {
		return
	}
	if s.boxHost == "" && s.setVolumeFn == nil {
		return
	}
	vol := p.Volume
	seq := s.presetVolumeSeq.Add(1)
	s.logger.Info("preset volume: waiting for the key to play", "slot", slot, "vol", vol, "why", why)
	go s.watchPresetVolume(seq, slot, vol, why)
}

func (s *Server) watchPresetVolume(seq uint64, slot, vol int, why string) {
	superseded := func() bool {
		if s.presetVolumeSeq.Load() == seq {
			return false
		}
		s.logger.Info("preset volume: a newer key press took over, standing down", "slot", slot)
		return true
	}
	deadline := time.Now().Add(presetVolumeWatchFor)
	for {
		time.Sleep(presetVolumeWatchPoll)
		if superseded() {
			return
		}
		if standby, busy := s.boxPlayState(); busy && !standby {
			break
		}
		if time.Now().After(deadline) {
			s.logger.Info("preset volume: the key did not start playing, volume left alone", "slot", slot, "vol", vol)
			return
		}
	}
	time.Sleep(presetVolumeSettle)
	if superseded() {
		return
	}
	before, haveBefore := s.readPresetVolumeLevel()
	if err := s.writePresetVolumeLevel(vol); err != nil {
		s.logger.Warn("preset volume: could not set the level", "slot", slot, "vol", vol, "err", err)
		return
	}
	s.logger.Info("preset volume applied", "slot", slot, "vol", vol, "was", before, "why", why)
	time.Sleep(presetVolumeRecheck)
	if superseded() {
		return
	}
	now, ok := s.readPresetVolumeLevel()
	// Only the firmware putting its own level back is undone: back at the
	// level from before the write. Anything else is somebody at the knob.
	if !ok || now == vol || !haveBefore || now != before {
		return
	}
	if err := s.writePresetVolumeLevel(vol); err != nil {
		s.logger.Warn("preset volume: could not set the level again", "slot", slot, "vol", vol, "err", err)
		return
	}
	s.logger.Info("preset volume applied again, the speaker had put its own level back",
		"slot", slot, "vol", vol, "speakerLevel", now)
}

func (s *Server) writePresetVolumeLevel(vol int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if s.setVolumeFn != nil {
		return s.setVolumeFn(ctx, vol)
	}
	return levelsClient(s.boxHost).SetVolume(ctx, vol)
}

func (s *Server) readPresetVolumeLevel() (int, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if s.getVolumeFn != nil {
		v, err := s.getVolumeFn(ctx)
		return v, err == nil
	}
	if s.boxHost == "" {
		return 0, false
	}
	v, err := levelsClient(s.boxHost).GetVolume(ctx)
	if err != nil {
		return 0, false
	}
	return v.Target, true
}

// handlePresetVolume stores the level of an existing key: the "volume" half
// of PATCH /api/presets/<slot>. 0 switches it off. Nothing is written to the
// speaker: the level lives in STR's store and is applied by STR on a press.
func (s *Server) handlePresetVolume(w http.ResponseWriter, r *http.Request, slot, vol int) {
	p, ok, err := s.presets.SetVolume(slot, vol)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "preset not set", http.StatusNotFound)
		return
	}
	s.logger.Info("preset volume set",
		"slot", slot, "vol", p.Volume, "name", p.Name,
		"from", r.RemoteAddr, "ua", r.Header.Get("User-Agent"))
	writeJSON(w, http.StatusOK, p)
}

// presetPatch is the body of PATCH /api/presets/<slot>: a new name, a new
// volume, or both. A field that is absent is left alone.
type presetPatch struct {
	Name   *string `json:"name"`
	Volume *int    `json:"volume"`
}
