package webui

import (
	"context"
	"time"
)

// A folder key pressed while its folder plays skips to the folder's next
// track (Jens, 2026-10-16). Only a person pressing the key again may do that.
//
// The speaker sends the same "preset selected" frame for things nobody
// pressed: it re-selects its own native preset while it wakes for the
// folder's start, it resumes its last preset on power-on, and inside a group
// it announces one press twice. Each of those frames used to skip a track.
// Field test 2026-11-08 (Portable, #1765 follow-up), key 5 holding a
// two-track folder, pressed on the sleeping speaker:
//
//	19:36:15.587 preset selected on the speaker slot=5      (the press)
//	19:36:24.284 preset slot recall (hardware): queue        (still waking)
//	19:36:28.816 preset selected on the speaker slot=5      (no key in the key trace)
//	19:36:36.332 queue start title="One Alpha"
//	19:36:48.345 queue advance why="next/previous pressed" endedTitle="One Alpha"
//
// The 28.816 frame found the folder loaded, decided "this key's folder plays,
// next track", and waited for the box command lock behind the folder's own
// start. When it got the lock, 15 s later and after the speaker had been
// switched off and on, it skipped the first track that had only just been
// handed over, and its push woke the speaker with track 2.
//
// So a selection only skips when it is a new press of the key:
//   - never while the folder's own start is still running (the frame is that
//     start, seen from the speaker);
//   - never for a press that came before the track that plays now was handed
//     over, or one that already skipped a track;
//   - when the speaker's key trace runs, only for a press it read;
//   - without a key trace, not in the first moments of a folder a power-on
//     resume continued (the speaker re-selects its key as it wakes);
//   - and the decision is checked again once the box command lock is held,
//     so a press that waited behind a start, an advance or a resume stands
//     down instead of skipping the track that took its place.

// SlotPress describes the selection frame behind a hardware preset recall.
type SlotPress struct {
	// At is when the speaker's selection frame arrived.
	At time.Time
	// KeyTrace, when set, returns the newest press of the key that a person
	// made on the speaker or its remote, as the speaker's own key trace read
	// it at or after since. found is false when the trace read none; known is
	// false when this speaker's trace cannot tell (not running, or it never
	// decoded a key). It may wait briefly for a trace line that trails the
	// frame.
	KeyTrace func(since time.Time) (at time.Time, found, known bool)
}

const (
	// folderKeyTraceLookback is how far before the selection frame the key
	// trace is searched for the press behind it. The frame follows the
	// release by about 0.13 s and a tap is well under a second; a held key is
	// the hold-to-save gesture, which selects nothing.
	folderKeyTraceLookback = 2 * time.Second
	// folderResumeReselectWindow is how long after a power-on resume started
	// a folder again a selection of its key is taken for the speaker
	// re-selecting the key as it wakes, on a speaker whose key trace cannot
	// tell. The field's re-selection came 12 s after the wake.
	folderResumeReselectWindow = 30 * time.Second
)

// pressEvidence is what the key trace says about a selection frame.
type pressEvidence int

const (
	pressUnknown     pressEvidence = iota // no key trace to ask
	pressPhysical                         // the trace read a press of the key
	pressNotPhysical                      // the trace runs and read no press
)

func (e pressEvidence) String() string {
	switch e {
	case pressPhysical:
		return "key-trace-press"
	case pressNotPhysical:
		return "no-key-press"
	}
	return "no-key-trace"
}

// evidence asks the key trace about the press behind the frame.
func (p SlotPress) evidence() (pressEvidence, time.Time) {
	if p.KeyTrace == nil {
		return pressUnknown, time.Time{}
	}
	at, found, known := p.KeyTrace(p.At.Add(-folderKeyTraceLookback))
	switch {
	case !known:
		return pressUnknown, time.Time{}
	case found:
		return pressPhysical, at
	}
	return pressNotPhysical, time.Time{}
}

// endQueueStarting marks the running queue start as done.
func (s *Server) endQueueStarting() {
	s.queueMu.Lock()
	s.queueStarting = false
	s.queueMu.Unlock()
}

// folderKeyNotARepress says why a selection of the key of the folder that
// plays is not a new press asking for the next track, "" when it is one.
// gen is the queue generation the verdict was taken on; ev and tracePress
// are the key trace's answer.
func (s *Server) folderKeyNotARepress(press SlotPress) (why string, gen int, ev pressEvidence, tracePress time.Time) {
	s.queueMu.Lock()
	starting := s.queueStarting
	gen = s.queueGen
	trackStart := s.queueTrackStart
	resumedAt := s.queueResumedAt
	usedPress := s.queueSkipPressAt
	s.queueMu.Unlock()
	if starting {
		return "the folder is still starting; the speaker selected its key again while it woke up for it", gen, pressUnknown, time.Time{}
	}
	if trackStart.IsZero() || !press.At.After(trackStart) {
		return "the track that plays now was handed to the speaker after this selection", gen, pressUnknown, time.Time{}
	}
	ev, tracePress = press.evidence()
	switch ev {
	case pressNotPhysical:
		return "the speaker selected the key by itself, its key trace shows no press of it (a power-on re-selection or an echo)", gen, ev, tracePress
	case pressPhysical:
		if !tracePress.After(trackStart) {
			return "the key press behind this selection came before the track that plays now (it started that track)", gen, ev, tracePress
		}
		if !usedPress.IsZero() && !tracePress.After(usedPress) {
			return "this key press already skipped a track (the speaker announced it twice)", gen, ev, tracePress
		}
	case pressUnknown:
		if !resumedAt.IsZero() && press.At.Sub(resumedAt) < folderResumeReselectWindow {
			return "the folder was continued after power-on moments ago; the speaker re-selects its key as it wakes", gen, ev, tracePress
		}
	}
	return "", gen, ev, tracePress
}

// repressFolderKey handles a selection of the key of the folder that plays:
// a new press skips to the next track, anything else leaves the folder
// playing. It reports whether the press is handled; false sends the recall
// on to start the folder from the top (the skip ran past the last track, or
// failed).
func (s *Server) repressFolderKey(slot int, press SlotPress) bool {
	why, gen, ev, tracePress := s.folderKeyNotARepress(press)
	if why != "" {
		s.logger.Info("preset slot recall (hardware): the key's folder already plays and this is no new press of the key, the folder plays on",
			"slot", slot, "why", why, "press", ev.String())
		return true
	}
	it, ok, stale, err := s.skipFolderForPress(slot, gen, press.At, tracePress)
	switch {
	case stale != "":
		s.logger.Info("preset slot recall (hardware): the press waited for the speaker and the folder moved on meanwhile, not skipping",
			"slot", slot, "why", stale, "waitedMs", time.Since(press.At).Milliseconds())
		return true
	case err != nil:
		s.logger.Warn("preset slot recall (hardware): next track failed, starting the folder again",
			"slot", slot, "err", err)
		return false
	case ok:
		s.logger.Info("preset slot recall (hardware): the key's folder already plays, next track",
			"slot", slot, "title", it.Title, "press", ev.String())
		return true
	}
	return false
}

// skipFolderForPress skips to the folder's next track for a press, once it
// holds the box command lock, and only if the folder is still where the
// press found it: the same generation (no start, advance or resume since),
// still the key's folder, its track handed over before the press. stale says
// why it stood down instead.
func (s *Server) skipFolderForPress(slot, gen int, pressAt, tracePress time.Time) (it queueItem, ok bool, stale string, err error) {
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	s.queueMu.Lock()
	moved := s.queueGen != gen || s.queueStarting || s.queueTrackStart.After(pressAt)
	s.queueMu.Unlock()
	switch {
	case !s.queue.isActive():
		return queueItem{}, false, "the folder ended", nil
	case moved:
		return queueItem{}, false, "a newer track or folder started", nil
	case !s.queuePlaysSlot(slot):
		return queueItem{}, false, "another folder plays now", nil
	}
	it, ok, err = s.queueSkipLocked(true)
	if ok && !tracePress.IsZero() {
		s.queueMu.Lock()
		s.queueSkipPressAt = tracePress
		s.queueMu.Unlock()
	}
	return it, ok, "", err
}

// resumeFolderKeyReselected handles a selection of a folder key whose folder
// does not play while a standby kept that folder for the next power-on: when
// the speaker selected the key by itself (its key trace shows no press), that
// is the speaker resuming its last key on power-on, and the folder continues
// at the track the standby ended it on instead of starting from the top. It
// reports whether it did. A press a person made, or one the key trace cannot
// tell, starts the folder from the top as before.
func (s *Server) resumeFolderKeyReselected(ctx context.Context, slot int, press SlotPress) bool {
	s.queueResumeMu.Lock()
	qr := s.queueResume
	s.queueResumeMu.Unlock()
	if qr == nil || (qr.slot != slot && qr.savedSlot != slot) {
		return false
	}
	if ev, _ := press.evidence(); ev != pressNotPhysical {
		return false
	}
	lp, ok := s.play.snapshot()
	if !ok {
		return false
	}
	kept := s.takeQueueResume(lp.boxURL, lp.ts)
	if kept == nil {
		return false
	}
	s.logger.Info("preset slot recall (hardware): the speaker selected the key by itself (no key press in its key trace) while the folder a standby ended waits on it, continuing that folder",
		"slot", slot)
	s.bumpRecallGen()
	s.ensureBoxReady(ctx)
	if err := s.resumeQueue(ctx, "preset slot recall (hardware)", kept); err != nil {
		s.logger.Warn("preset slot recall (hardware): the folder could not be continued", "slot", slot, "err", err)
	}
	return true
}
