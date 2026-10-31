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

// presetRefusalKeep bounds how many refusals the reader remembers. One sweep
// writes at most six slots, so this covers two full sweeps with room to spare.
const presetRefusalKeep = 16

// PresetRefusal is one preset write the firmware refused in its syslog
// (UpdatePresetFailureCB).
type PresetRefusal struct {
	At time.Time
	// Reason is the firmware's line, redacted and truncated.
	Reason string
	// InvalidSourceID is set for "AddPreset - failed due to invalid
	// SourceID": the firmware's account copy of the key refusing an item
	// whose source has no account SourceID. It logs that for EVERY UPnP-form
	// write (Spotify keys, any slot without the native radio form), while the
	// key itself is stored and stays listed. Any other reason ("MargeHSM is
	// in the wrong state") is a real refusal of the write.
	InvalidSourceID bool
}

// PresetRefusalsSince returns the firmware's preset refusals stamped at or
// after since, oldest first, so a caller can count the refusals of one write
// sweep and read why each happened. Safe for concurrent use; nil on a nil
// reader.
func (r *Reader) PresetRefusalsSince(since time.Time) []PresetRefusal {
	if r == nil || r.forensics == nil {
		return nil
	}
	return r.forensics.presetRefusalsSince(since)
}

// AudioPathOffset returns the speaker's own play clock for a UPnP play, the
// newest "HandleAudioStatus: Audio path offset N" (seconds into the track),
// and when the reader saw it; ok is false when no such line was ever read
// (another chassis, the reader not running). The line comes once a second
// while the file plays, so a caller judges freshness from at.
//
// It is what tells a resume the speaker did by itself from the frozen resume
// of #830: on an ST10 (FW 27.0.6) GetPositionInfo RelTime and the now_playing
// <time> both stayed on the paused second through a resume that played on
// audibly, while this offset counted on with the music. Safe for concurrent
// use; a nil reader reports ok=false.
func (r *Reader) AudioPathOffset() (offset int, at time.Time, ok bool) {
	if r == nil || r.forensics == nil {
		return 0, time.Time{}, false
	}
	return r.forensics.audioPathOffset()
}

// StaleURLIDSince reports whether the firmware logged, at or after t, that it
// threw a re-opened file away as belonging to a stale URL id ("urlId
// mismatch", "PLAYING error #0 for stale UrlID"): the frozen resume's own
// signature (docs/FIRMWARE-NOTES.md). Safe for concurrent use; false on a nil
// reader.
func (r *Reader) StaleURLIDSince(t time.Time) bool {
	if r == nil || r.forensics == nil {
		return false
	}
	return r.forensics.staleURLIDSince(t)
}
