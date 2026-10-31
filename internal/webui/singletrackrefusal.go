package webui

import "time"

// A library track the speaker dropped without even trying it.
//
// #1634 (ST10, v1.2.1): every track clicked in the desktop app's Library ended
// with "did not start on the speaker, maybe a file format it cannot play". The
// speaker's own log said otherwise. It answered SetAVTransportURI and Play with
// success, then sat in "HandleUpnpPlay() >> InactiveSelected" ("SourceSelect
// already sent"), never fetched the file and never logged a playback failure,
// and every wake in the same hour drew 1036 UpnpRcvdContentItemInWrongState.
// That is the UPnP refusal a soft restart clears (wedge.go), the one the app's
// restart banner exists for. But only an exhausted PRESET recall counted toward
// that banner; a lone library track ended its watch with a log line and
// nothing else, so a speaker refusing everything STR pushed looked, track after
// track, like a pile of files in the wrong format.
//
// So a lone track that was dropped without playing now counts as a refused
// push, in the same latch as the recall refusals, when the speaker gives the
// two signs that it never tried the file: it rejected a source as not signed
// in during the play, or its log is being read and holds no playback failure
// for the play. A file the speaker did try and could not decode logs exactly
// such a failure, so a genuinely unplayable file never counts.

// singleTrackRefusalSlack widens the look-back past the play's own start: the
// push goes out a moment before the watch starts its clock, and a 1036 the
// wake right before the push drew belongs to the same attempt.
const singleTrackRefusalSlack = 5 * time.Second

// SetBoxPlayFailureFn wires the firmware log's playback-failure view (see
// boxPlayFailureFn).
func (s *Server) SetBoxPlayFailureFn(fn func(d time.Duration) (failed, logLive bool)) {
	s.boxPlayFailureFn = fn
}

// noteSingleTrackRefused decides whether a lone library track that the speaker
// dropped (or never started) without playing it counts as a refused push, and
// records the strike when it does. playStart is when the first push of this
// play went out (before any start retry). It returns the strike kind, or ""
// when the drop is not counted.
func (s *Server) noteSingleTrackRefused(title string, playStart time.Time) string {
	window := time.Since(playStart) + singleTrackRefusalSlack
	if s.loginErrorRecentWithin(window) {
		s.logger.Info("single track: the speaker refused the track as not signed in, counting it toward the restart hint",
			"title", title)
		s.noteRefusalStrike("login")
		return "login"
	}
	if s.boxPlayFailureFn == nil {
		return ""
	}
	failed, live := s.boxPlayFailureFn(window)
	switch {
	case failed:
		// The speaker tried the file and gave up on it: that is about the file
		// or the server, not a refusing speaker.
		s.logger.Info("single track: the speaker tried the track and logged a playback failure; not a refusal",
			"title", title)
		return ""
	case !live:
		// Without the log there is no telling a refusal from an unplayable
		// file, and telling someone to restart a healthy speaker is the worse
		// of the two wrong answers.
		return ""
	}
	s.logger.Info("single track: the speaker dropped the track without trying it (its log holds no playback failure); counting it toward the restart hint",
		"title", title)
	s.noteRefusalStrike("track")
	return "track"
}
