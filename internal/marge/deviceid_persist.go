package marge

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Remembering the box's confirmed device id across restarts.
//
// The id the agent starts with is a guess taken from a network interface MAC,
// and on a chassis with two interfaces it can name the wrong one. Two live
// sources correct it - the addDevice POST and GET /info - but both arrive
// AFTER the agent starts serving, so there is a window at every boot in which
// the emulated account can name an id the box does not recognise. The firmware
// discards an account it cannot find itself in, and that account is what
// registers the radio source behind the hardware preset buttons.
//
// Persisting the confirmed id closes the window: from the second boot onward
// the very first request is already answered correctly, and the live sources
// only ever confirm it.
//
// The write is deliberately once-per-change, never periodic. A speaker's
// standby countdown is reset by writes, and a timer that rewrote this file
// would be the same mistake that kept boxes awake in v0.9.17.

// WithDeviceIDPath persists the confirmed device id at path and seeds it back
// on the next start. Empty (the default) keeps everything in memory.
func WithDeviceIDPath(path string) Option {
	return func(s *Server) { s.deviceIDPath = path }
}

// loadDeviceID seeds the device id from the last confirmed value. Called from
// New after the options are applied, so a stored id supersedes the MAC guess
// while still being replaced by anything the box states about itself.
func (s *Server) loadDeviceID() {
	if s.deviceIDPath == "" {
		return
	}
	data, err := os.ReadFile(s.deviceIDPath)
	if err != nil {
		return
	}
	id := ValidDeviceID(string(data))
	if id == "" {
		s.logger.Warn("marge: the stored deviceID is unusable, falling back to the interface guess",
			slog.String("comp", "marge"), slog.String("path", s.deviceIDPath))
		return
	}
	s.mu.Lock()
	prev := s.deviceID
	s.deviceID = id
	s.mu.Unlock()
	if prev != "" && prev != id {
		s.logger.Warn("marge: using the deviceID the box confirmed previously, not the interface guess",
			slog.String("comp", "marge"), slog.String("deviceID", id), slog.String("guess", prev))
	}
}

// persistDeviceID stores a confirmed id. Best effort: a speaker where the write
// fails simply relearns the id from the box on every boot, exactly as it does
// without persistence at all.
func (s *Server) persistDeviceID(id string) {
	if s.deviceIDPath == "" || id == "" {
		return
	}
	if dir := filepath.Dir(s.deviceIDPath); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	if err := os.WriteFile(s.deviceIDPath, []byte(strings.ToUpper(id)+"\n"), 0o644); err != nil {
		s.logger.Warn("marge: could not store the confirmed deviceID (it will be relearned next boot)",
			slog.String("comp", "marge"), slog.String("err", err.Error()))
	}
}
