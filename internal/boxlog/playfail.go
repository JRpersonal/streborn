package boxlog

import "time"

// The firmware saying, in its own log, that it did not get a note out.
//
// These lines were classified for the diagnostic bundle and delivered to
// nobody, so STR could read after the fact that a press produced silence but
// could do nothing at the moment it happened. Measured on an ST30 on
// 2026-09-27: a preset pressed while the speaker was still coming out of
// standby produced "Could not obtain first-frame" and "PlaybackFailure
// ERROR_NO_DECODED_DATA" two seconds later, and the owner's second press was
// the only thing that started the music.

// PlayFailure is one such line, normalised.
type PlayFailure struct {
	// Class is play_failure or play_no_first_frame.
	Class Class
	// Message is the firmware's own words, for the log line that follows.
	Message string
	At      time.Time
}

// PlayFailureHandler receives each one. It runs on the reader's goroutine, so
// it must not block; the agent's handler starts its work and returns.
type PlayFailureHandler func(PlayFailure)

// SetPlayFailureHandler installs the hook. Set before Run; nil disables it.
func (r *Reader) SetPlayFailureHandler(h PlayFailureHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.playFailHandler = h
}

// firePlayFailure hands one classified failure to the hook.
func (r *Reader) firePlayFailure(ev Event) {
	if ev.Class != ClassPlayFailure && ev.Class != ClassPlayNoFirstFrame {
		return
	}
	r.mu.Lock()
	h := r.playFailHandler
	r.mu.Unlock()
	if h == nil {
		return
	}
	h(PlayFailure{Class: ev.Class, Message: ev.Message, At: ev.At})
}

// LastPresetRefusal reports when the firmware last logged that it refused a
// preset write ("UpdatePresetFailureCB ... MargeHSM is in the wrong state"),
// zero when it never did. The speaker's CLI acknowledges such an AddPreset
// like any other and the slot it already held stays listed, so a forced
// rewrite reads as landed; this line is the only sign it did not (SoundTouch
// 20 field bundle 2026-10-22: twelve refused boot-time writes logged as
// rewritten). Safe for concurrent use.
func (r *Reader) LastPresetRefusal() time.Time {
	if r == nil || r.forensics == nil {
		return time.Time{}
	}
	return r.forensics.presetRefusalAt()
}
