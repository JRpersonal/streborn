package webui

import (
	"sync"
	"time"
)

// playSession is the stream STR last told the box to play together with every
// piece of bookkeeping that is read or written in the same breath: the recall
// generation, the stream-drop re-push latch, the stuck-buffering rescue stamp,
// the auto-resume crash-loop counters, the live ICY title and TuneIn station
// of that stream, and the display-push debounce. One mutex guards all of it
// and only the methods below touch the fields, so a section that has to read
// or change two of them together (a check-then-set, a generation read next to
// the URL) stays one critical section and cannot be split by a caller.
//
// The pointer handed out by current is shared: the URL and the metadata of a
// record never change once it is made, the re-push counters on it do, and
// those are only changed through this type.
type playSession struct {
	mu sync.Mutex
	// last remembers the stream STR last told the box to play, so the
	// auto-re-push (#4) can resume it when the Bose renderer drops a long
	// stream on its own (reported: radio stops after ~11 min, no STR error).
	last *lastPlayInfo
	// rePushInFlight coalesces stream-drop resumes (see HandleStreamDisconnect).
	// A SERVER-level latch on purpose, guarded by mu: it used to live on
	// lastPlayInfo, but setLastPlay replaces that struct on every fresh play, so
	// a drop of the NEW stream while an older maybeRePush was still waiting out
	// the recall-ownership window spawned a second concurrent re-pusher, and the
	// first one's deferred release then cleared the latch the second one owned -
	// the box got double SetURI+Play (two audible re-buffers) and a third
	// goroutine could stack. One latch per Server survives the struct swap.
	rePushInFlight bool
	// stuckBufRescueAt is when the stuck-buffering rescue (spotify_stuck_
	// buffer.go) last acted, guarded by mu. One rescue per episode:
	// only a user play newer than this opens the next one.
	stuckBufRescueAt time.Time
	// recallGen counts every stream push recorded via setLastPlay. Each
	// recall's verify loop captures the generation of its own play; any later
	// play bumps it, telling the older verify it was superseded so it stands
	// down instead of re-pushing its now-unwanted stream over the user's newer
	// choice (two rapid preset presses used to ping-pong stations for ~15s).
	// Guarded by mu.
	recallGen uint64
	// resumeAttempts counts consecutive AUTOMATIC resume attempts (power-on
	// resume / reconnect recovery) that never reached stable playback, and
	// lastResumeAt is when the newest one was pushed. They drive the
	// auto-resume crash-loop guard (#381, see resume_guard.go): a stream that
	// crashes the box on playback otherwise loops boot -> auto-resume ->
	// crash -> watchdog reboot forever. Guarded by mu; persisted
	// inside last-play.json so the count survives the very reboot it counts.
	resumeAttempts int
	lastResumeAt   time.Time
	// lastDisplayPush debounces the ICY display push so a station that flips its
	// StreamTitle between the song and promo/talk lines does not cause a re-buffer
	// gap every few seconds. Guarded by mu.
	lastDisplayPush time.Time
	// lastDisplayShown and lastDisplayFor remember what the last display push
	// put on screen and for which play. A push re-buffers the box, so a title
	// change that would show the SAME text (artist mode across two songs by one
	// artist, or a station that blanks its StreamTitle between songs and then
	// resends the same one) is skipped instead of costing an audible gap for
	// nothing (#500). Keyed on the lastPlay pointer so a new play always pushes.
	// Guarded by mu.
	lastDisplayShown string
	lastDisplayFor   *lastPlayInfo
	// lastICYTitle is the most recent radio StreamTitle seen, kept so enabling the
	// display push or changing its mode can show the CURRENT track immediately
	// instead of waiting for the next title change. Guarded by mu.
	lastICYTitle string
	// tuneInStation is the TuneIn station last resolved, for the now-playing
	// answer (#500). Set and read together with lastICYTitle.
	tuneInStation tuneInNowStation
}

// current returns the play on record, nil when there is none.
func (p *playSession) current() *lastPlayInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}

// currentURL returns the box-facing URL of the play on record, "" when none.
func (p *playSession) currentURL() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last == nil {
		return ""
	}
	return p.last.boxURL
}

// snapshot returns a copy of the play on record.
func (p *playSession) snapshot() (lastPlayInfo, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last == nil {
		return lastPlayInfo{}, false
	}
	return *p.last, true
}

// record is a fresh play (setLastPlay): it replaces the record, bumps the
// recall generation and re-arms the auto-resume crash-loop guard (#381),
// because a fresh play is an explicit user "play this" and may well be a
// different stream. newStream reports whether the URL changed against the
// previous record.
func (p *playSession) record(lp *lastPlayInfo) (gen uint64, newStream bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	newStream = p.last == nil || p.last.boxURL != lp.boxURL
	p.last = lp
	p.recallGen++
	p.resumeAttempts = 0
	p.lastResumeAt = time.Time{}
	return p.recallGen, newStream
}

// restore seeds the record from NAND at start-up together with the
// auto-resume guard count persisted with it (#381): after a crash-caused
// reboot this is what tells the automatic resume it is looping. The recall
// generation is left alone, nothing was pushed.
func (p *playSession) restore(lp *lastPlayInfo, resumeAttempts int, lastResumeAt time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.last = lp
	p.resumeAttempts = resumeAttempts
	p.lastResumeAt = lastResumeAt
}

// gen returns the current recall generation.
func (p *playSession) gen() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.recallGen
}

// bumpGen advances the recall generation without recording a play.
func (p *playSession) bumpGen() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.recallGen++
}

// genAndURL reads the recall generation and the URL on record in one go, so
// the two cannot come from different plays.
func (p *playSession) genAndURL() (gen uint64, boxURL string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last != nil {
		boxURL = p.last.boxURL
	}
	return p.recallGen, boxURL
}

// markFromBoxIfGen flags the record as a station the firmware activated by
// itself, but only while it is still the play that gen was issued for; a
// newer play in between is left alone.
func (p *playSession) markFromBoxIfGen(gen uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last != nil && p.recallGen == gen {
		p.last.fromBox = true
	}
}

// userPlayedSince reports whether a play STR itself pushed (not one the
// firmware activated) is on record with a timestamp after t.
func (p *playSession) userPlayedSince(t time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last != nil && !p.last.fromBox && p.last.ts.After(t)
}

// noteRepushed marks the record as a play STR itself pushes again: fresh
// timestamp, no longer the firmware's own doing. The power-on endpoint
// re-pushes the record it holds, which may be the firmware's own power-on
// resume (NoteNativeLastPlay, fromBox). Left as it was, the late group-wake
// guard took STR's own push for that self-resume and stopped it, so a group
// key pressed on a sleeping speaker played its station for four seconds and
// went silent (2026-10-27). Only the record that was pushed is touched: a
// newer play in between is left alone. The generation and the auto-resume
// counters stay as they are, it is the same stream.
//
// The returned undo puts the record back as it was, for a push that then
// failed; it does nothing once anything else changed the record.
func (p *playSession) noteRepushed(boxURL string) (undo func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last == nil || p.last.boxURL != boxURL {
		return func() {}
	}
	rec := p.last
	prevTS, prevFromBox := rec.ts, rec.fromBox
	marked := time.Now()
	rec.ts = marked
	rec.fromBox = false
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.last != rec || !rec.ts.Equal(marked) || rec.fromBox {
			return
		}
		rec.ts, rec.fromBox = prevTS, prevFromBox
	}
}

// rearmForResume clears the given-up flag and the re-push count of the record
// so a resume can push it again, and hands back a copy. With maxAge > 0 a
// record older than that is left untouched and reported as tooOld; ok is
// false for that and for an empty record (the copy still carries the record
// when there is one, so a caller can log its age).
func (p *playSession) rearmForResume(maxAge time.Duration) (lp lastPlayInfo, tooOld, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last == nil {
		return lastPlayInfo{}, false, false
	}
	if maxAge > 0 && time.Since(p.last.ts) >= maxAge {
		return *p.last, true, false
	}
	p.last.failed = false
	p.last.rePushes = 0
	return *p.last, false, true
}

// markFailed declares the stream on record dead so no later drop re-arms it.
func (p *playSession) markFailed() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last != nil {
		p.last.failed = true
	}
}

// rePushMaxAge bounds how old a dropped stream may be and still be brought
// back by the drop recovery.
const rePushMaxAge = 6 * time.Hour

// tryClaimRePush takes the drop-coalescing latch for the stream on record and
// reports whether the caller now owns the re-push. It refuses when there is
// no recent stream, when the stream was already declared dead, or when a
// resume is already in flight. A dead/moved URL fires a disconnect on EVERY
// failed resume; without this latch each one spawned a fresh maybeRePush
// goroutine, producing the dozens-per-second runaway that starved :8888
// (v0.7.5). The latch is Server-level so it survives setLastPlay swapping the
// lastPlayInfo struct mid-wait.
func (p *playSession) tryClaimRePush() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	lp := p.last
	if lp == nil || time.Since(lp.ts) >= rePushMaxAge || lp.failed || p.rePushInFlight {
		return false
	}
	p.rePushInFlight = true
	return true
}

// releaseRePush frees the latch when the re-pusher exits so a later genuine
// drop can re-arm it. The latch lives here and not on the record, so this
// release cannot land on the wrong struct after setLastPlay swapped the
// record mid-run.
func (p *playSession) releaseRePush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rePushInFlight = false
}

// rePushAttempt reports how many re-pushes the stream on record already
// took; ok is false when nothing is on record.
func (p *playSession) rePushAttempt() (attempt int, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last == nil {
		return 0, false
	}
	return p.last.rePushes, true
}

// resetRePushes clears the per-stream attempt counter after a recovery, so a
// later genuine drop starts a fresh backoff window.
func (p *playSession) resetRePushes() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last != nil {
		p.last.rePushes = 0
	}
}

// nextRePush claims the next re-push attempt of the stream on record and
// hands back a copy with the attempt counted. ok is false when nothing is on
// record. gaveUp is true when the stream had already spent maxRePushes: it
// is then marked dead in the same section so no further disconnect re-arms
// it (only a fresh play clears this), and the copy carries the URL for the
// log line.
func (p *playSession) nextRePush() (lp lastPlayInfo, gaveUp, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last == nil {
		return lastPlayInfo{}, false, false
	}
	if p.last.rePushes >= maxRePushes {
		p.last.failed = true
		return *p.last, true, true
	}
	p.last.rePushes++
	return *p.last, false, true
}

// autoResumeBlocked reports whether the crash-loop guard (#381) has tripped:
// maxAutoResumeAttempts automatic resumes inside the window without stable
// playback.
func (p *playSession) autoResumeBlocked() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.resumeAttempts >= maxAutoResumeAttempts &&
		time.Since(p.lastResumeAt) < autoResumeAttemptWindow
}

// noteAutoResumeAttempt counts an automatic resume that is about to push the
// persisted stream. A count older than the window starts over. It returns
// the new count and stamp together with the record they belong to, so the
// caller can persist all three as one state.
func (p *playSession) noteAutoResumeAttempt() (attempts int, at time.Time, lp *lastPlayInfo) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if time.Since(p.lastResumeAt) >= autoResumeAttemptWindow {
		p.resumeAttempts = 0
	}
	p.resumeAttempts++
	p.lastResumeAt = time.Now()
	return p.resumeAttempts, p.lastResumeAt, p.last
}

// clearResumeAttemptsIfQuiet resets the attempt count when the newest
// attempt is still the one stamped at `at` and there is a count to clear.
// cleared is false otherwise (a newer attempt means a reboot DID happen in
// between; its count is left alone). lp is the record the cleared count
// belonged to, for persisting.
func (p *playSession) clearResumeAttemptsIfQuiet(at time.Time) (lp *lastPlayInfo, cleared bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.resumeAttempts == 0 || !p.lastResumeAt.Equal(at) {
		return nil, false
	}
	p.resumeAttempts = 0
	p.lastResumeAt = time.Time{}
	return p.last, true
}

// stuckBufEpisode reads the URL on record and when the stuck-buffering rescue
// last acted in one go, so the episode gate judges the stream the rescue
// would act on.
func (p *playSession) stuckBufEpisode() (boxURL string, lastRescue time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last != nil {
		boxURL = p.last.boxURL
	}
	return boxURL, p.stuckBufRescueAt
}

// noteStuckBufRescue stamps when the stuck-buffering rescue acted.
func (p *playSession) noteStuckBufRescue(at time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stuckBufRescueAt = at
}

// setICYTitle remembers the live radio StreamTitle ("" forgets it).
func (p *playSession) setICYTitle(title string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastICYTitle = title
}

// icyTitle returns the most recent radio StreamTitle seen.
func (p *playSession) icyTitle() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastICYTitle
}

// setTuneInStation records the TuneIn station just resolved and forgets the
// ICY title in the same section: a title from the previous station must not
// reach this one.
func (p *playSession) setTuneInStation(st tuneInNowStation) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastICYTitle = ""
	p.tuneInStation = st
}

// tuneInNow reads the live ICY title and the resolved TuneIn station
// together for the now-playing answer.
func (p *playSession) tuneInNow() (title string, st tuneInNowStation) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastICYTitle, p.tuneInStation
}

// displayPushThrottled reports whether a display push landed less than
// minGap ago.
func (p *playSession) displayPushThrottled(minGap time.Duration) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.lastDisplayPush.IsZero() && time.Since(p.lastDisplayPush) < minGap
}

// claimDisplayPush stamps a display push and hands back the play it is for
// (the pointer, so the push can later be keyed on it by noteDisplayShown);
// nil, and nothing stamped, when nothing is on record.
func (p *playSession) claimDisplayPush() *lastPlayInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last == nil {
		return nil
	}
	p.lastDisplayPush = time.Now()
	return p.last
}

// noteDisplayShown records that shown is on the display for play lp, so an
// identical follow-up push can be skipped (see displayAlreadyShows).
func (p *playSession) noteDisplayShown(lp *lastPlayInfo, shown string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastDisplayShown, p.lastDisplayFor = shown, lp
}

// displayAlreadyShows reports whether the last successful display push for
// the CURRENT play put exactly shown on screen. A new play (a new record)
// never counts as shown, so a station switch always gets its first push.
func (p *playSession) displayAlreadyShows(shown string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return shown != "" && p.last != nil && p.lastDisplayFor == p.last && p.lastDisplayShown == shown
}
