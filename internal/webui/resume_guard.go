package webui

import (
	"time"
)

// Auto-resume crash-loop guard (discussion #381).
//
// The power-on resume and the reconnect recovery re-push the persisted
// last-play stream automatically, with no user action involved. When that
// very stream is what crashes the box (a SoundTouch 20 was reported playing
// distorted audio for a few seconds and then watchdog-rebooting), each boot
// auto-resumes the same stream and the box loops forever: boot -> resume ->
// crash -> watchdog reboot. The only way out was SSH surgery (move rc.local
// aside, delete last-play.json).
//
// The guard breaks that loop: every automatic resume attempt is counted and
// persisted to NAND (inside last-play.json, so it survives the reboot the
// crash causes). Attempts only count as consecutive while they happen within
// autoResumeAttemptWindow of each other - a crash loop produces attempts
// minutes apart, while ordinary resumes are hours apart. After
// maxAutoResumeAttempts consecutive attempts that never reached stable
// playback, the automatic paths stand down and the box stays idle.
//
// The guard resets itself three ways:
//   - stability: the agent surviving autoResumeStableAfter past an attempt
//     with no further attempt proves the resume did not crash the box
//     (a crash reboots the whole box, killing the agent long before the
//     timer fires);
//   - a fresh play (setLastPlay): the user starting anything - preset key,
//     app, queue - is an explicit "play this", and it may well be a
//     different stream;
//   - decay: once the window has passed with no attempt, one new attempt is
//     allowed again (counted from 1). A crash loop thus degrades to at most
//     one reboot per window instead of a tight loop, and a box whose crash
//     was transient heals itself without user action.
const (
	// maxAutoResumeAttempts is how many consecutive automatic resume attempts
	// may end in a reboot/agent death before the automatic paths stand down.
	maxAutoResumeAttempts = 3
	// autoResumeAttemptWindow bounds what "consecutive" means: an attempt
	// this long after the previous one starts a fresh count. It is also how
	// long the tripped guard holds before allowing one probing attempt again.
	autoResumeAttemptWindow = 30 * time.Minute
	// autoResumeStableAfter is how long the agent must outlive an attempt for
	// the resume to count as stable (box did not crash), clearing the count.
	autoResumeStableAfter = 10 * time.Minute
)

// autoResumeBlocked reports whether the automatic resume paths (ResumeLastPlay,
// RecoverAfterReconnect) must stand down because the last
// maxAutoResumeAttempts attempts all ended in a reboot before reaching stable
// playback. Manual plays are never blocked; any of them re-arms the resume via
// setLastPlay's reset.
func (s *Server) autoResumeBlocked() bool {
	s.lastPlayMu.Lock()
	defer s.lastPlayMu.Unlock()
	return s.resumeAttempts >= maxAutoResumeAttempts &&
		time.Since(s.lastResumeAt) < autoResumeAttemptWindow
}

// noteAutoResumeAttempt records that an automatic resume is about to push the
// persisted stream, and persists the count to NAND BEFORE the push: if the
// stream crashes the box, the incremented count is what the next boot loads.
// It also arms the stability timer that clears the count when the agent
// outlives the attempt (see clearResumeAttemptsIfQuiet).
func (s *Server) noteAutoResumeAttempt() {
	s.lastPlayMu.Lock()
	if time.Since(s.lastResumeAt) >= autoResumeAttemptWindow {
		s.resumeAttempts = 0
	}
	s.resumeAttempts++
	s.lastResumeAt = time.Now()
	attempts := s.resumeAttempts
	at := s.lastResumeAt
	lp := s.lastPlay
	s.lastPlayMu.Unlock()
	if lp != nil {
		s.persistLastPlay(lp.boxURL, lp.title, lp.art, lp.mime, lp.ts, attempts, at)
	}
	if attempts >= maxAutoResumeAttempts {
		s.logger.Warn("auto-resume guard: this is the last automatic resume attempt; if the box reboots again the power-on resume stands down until a manual play",
			"attempts", attempts)
	}
	time.AfterFunc(autoResumeStableAfter, func() { s.clearResumeAttemptsIfQuiet(at) })
}

// clearResumeAttemptsIfQuiet resets the attempt count when no NEWER attempt
// has been recorded since `at`: the agent surviving this long past the attempt
// proves the resumed stream did not crash the box (a crash watchdog-reboots
// the whole box, so the timer that calls this never fires). A newer attempt
// means a reboot DID happen in between; leave its count alone.
func (s *Server) clearResumeAttemptsIfQuiet(at time.Time) {
	s.lastPlayMu.Lock()
	if s.resumeAttempts == 0 || !s.lastResumeAt.Equal(at) {
		s.lastPlayMu.Unlock()
		return
	}
	s.resumeAttempts = 0
	s.lastResumeAt = time.Time{}
	lp := s.lastPlay
	s.lastPlayMu.Unlock()
	if lp != nil {
		s.persistLastPlay(lp.boxURL, lp.title, lp.art, lp.mime, lp.ts, 0, time.Time{})
	}
	s.logger.Info("auto-resume guard: playback stayed up, resume attempt count cleared")
}
