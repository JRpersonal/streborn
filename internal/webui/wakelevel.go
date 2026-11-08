package webui

import (
	"context"
	"time"
)

// wakelevel.go: the level a speaker comes back to after STR muted it for a
// wake of its own (a quiet wake for a group operation, a muted wake for a new
// play, a silent wake for a Spotify hand-over).
//
// Each of those wakes used to take the level to put back from GetVolume on the
// SLEEPING speaker, or from the last volume frame it saw. A sleeping speaker
// answers with the last level written into it, and STR itself writes 0 into it
// on every one of these wakes. So once one restore put 0 back, every later
// wake captured that 0 and "restored" it again. Seen on a v1.5.3 bundle (two
// SoundTouch 10 in a stereo pair, groups and pairs formed and dissolved, then
// a preset): the pair master woke at its own level 10, the quiet wake had
// captured 0 before that, the group volume 14 set during the pairing was
// overwritten with 0 when the pair went back to standby, and the preset after
// it read that 0 from the frames and played at 0.
//
// The rules now, for all three wakes:
//
//   - The level to put back is the last non-zero level the speaker reported
//     while it was awake and STR was not holding it down (rememberedAwakeLevel,
//     fed by NoteBoxVolumeLevel). Frames seen while STR mutes the speaker are
//     never taken for it.
//   - Unknown that (the agent just started), the speaker's own power-on level
//     is the next best: the first non-zero level frame during the wake, before
//     STR's mute took it down. Then a non-zero level read while the speaker
//     slept. A 0 is never a level to put back, wherever it came from.
//   - Unknown all of those, a speaker STR muted is still never left at 0: it
//     goes to the start volume when the user configured one (their own choice
//     of "the level my speaker starts at"), else to defaultWakeRestoreLevel.
//   - A level that is no longer 0 when the restore runs was set by somebody
//     since (the group volume, the volume key, the app) and is left as it is.

// wakeLevelInputs is what is known before a muted wake about the level the
// speaker played at. 0 in a field means unknown.
type wakeLevelInputs struct {
	// remembered is the last non-zero level the speaker reported while awake
	// and not held by STR.
	remembered int
	// standby is a non-zero level read from the speaker while it slept (its
	// target level first). Weaker than the power-on level: a sleeping speaker
	// can report a level below the one it plays at (live ST10: read 5, resumed
	// at 10), and it reports whatever was last written into it.
	standby int
}

// defaultWakeRestoreLevel is the level a speaker STR muted for a wake comes
// back to when nothing at all is known about its own level and no start
// volume is configured. 15 of 100 is a quiet but clearly audible level on
// every SoundTouch model: audible, so the user hears that the play started
// instead of finding the speaker "broken" at 0, and low enough that it never
// startles. The levels seen in field bundles sit between 10 and 35.
const defaultWakeRestoreLevel = 15

// rememberedAwakeLevel is the last non-zero level the speaker reported while
// awake and not held down by STR, 0 when none was seen since the agent
// started.
func (s *Server) rememberedAwakeLevel() int {
	l := &s.newPlayLevel
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lastAt.IsZero() || l.last <= 0 {
		return 0
	}
	return l.last
}

// noteAwakeLevel records level as the one the speaker plays at. 0 is never
// recorded: a 0 is a level nobody wants back after a wake, and STR's own mute
// is the most common source of one.
func (s *Server) noteAwakeLevel(level int) {
	if level <= 0 {
		return
	}
	l := &s.newPlayLevel
	l.mu.Lock()
	l.last = level
	l.lastAt = time.Now()
	l.mu.Unlock()
}

// wakeLevelInputsNow gathers what is known about the speaker's level just
// before a muted wake. The read of the sleeping speaker is made only when
// nothing was remembered, so a play out of standby is not slowed by it.
func (s *Server) wakeLevelInputsNow(ctx context.Context) wakeLevelInputs {
	in := wakeLevelInputs{remembered: s.rememberedAwakeLevel()}
	if in.remembered > 0 {
		return in
	}
	volCtx, cancel := context.WithTimeout(ctx, quietWakeStepBudget)
	defer cancel()
	if v, err := s.ownVolume(volCtx); err == nil {
		switch {
		case v.Target > 0:
			in.standby = v.Target
		case v.Actual > 0:
			in.standby = v.Actual
		}
	}
	return in
}

// wakeRestoreTarget picks the level a speaker STR muted comes back to, and
// names where it came from for the log. powerOn is the first non-zero level
// the speaker reported during the wake, 0 when none. Never 0.
func (s *Server) wakeRestoreTarget(in wakeLevelInputs, powerOn int) (vol int, from string) {
	switch {
	case in.remembered > 0:
		vol, from = in.remembered, "the level before standby"
	case powerOn > 0:
		vol, from = powerOn, "the speaker's own power-on level"
	case in.standby > 0:
		vol, from = in.standby, "the level read in standby"
	default:
		if v, ok := s.startVolume(); ok {
			vol, from = v, "the start volume"
		} else {
			vol, from = defaultWakeRestoreLevel, "the default level, nothing else was known"
		}
	}
	if capped := s.capVolume(vol); capped != vol {
		vol, from = capped, from+", held to the maximum volume"
	}
	return vol, from
}

// restoreWokenLevel brings back the level of a speaker STR muted for a wake.
// kind names the wake for the log ("quiet wake", "silent wake"), why the
// reason of the release. A level that is no longer 0 was set by somebody
// since and is left; it is also the level the speaker plays at from now on.
// A level that cannot be read is treated as still muted, so a failed read
// never leaves the speaker at 0.
func (s *Server) restoreWokenLevel(kind, why string, in wakeLevelInputs, powerOn int) {
	ctx, cancel := quietWakeStepCtx()
	defer cancel()
	if v, err := s.ownVolume(ctx); err == nil && v.Actual != 0 {
		s.noteAwakeLevel(v.Actual)
		s.logger.Info("wake: level released after a "+kind, "restored", false,
			"reason", "the level was set since the mute, leaving it", "vol", v.Actual, "why", why)
		return
	}
	vol, from := s.wakeRestoreTarget(in, powerOn)
	if err := s.setOwnLevel(ctx, vol); err != nil {
		s.logger.Warn("wake: could not restore the level after a "+kind, "vol", vol, "from", from, "why", why, "err", err)
		return
	}
	s.noteAwakeLevel(vol)
	s.logger.Info("wake: level restored after a "+kind, "restored", true, "vol", vol, "from", from, "why", why)
}
