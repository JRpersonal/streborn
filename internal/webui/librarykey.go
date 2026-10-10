package webui

import (
	"context"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
)

// A single library track on a preset key (#1065).
//
// A track saved from the Library tab is stored as a radio-shaped preset whose
// StreamURL is the file on the LAN media server. The app has always recalled it
// by handing the speaker the file directly (#139). The speaker's own key did
// not: the key holds the slot as a native radio station, so the firmware
// fetched the file through the radio stream proxy like a live station.
//
// Measured on an ST10 (FW 27.0.6, 2026-10-06, an ALAC .m4a on MinimServer):
// the firmware's radio path opens the file, drops the connection after a few
// hundred KB and comes back with "Range: bytes=32768-". The proxy is built for
// live streams and treats the server's 206 answer as a failure, so the speaker
// logged AUDIO_ERROR_BAD_URL, went amber and gave up on every press. The same
// file started from the app played at once, and album and playlist keys were
// fine, because those are queue presets that RecallSlot already claims.
//
// So a single library track is claimed the same way: the press plays the file
// directly, exactly like the app's recall of the same key.

// libraryFileMime returns the MIME a library-track preset is played with when
// it is handed to the speaker directly, or "" when p is not one. A library
// track is a radio-shaped preset saved from the Library tab (Source names the
// media server) whose URL is a plain-HTTP file with a known audio extension.
func libraryFileMime(p presets.Preset) string {
	switch strings.ToLower(strings.TrimSpace(p.Type)) {
	case "spotify", "queue", presets.TypeNative:
		return ""
	}
	if p.Source == "" || !isPlainHTTPURL(p.StreamURL) {
		return ""
	}
	return mimeFromURL(p.StreamURL)
}

// playLibraryFilePresetLocked hands a library-track preset to the speaker as
// the file itself, records it as the last play and starts the recall verify.
// The caller holds boxCmdMu.
func (s *Server) playLibraryFilePresetLocked(ctx context.Context, p presets.Preset, mime string, recallStart time.Time) error {
	directURL := p.StreamURL
	if err := s.pushOverNativeStation(ctx, p.Name, func() error {
		return s.renderer.PlayURLMime(ctx, directURL, p.Name, p.Art, mime)
	}); err != nil {
		return err
	}
	gen := s.setLastPlay(directURL, p.Name, p.Art, mime)
	name, art := p.Name, p.Art
	go s.verifyRecall(gen, recallStart, directURL, func(ctx context.Context, _ bool) {
		_ = s.renderer.PlayURLMime(ctx, directURL, name, art, mime)
	}, nil)
	// A finite file has no end event from the firmware: it freezes in
	// PLAY_STATE at the end (#380), so a track recalled from a key, from the
	// app or from the speaker's own button, kept showing as playing until the
	// auto-off timer (#1031). Give it the same end watch the plain play arms
	// (#844). A preset stores no length, so dur stays 0 and the watch takes
	// the length the speaker reports, or falls back to the frozen position.
	// The verify's re-push above does not bump the recall generation, so it
	// does not call the watch off.
	s.armSingleTrackEnd(singleTrack{
		boxURL: directURL, title: name, art: art, mime: mime,
	}, gen)
	return nil
}

// recallLibraryFileSlot is RecallSlot's branch for a single library track. It
// reports whether it claimed the press; a slot that holds anything else is
// left to the caller.
func (s *Server) recallLibraryFileSlot(ctx context.Context, slot int, p presets.Preset) bool {
	mime := libraryFileMime(p)
	if mime == "" || s.renderer == nil {
		return false
	}
	// The press supersedes any verify still running from an earlier one, and a
	// library queue that is still playing must not advance over this track
	// later (the same reasons NoteLastPlay gives for the radio keys).
	s.bumpRecallGen()
	s.stopQueue("a hardware preset key was pressed")
	s.ensureBoxReady(ctx)
	recallStart := time.Now()
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	s.logger.Info("preset slot recall (hardware): library track, playing the file directly",
		"slot", slot, "mime", mime)
	if err := s.playLibraryFilePresetLocked(ctx, p, mime, recallStart); err != nil {
		s.logger.Warn("hardware library track recall failed", "slot", slot, "err", err)
	}
	return true
}
