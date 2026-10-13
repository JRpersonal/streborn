package oled

// The held-key screen: one panel session for a preset key held down, from
// shortly before the firmware's own hold reaction until the outcome of the
// save has been shown.
//
// Holding a preset key while STR plays something the firmware cannot store
// (Spotify, a library file or folder: a UPnP push) makes the firmware draw
// its own "action not available" message at about 2.0 s after the press. STR
// saves the item anyway, but its outcome notice comes 0.5 to 3 s later, so
// the user first saw the firmware's error and only then STR's SAVED. The
// agent therefore starts this session heldScreenAfter (1.8 s) after the press
// (cmd/agent holdsave.go) while the key is still down: STR then holds
// BoseApp's flip thread before the firmware's message is drawn, and the
// message never reaches the panel.
//
// The outcome (SAVED N, ON KEY M, NOT SAVED, KEY N EMPTY) is posted through
// Show like any other notice; while a session for that key runs, the queue
// hands it to the session instead, which swaps the picture in the same panel
// hold: no release in between (BoseApp could repaint) and no start-rate gap.
//
// BoseApp keeps running while its flip thread is held, so the firmware's
// message is still drawn into BoseApp's own frame and stays there until the
// firmware's timer takes it down; at the release BoseApp's current frame is
// flipped again. The session is therefore kept at least until that message
// must be gone: heldMinSession after its start (the message comes at 2.0 s
// after the press and lasts 2 to 3 s), and heldReleaseLinger after the key's
// release, because the firmware also reacts at the release (a marge_error at
// the very moment of the release was seen on the first field attempt).

import (
	"strconv"
	"sync"
	"time"
)

const (
	// heldOutcomeWait bounds the held screen without an outcome: a save
	// that has not answered by then ends the screen, and a late outcome
	// shows as an ordinary notice. The save starts 2.5 s after the press
	// (holdConfirmAfter), the screen at 1.8 s, so this leaves the save
	// about 5 s.
	heldOutcomeWait = 6 * time.Second
	// heldMinSession is the least time a session with an outcome keeps the
	// panel from its start. The screen starts 1.8 s after the press, the
	// firmware draws its message at 2.0 s and keeps it for 2 to 3 s, so it
	// is gone by 5.0 s after the press, 3.2 s into the session; the rest is
	// margin.
	heldMinSession = 4 * time.Second
	// heldReleaseLinger keeps the panel after the key's release for as long
	// as a firmware message drawn at the release would stay.
	heldReleaseLinger = 3 * time.Second
	// heldMaxSession caps one session whatever the key does (a key held for
	// a minute must not keep BoseApp's screen away that long).
	heldMaxSession = 12 * time.Second
	// heldPanelPoll is how often a session waiting for the panel looks
	// again; a session is time critical, a plain notice polls at 500 ms.
	heldPanelPoll = 100 * time.Millisecond
)

// NoticeKeyHeld is the held-key screen.
const NoticeKeyHeld NoticeKind = "key-held"

// KeyHeld is the screen while a preset key is held: the key with a busy
// badge, KEY N, SAVING...
func KeyHeld(key int) Notice {
	return keyNotice(NoticeKeyHeld, iconKeyHold, key, "KEY "+strconv.Itoa(key), "SAVING...", heldOutcomeWait, 2)
}

// heldSession is one held-key panel session. All fields are guarded by the
// queue's mu.
type heldSession struct {
	key     int
	pressAt time.Time
	screen  Notice

	start      time.Time // on the panel since; zero while it waits
	outcome    *Notice
	outcomeAt  time.Time
	releasedAt time.Time
	// cancel is a soft end: it only ends a session no outcome reached yet.
	cancel string
	// ended is a hard end (replaced by a newer press, the panel taken).
	ended string
	over  bool
	// why is the reason the session ended, for the log.
	why string
}

// takes reports whether n is the outcome of this held key.
func (s *heldSession) takes(n Notice) bool {
	switch n.Kind {
	case NoticeSavedByKey, NoticeNotSaved, NoticeEmptyKey:
		return n.Key == s.key
	case NoticeAlreadyOnKey:
		// names the OTHER key the item sits on
		return true
	}
	return false
}

// endAt is when a session with an outcome hands the panel back.
func (s *heldSession) endAt() time.Time {
	end := s.outcomeAt.Add(s.outcome.Hold)
	if m := s.start.Add(heldMinSession); m.After(end) {
		end = m
	}
	if !s.releasedAt.IsZero() {
		if r := s.releasedAt.Add(heldReleaseLinger); r.After(end) {
			end = r
		}
	}
	if c := s.start.Add(heldMaxSession); end.After(c) {
		end = c
	}
	return end
}

// KeyHoldStart starts the held-key screen for key, pressed at pressAt. It
// returns at once; a running ordinary notice (anything but identify) gives
// way, and so does an older held-key session.
func KeyHoldStart(key int, pressAt time.Time) { notices.holdStart(key, pressAt) }

// KeyHoldReleased tells the held-key session of key that the key went up.
func KeyHoldReleased(key int, at time.Time) { notices.holdReleased(key, at) }

// KeyHoldCancel ends the held-key session of key unless an outcome already
// reached it (a tap after all, a save that gave up without a word).
func KeyHoldCancel(key int, reason string) { notices.holdCancel(key, reason) }

func (q *noticeQueue) holdStart(key int, pressAt time.Time) {
	q.startOnce.Do(func() { go q.loop() })
	s := &heldSession{key: key, pressAt: pressAt, screen: KeyHeld(key)}
	q.mu.Lock()
	if old := q.held; old != nil && !old.over {
		old.ended = "replaced by a newer held key"
	}
	q.held, q.heldReq = s, s
	if q.showing && q.curSession == nil && q.cur.prio < 3 {
		// An ordinary notice gives way at once, and is not shown again: the
		// person is doing something new at the speaker.
		q.requeue = false
		quit, once := q.curQuit, q.quitOnce
		q.mu.Unlock()
		once.Do(func() { close(quit) })
	} else {
		q.mu.Unlock()
	}
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *noticeQueue) holdReleased(key int, at time.Time) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if s := q.held; s != nil && !s.over && s.key == key && s.releasedAt.IsZero() {
		s.releasedAt = at
	}
}

func (q *noticeQueue) holdCancel(key int, reason string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if s := q.held; s != nil && !s.over && s.key == key && s.outcome == nil && s.cancel == "" {
		s.cancel = reason
	}
}

// attach hands n to the running held-key session when it is that key's
// outcome. Called with q.mu held; reports whether n was taken.
func (q *noticeQueue) attach(n Notice) bool {
	s := q.held
	if s == nil || s.over || s.ended != "" || !s.takes(n) {
		return false
	}
	if s.outcome != nil && s.outcome.same(n) {
		return true
	}
	now := q.now()
	s.outcome, s.outcomeAt = &n, now
	if !s.start.IsZero() {
		noticeLogger().Info("display held-key screen handed over", "key", s.key, "kind", string(n.Kind),
			"line1", n.Line1, "line2", n.Line2,
			"heldMs", now.Sub(s.start).Milliseconds(), "sincePressMs", now.Sub(s.pressAt).Milliseconds())
	}
	return true
}

// sessionFrame says what the session shows now: the notice, how long it has
// been showing, and false once the session is over.
func (q *noticeQueue) sessionFrame(s *heldSession) (Notice, time.Duration, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if s.over {
		return Notice{}, 0, false
	}
	now := q.now()
	switch {
	case s.ended != "":
		s.over, s.why = true, s.ended
	case s.outcome == nil && s.cancel != "":
		s.over, s.why = true, s.cancel
	case s.outcome == nil && now.Sub(s.start) >= heldOutcomeWait:
		s.over, s.why = true, "no outcome in time"
	case s.outcome == nil:
		return s.screen, now.Sub(s.start), true
	case !now.Before(s.endAt()):
		s.over, s.why = true, "outcome shown"
	default:
		// The outcome replaces the held screen at full level: a fade from
		// black would be a visible gap.
		return *s.outcome, now.Sub(s.outcomeAt) + time.Duration(noticeFadeIn*float64(time.Second)), true
	}
	return Notice{}, 0, false
}

// runSession puts a held-key session on the panel and keeps it there until
// sessionFrame says it is over.
func (q *noticeQueue) runSession(s *heldSession) {
	skip := func(reason string) {
		q.mu.Lock()
		s.over, s.why = true, reason
		if q.held == s {
			q.held = nil
		}
		out := s.outcome
		q.mu.Unlock()
		noticeLogger().Debug("display notice skipped", "kind", string(NoticeKeyHeld), "key", s.key, "reason", reason)
		if out != nil {
			// The outcome must not be lost with the screen.
			q.post(*out)
		}
	}
	q.mu.Lock()
	gone := s.ended != "" || s.cancel != "" && s.outcome == nil
	q.mu.Unlock()
	if gone {
		skip("ended before it showed")
		return
	}
	if !q.enabled() {
		skip("off")
		return
	}
	if !q.supported() {
		skip("unsupported panel")
		return
	}
	deadline := q.now().Add(heldOutcomeWait)
	for {
		if q.gameActive() {
			skip("busy")
			return
		}
		if q.ready() && q.acquire() {
			if !q.gameActive() {
				break
			}
			q.release()
			skip("busy")
			return
		}
		q.mu.Lock()
		gone := s.ended != "" || s.cancel != "" && s.outcome == nil
		q.mu.Unlock()
		if gone {
			skip("ended before it showed")
			return
		}
		if q.now().After(deadline) {
			skip("busy")
			return
		}
		q.sleep(heldPanelPoll)
	}
	quit := make(chan struct{})
	done := make(chan struct{})
	q.mu.Lock()
	now := q.now()
	s.start = now
	if s.outcome != nil {
		s.outcomeAt = now
	}
	q.showing, q.cur, q.curSession = true, s.screen, s
	q.curQuit, q.curDone, q.quitOnce, q.requeue = quit, done, &sync.Once{}, false
	q.lastStart = now
	outcome := s.outcome
	q.mu.Unlock()
	noticeLogger().Info("display held-key screen started", "key", s.key,
		"sincePressMs", now.Sub(s.pressAt).Milliseconds(), "outcomeReady", outcome != nil)
	err := q.showSession(s, quit)
	q.release()
	q.mu.Lock()
	end := q.now()
	if !s.over {
		s.over, s.why = true, "panel taken"
	}
	q.showing, q.curSession = false, nil
	if q.held == s {
		q.held = nil
	}
	why, kind := s.why, ""
	if s.outcome != nil {
		kind = string(s.outcome.Kind)
	}
	q.mu.Unlock()
	close(done)
	if err != nil {
		noticeLogger().Warn("display notice ended early", "kind", string(NoticeKeyHeld), "key", s.key, "err", err)
	}
	noticeLogger().Info("display held-key screen ended", "key", s.key, "reason", why, "outcome", kind,
		"durationMs", end.Sub(now).Milliseconds(), "sincePressMs", end.Sub(s.pressAt).Milliseconds())
}

// playSession draws a held-key session on the panel.
func playSession(q *noticeQueue, s *heldSession, quit <-chan struct{}) error {
	var r *noticeRenderer
	var cur Notice
	return play(func(_ float64, buf []byte) bool {
		n, since, ok := q.sessionFrame(s)
		if !ok {
			return false
		}
		if r == nil || !n.same(cur) {
			r, cur = newNoticeRenderer(n), n
		}
		r.frame(since.Seconds(), buf)
		return true
	}, nil, heldMaxSession+time.Second, quit)
}
