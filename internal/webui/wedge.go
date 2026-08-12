package webui

// Wedged-control detection.
//
// Field case (SoundTouch 300, 2026-07-09): the box answers :8090, the agent
// runs, presets are accepted — but the speaker blinks its boot pattern, the
// firmware never pulls the stream URL it just accepted and never reaches a
// playing state. Software reboots do not clear it; only a power-cycle does.
// The user cannot tell this state from a normal boot, so STR now detects it
// and says the one thing that helps: pull the plug.
//
// Signal: a recall verify that exhausts its retries while (a) the box is
// awake (not STANDBY — a user power-off mid-recall is not a wedge), (b) the
// box never opened any proxied stream in the window, and (c) no upstream
// stream failure was recorded (that would be a dead STATION, not a dead box).
// Two such strikes in a row latch boxHealth=wedged, surfaced via
// /api/agent/version to the desktop app and the phone remote. Any observed
// playback clears the state.

import (
	"context"
	"sync"
	"time"
)

// wedgeStrikeWindow is how far back a proxy fetch / upstream failure absolves
// an exhausted recall: it spans the recall's own retry cycle (3x5 s + pushes).
const wedgeStrikeWindow = 90 * time.Second

// wedgeStrikesToLatch is how many consecutive absolved-by-nothing recall
// failures latch the wedged state. Two keeps a single odd failure quiet.
const wedgeStrikesToLatch = 2

type wedgeState struct {
	mu      sync.Mutex
	strikes int
	wedged  bool
	since   time.Time
	lastHit time.Time // when the most recent strike was recorded
}

// loginErrState tracks the most recent not-logged-in rejection (errorUpdate
// 1036), so the recall verify can stand down instead of re-pushing a source the
// box refuses (which flaps the source and can wedge it).
type loginErrState struct {
	mu   sync.Mutex
	last time.Time
}

// recentLoginErrorWindow is how long a not-logged-in rejection suppresses the
// recall retry after it - long enough to span the verify's own 3x5 s cycle so
// STR does not immediately re-push the source the box just refused.
const recentLoginErrorWindow = 20 * time.Second

// loginErrWedgeSkipWindow is how long after a not-logged-in rejection an
// exhausted recall verify is attributed to the login instead of a wedge. Wider
// than recentLoginErrorWindow: the hardware verify exhausts ~26s after the
// press that provoked the 1036.
const loginErrWedgeSkipWindow = 60 * time.Second

// NoteBoxLoginError records that the box just rejected a source because it does
// not think it is signed in (errorUpdate 1036). Wired from the boxws
// not-logged-in callback. verifyRecall reads recentLoginError() to stand its
// retry down meanwhile, so STR self-heals via a bare stream re-push (v0.9.0
// behaviour) without thrashing the box.
//
// It deliberately does NOT force a re-login. A forced re-assert of the marge
// account (ForcePair -> setMargeAccount) makes the box re-onboard mid-recall,
// which bounces its active source through INVALID_SOURCE and the firmware then
// powers the source off to STANDBY - the "reconnect with volume reset" users
// saw on every taigan/scm remote-preset press (field 2026-07-23). v0.9.0 had no
// such reactive re-login and recalled cleanly; the re-assert never actually
// cured the 1036 (the UPnP activation login is not levered by the marge
// account, proven on .79) - it only cost a self-off. A genuinely un-paired box
// is still re-paired by the proactive EnsurePaired on the next wake / connect /
// press (triggerPairAsync), which is a no-op when the box is already paired and
// therefore never bounces a live source.
func (s *Server) NoteBoxLoginError() {
	s.loginErr.mu.Lock()
	s.loginErr.last = time.Now()
	s.loginErr.mu.Unlock()
}

// loginErrorRecentWithin reports whether the box rejected a source as
// not-logged-in within the given window.
func (s *Server) loginErrorRecentWithin(window time.Duration) bool {
	s.loginErr.mu.Lock()
	defer s.loginErr.mu.Unlock()
	return !s.loginErr.last.IsZero() && time.Since(s.loginErr.last) < window
}

// LoginErrorSince reports whether the box rejected a source as not-logged-in
// (1036) at any point after t. The hardware recall verify reads this to decide
// whether a now-closed slot fetch is trustworthy proof of playback: a login
// error means the box's re-login source bounce can serve the proxied stream for
// ~3s and drop it without audio, so a closed short pull is NOT success in that
// window. Safe for concurrent use.
func (s *Server) LoginErrorSince(t time.Time) bool {
	s.loginErr.mu.Lock()
	defer s.loginErr.mu.Unlock()
	return !s.loginErr.last.IsZero() && s.loginErr.last.After(t)
}

// recentLoginError reports whether the box rejected a source as not-logged-in
// within recentLoginErrorWindow.
func (s *Server) recentLoginError() bool {
	return s.loginErrorRecentWithin(recentLoginErrorWindow)
}

// SetStreamActivityFn wires the stream proxy's LastActivity so the wedge
// detector can tell "box never pulled the stream" from "station failed".
func (s *Server) SetStreamActivityFn(fn func() (lastFetch, lastFailure time.Time)) {
	s.streamActivityFn = fn
}

// stationRefusedRecently reports whether the stream proxy failed on its upstream
// inside the strike window, i.e. the station refused us rather than the box
// misbehaving. The proxy records a failure for a non-200 upstream status
// (401/403/404/410 and friends) as well as for a dead host, so this covers both
// the taken-down station and the one that started demanding credentials.
func (s *Server) stationRefusedRecently() bool {
	if s.streamActivityFn == nil {
		return false
	}
	_, fail := s.streamActivityFn()
	return !fail.IsZero() && time.Since(fail) < wedgeStrikeWindow
}

// NoteRecallExhausted is called when a play/recall verify gave up. It decides
// whether this failure looks like the box (not the station) and counts a
// strike; the second consecutive strike latches wedged.
func (s *Server) NoteRecallExhausted() {
	// Read the live state once, best-effort; the decision lives in
	// noteRecallExhaustedWithSource so it is testable without a box.
	npCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	np := s.snapshotNowPlaying(npCtx)
	cancel()
	s.noteRecallExhaustedWithSource(np.Source)
}

func (s *Server) noteRecallExhaustedWithSource(source string) {
	if source == "" {
		return
	}
	if source == "STANDBY" {
		// A user power-off mid-recall exhausts the verify too; that standby
		// is not a wedge. But when HandleEnterStandby just classified the
		// drop as NOT a user power-off (the #419 mid-recall branch: no
		// adjacent key press), the standby is the box giving up on its own -
		// the silent variant of the not-logged-in refusal family, which never
		// sends a 1036 and therefore never trips the storm banner. Count it
		// toward its own latch so the app can finally say "restart the
		// speaker" instead of failing without a word (field: two independent
		// ST10 reports, 2026-08-01).
		s.noteSilentRefusalCandidate()
		return
	}
	// The station is checked BEFORE the login window, because a 1036 arrives on
	// practically every hardware press on some boxes and would otherwise claim
	// every failure for the login family.
	//
	// A station whose upstream refuses us cannot be fixed by anything the user
	// does to the speaker. Live case (#582, ST10, 2026-08-12): slot 4 pointed
	// at a URL answering 401 on every fetch, so every recall ran push, 401,
	// AUDIO_ERROR_BAD_URL, source drop, retry, five times over, and STR ended
	// it with "restart the speaker" while slots 1 and 5 played fine seconds
	// earlier through the same box and the same 1036s. Telling someone to
	// power-cycle a healthy speaker because one station went dead is the worst
	// kind of wrong answer: it costs them time and teaches them to distrust the
	// next message.
	if s.stationRefusedRecently() {
		s.logger.Info("recall exhausted right after the station refused the stream; not a box problem",
			"hint", "the station URL is answering an error, not the speaker")
		return
	}
	// A recall that exhausted while the box was rejecting sources as
	// not-logged-in failed on the LOGIN, not on a wedged transport: counting
	// it latched boxHealth=wedged and told the user to power-cycle a box that
	// only needed the re-login to stick (field bundle: strike at +25s after a
	// 1036). The window is wider than recentLoginError's 20s because the
	// verify exhausts ~26s after the press.
	if s.loginErrorRecentWithin(loginErrWedgeSkipWindow) {
		s.logger.Info("recall exhausted during a not-logged-in window; not counting a wedge strike (login failure, not a wedge)")
		// It is not a wedge, but it is not nothing either: the box refused
		// the recall because it thinks it is signed out, and the cure is the
		// same soft restart the refusal banner offers. Until now this branch
		// latched NOTHING, and the 1036 storm marker needs six rejections in
		// ten minutes, so a user whose box refused "only" three times saw no
		// message at all while no preset played (field: ST10, 2026-08-02,
		// three rejections across two minutes, then a plug pull).
		s.noteRefusalStrike("login")
		return
	}
	if s.streamActivityFn != nil {
		fetch, fail := s.streamActivityFn()
		if (!fetch.IsZero() && time.Since(fetch) < wedgeStrikeWindow) ||
			(!fail.IsZero() && time.Since(fail) < wedgeStrikeWindow) {
			// The box pulled a stream (or the station demonstrably failed):
			// the problem is the content path, not a wedged box.
			return
		}
	}
	s.wedge.mu.Lock()
	s.wedge.strikes++
	s.wedge.lastHit = time.Now()
	latch := s.wedge.strikes >= wedgeStrikesToLatch && !s.wedge.wedged
	if latch {
		s.wedge.wedged = true
		s.wedge.since = time.Now()
	}
	strikes := s.wedge.strikes
	s.wedge.mu.Unlock()
	if latch {
		s.logger.Warn("box wedge detected: transport accepted but the box never pulls the stream and never plays; a power-cycle is required (software reboots do not clear this state)",
			"strikes", strikes)
	} else {
		s.logger.Warn("box wedge suspected (strike recorded)", "strikes", strikes)
	}
}

// NoteBoxHealthy clears the wedge state; called whenever playback is actually
// observed (a verify succeeding, the box attaching to a stream).
func (s *Server) NoteBoxHealthy() {
	s.wedge.mu.Lock()
	wasWedged := s.wedge.wedged
	s.wedge.strikes = 0
	s.wedge.wedged = false
	s.wedge.since = time.Time{}
	s.wedge.lastHit = time.Time{}
	s.wedge.mu.Unlock()
	if wasWedged {
		s.logger.Info("box wedge cleared: playback observed")
	}
	s.refusal.mu.Lock()
	wasRefusing := s.refusal.latched
	s.refusal.strikes = 0
	s.refusal.latched = false
	s.refusal.since = time.Time{}
	s.refusal.lastNonUserDrop = time.Time{}
	s.refusal.mu.Unlock()
	if wasRefusing {
		s.logger.Info("silent recall refusal cleared: playback observed")
	}
}

// refusalState tracks recalls that exhausted while the box dropped its source
// to STANDBY on its own (no adjacent key press, no 1036): the silent variant
// of the not-logged-in refusal family. It sneaks past both existing detectors
// - no 1036 means no storm, and the self-drop to STANDBY made the exhausted
// verify look like a user power-off - so users saw no message at all while
// nothing played. The latched state is surfaced on the version envelope next
// to the 1036 storm and joins the app's storm banner, whose soft-restart
// button is the right remedy (a plug pull also clears it but poisons the box
// clock, #419 F4).
type refusalState struct {
	mu              sync.Mutex
	strikes         int
	latched         bool
	since           time.Time
	lastNonUserDrop time.Time
}

// refusalStrikesToLatch mirrors wedgeStrikesToLatch: two consecutive silent
// failures latch, a single odd failure stays quiet.
const refusalStrikesToLatch = 2

// NoteNonUserStandbyDrop records that HandleEnterStandby classified a
// UPNP->STANDBY drop as NOT a user power-off (#419 mid-recall branch), so an
// exhausted verify seeing STANDBY can tell "the box gave up on its own" from
// "the user switched it off".
func (s *Server) NoteNonUserStandbyDrop() {
	s.refusal.mu.Lock()
	s.refusal.lastNonUserDrop = time.Now()
	s.refusal.mu.Unlock()
}

func (s *Server) nonUserStandbyDropRecent(window time.Duration) bool {
	s.refusal.mu.Lock()
	defer s.refusal.mu.Unlock()
	return !s.refusal.lastNonUserDrop.IsZero() && time.Since(s.refusal.lastNonUserDrop) < window
}

// noteSilentRefusalCandidate decides whether an exhausted recall that ended in
// STANDBY counts toward the silent-refusal latch. Same absolution ladder as
// the wedge: a user power-off (no non-user drop classified), a recent 1036
// (the storm banner owns that messaging), or recent stream activity (a content
// problem, not the box) all keep it quiet.
func (s *Server) noteSilentRefusalCandidate() {
	if !s.nonUserStandbyDropRecent(wedgeStrikeWindow) {
		return
	}
	// A recent 1036 does NOT excuse this any more: it used to hand the
	// messaging to the storm marker, which only fires at six rejections in
	// ten minutes and therefore left the three-rejection case silent.
	if s.streamActivityFn != nil {
		fetch, fail := s.streamActivityFn()
		if (!fetch.IsZero() && time.Since(fetch) < wedgeStrikeWindow) ||
			(!fail.IsZero() && time.Since(fail) < wedgeStrikeWindow) {
			return
		}
	}
	s.noteRefusalStrike("silent")
}

// noteRefusalStrike counts one refused recall toward the restart hint and
// latches it on the second consecutive one. kind names the observed flavour
// for the bundle: "silent" (source self-dropped, no 1036) or "login" (the box
// answered 1036). Both end in the same advice, so they share one latch.
func (s *Server) noteRefusalStrike(kind string) {
	s.refusal.mu.Lock()
	s.refusal.strikes++
	latch := s.refusal.strikes >= refusalStrikesToLatch && !s.refusal.latched
	if latch {
		s.refusal.latched = true
		s.refusal.since = time.Now()
	}
	strikes := s.refusal.strikes
	s.refusal.mu.Unlock()
	if latch {
		// Bundle-forensics marker: this line separates the refusal family
		// from a wedge and names which flavour was observed.
		s.logger.Warn("box refuses recalls: surfacing the restart hint", "kind", kind, "strikes", strikes)
	} else {
		s.logger.Warn("recall refusal suspected (strike recorded)", "kind", kind, "strikes", strikes)
	}
}

// RecallRefusal reports whether the silent-refusal state is latched, and since
// when. Surfaced on the version envelope so the desktop app can join it into
// the storm banner (same remedy: a soft restart).
func (s *Server) RecallRefusal() (active bool, since time.Time) {
	s.refusal.mu.Lock()
	defer s.refusal.mu.Unlock()
	return s.refusal.latched, s.refusal.since
}

// BoxStateHint reports the speaker-side condition that would make streams fail
// regardless of the station: "wedged" (transport accepted but the box never
// pulls or plays; a power-cycle is required) or "login-error" (the box is
// rejecting sources as not-logged-in while the re-login self-heal runs). "" =
// no known box-side problem. Surfaced via /api/stream-status so the desktop
// app can name the real cause instead of cycling station alternates.
func (s *Server) BoxStateHint() string {
	if status, _ := s.BoxHealth(); status == "wedged" {
		return "wedged"
	}
	if s.loginErrorRecentWithin(loginErrWedgeSkipWindow) {
		return "login-error"
	}
	return ""
}

// BoxHealth reports "ok" or "wedged" (plus the latch time for the latter).
func (s *Server) BoxHealth() (status string, since time.Time) {
	s.wedge.mu.Lock()
	defer s.wedge.mu.Unlock()
	if s.wedge.wedged {
		return "wedged", s.wedge.since
	}
	return "ok", time.Time{}
}

// BoxHealthStrikes reports the current wedge-strike count and when the most
// recent strike landed. A diagnostic comparison of a wedged vs. healthy box
// showed the pre-latch state was invisible over the API (boxHealth stays "ok"
// at one strike while the user already sees "service unavailable"), so tools
// could not tell a building wedge from a healthy box (#402). Exposed via
// /api/agent/version alongside boxHealth.
func (s *Server) BoxHealthStrikes() (strikes int, lastHit time.Time) {
	s.wedge.mu.Lock()
	defer s.wedge.mu.Unlock()
	return s.wedge.strikes, s.wedge.lastHit
}
