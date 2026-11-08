package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/JRpersonal/streborn/internal/atomicfile"
	"github.com/JRpersonal/streborn/internal/presets"
)

// Continuing a music folder after the speaker was switched off and on (#1765).
//
// A standby ends the play queue: the speaker must not be woken again by the
// next track. The power-on resume then brought back the play on record, which
// for a folder is the one track that played when the speaker went off. Nothing
// started the queue again, so a 1000-track folder on a key, shuffled and on
// repeat, played that single song after every power-on and stopped.
//
// So a standby keeps the folder: its tracks, its play order (the shuffled one
// included), the position and the shuffle and repeat flags. A power-on resume
// whose target is still that folder's track starts the queue again at that
// track with that order, and falls back to the single track otherwise.
//
// The play record is the key that ties the two together: the kept folder
// belongs to the record of its last track (URL and timestamp). Any newer play
// replaces that record, so a folder can never come back over something that
// was played after it, and every way off a queue that is not a standby drops
// it outright.
//
// The play record is persisted on NAND (last-play.json) so the power-on resume
// survives the agent restart a long standby can bring (#119). A folder started
// from a key keeps that promise too, with a small file written once per
// standby: the key number, the position and the order as track indices. The
// tracks themselves are on the key already, and presets.json is big enough
// for a 1000-track folder without a second copy (the NAND budget of a queue
// preset is a known problem). A folder started in the Library is on no key and
// is kept in memory only: after an agent restart its last track comes back
// alone, as before.

// queueResumeFile is the NAND file a key's folder is kept in, next to
// last-play.json.
const queueResumeFile = "queue-resume.json"

// queueResume is the folder a standby ended.
type queueResume struct {
	// state is the queue as it was when the standby ended it. Its items are
	// nil for a folder read back from NAND; they come from the key then.
	state playQueueState
	// slot is the key the folder was started from while that key still holds
	// exactly its tracks, 0 for a folder on no key.
	slot int
	// card is the folder's Recently-played identity, savedSlot the key it was
	// saved on while it played.
	card      recentCardCtx
	savedSlot int
	// boxURL and playTS are the play record of the track that played when the
	// speaker went off, itemURL that track's own address in the folder.
	boxURL  string
	itemURL string
	playTS  time.Time
	// at is when the standby ended the folder.
	at time.Time
}

// persistedQueueResume is the NAND shape of a key's kept folder.
type persistedQueueResume struct {
	Slot    int       `json:"slot"`
	BoxURL  string    `json:"boxURL"`
	ItemURL string    `json:"itemURL"`
	PlayTS  time.Time `json:"playTS"`
	At      time.Time `json:"at"`
	Pos     int       `json:"pos"`
	// Order is the play order as track indices into the key's items, left
	// out when it is the key's own order (no shuffle).
	Order     []int  `json:"order,omitempty"`
	Tracks    int    `json:"tracks"`
	Shuffle   bool   `json:"shuffle,omitempty"`
	Repeat    string `json:"repeat"`
	SavedSlot int    `json:"savedSlot,omitempty"`
}

// queueResumePath is the NAND file of a key's kept folder, "" when the play
// record is not persisted either (tests, a box without a NAND path).
func (s *Server) queueResumePath() string {
	if s.lastPlayPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.lastPlayPath), queueResumeFile)
}

// slotQueueCard is the Recently-played card of the folder on key slot (#220).
func slotQueueCard(slot int, p presets.Preset) recentCardCtx {
	return recentCardCtx{key: fmt.Sprintf("queue:slot:%d", slot), name: p.Name, art: queueKeyCover(p), source: p.Source,
		recentKey: slotFolderRecentKey(slot, p)}
}

// identityOrder reports whether order plays the tracks in their own order.
func identityOrder(order []int) bool {
	for i, v := range order {
		if v != i {
			return false
		}
	}
	return true
}

// keepQueueForResume keeps the running folder for the next power-on, called
// as a standby ends it. A queue that already ended (the watcher saw the
// standby first) leaves the kept folder alone.
func (s *Server) keepQueueForResume(why string) {
	if s.queue == nil {
		return
	}
	st := s.queue.save()
	if !st.active || st.pos < 0 || st.pos >= len(st.order) || len(st.order) != len(st.items) {
		return
	}
	cur := st.items[st.order[st.pos]]
	lp, ok := s.play.snapshot()
	if !ok || lp.boxURL != queueBoxURL(cur.URL, cur.Mime) {
		// The play on record is not the folder's track, so a power-on resume
		// would not be about this folder either.
		s.dropQueueResume("the play on record is not the folder's track")
		return
	}
	s.queueMu.Lock()
	card, savedSlot := s.queueFolder, s.queueSavedSlot
	s.queueMu.Unlock()
	slot := 0
	if n, ok := queueSlotCardNum(card.key); ok && s.cardSlotHolds(n) {
		slot = n
	}
	qr := &queueResume{state: st, slot: slot, card: card, savedSlot: savedSlot,
		boxURL: lp.boxURL, itemURL: cur.URL, playTS: lp.ts, at: time.Now()}
	onNAND := slot != 0 && s.persistQueueResume(qr)
	s.queueResumeMu.Lock()
	wasOnNAND := s.queueResumeOnNAND
	s.queueResume, s.queueResumeOnNAND = qr, onNAND
	s.queueResumeMu.Unlock()
	if wasOnNAND && !onNAND {
		s.removeQueueResumeFile()
	}
	s.logger.Info("queue resume: kept the folder for the next power-on",
		"why", why, "tracks", len(st.items), "pos", st.pos, "shuffle", st.shuffle,
		"repeat", st.repeat.String(), "slot", slot, "onNAND", onNAND, "title", cur.Title)
}

// persistQueueResume writes a key's kept folder to NAND and reports whether
// it is there now.
func (s *Server) persistQueueResume(qr *queueResume) bool {
	path := s.queueResumePath()
	if path == "" {
		return false
	}
	pq := persistedQueueResume{Slot: qr.slot, BoxURL: qr.boxURL, ItemURL: qr.itemURL, PlayTS: qr.playTS, At: qr.at,
		Pos: qr.state.pos, Tracks: len(qr.state.items), Shuffle: qr.state.shuffle, Repeat: qr.state.repeat.String(),
		SavedSlot: qr.savedSlot}
	if !identityOrder(qr.state.order) {
		pq.Order = qr.state.order
	}
	b, err := json.Marshal(pq)
	if err != nil {
		return false
	}
	if err := atomicfile.WriteFile(path, b, 0o644); err != nil {
		s.logger.Warn("queue resume: could not keep the folder on NAND", "err", err)
		return false
	}
	return true
}

func (s *Server) removeQueueResumeFile() {
	path := s.queueResumePath()
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.logger.Warn("queue resume: could not remove the kept folder from NAND", "err", err)
	}
}

// dropQueueResume forgets the kept folder: the user moved on to something
// else, so the next power-on must not bring it back.
func (s *Server) dropQueueResume(why string) {
	s.queueResumeMu.Lock()
	qr, onNAND := s.queueResume, s.queueResumeOnNAND
	s.queueResume, s.queueResumeOnNAND = nil, false
	s.queueResumeMu.Unlock()
	if onNAND {
		s.removeQueueResumeFile()
	}
	if qr != nil {
		s.logger.Info("queue resume: the kept folder is dropped", "why", why)
	}
}

// loadQueueResume reads a key's kept folder back at agent start. It belongs to
// the play record loaded just before it; a file for any other record is stale.
func (s *Server) loadQueueResume() {
	path := s.queueResumePath()
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var pq persistedQueueResume
	lp := s.play.current()
	if json.Unmarshal(b, &pq) != nil || pq.Slot <= 0 || pq.Tracks <= 0 ||
		lp == nil || lp.boxURL != pq.BoxURL || !lp.ts.Equal(pq.PlayTS) || time.Since(pq.At) >= resumeMaxAge {
		s.removeQueueResumeFile()
		return
	}
	order := pq.Order
	if len(order) == 0 {
		order = make([]int, pq.Tracks)
		for i := range order {
			order[i] = i
		}
	}
	qr := &queueResume{
		state:     playQueueState{order: order, pos: pq.Pos, repeat: parseRepeat(pq.Repeat), shuffle: pq.Shuffle, active: true},
		slot:      pq.Slot,
		savedSlot: pq.SavedSlot,
		boxURL:    pq.BoxURL,
		itemURL:   pq.ItemURL,
		playTS:    pq.PlayTS,
		at:        pq.At,
	}
	s.queueResumeMu.Lock()
	s.queueResume, s.queueResumeOnNAND = qr, true
	s.queueResumeMu.Unlock()
	s.logger.Info("queue resume: the folder kept at the last standby is back from NAND",
		"slot", pq.Slot, "tracks", pq.Tracks, "pos", pq.Pos, "ageMin", int(time.Since(pq.At).Minutes()))
}

// takeQueueResume hands out the kept folder when the resume target (the play
// record a resume is about to push) is still its track, and forgets it either
// way: a folder is started again once, and a target that is not its track
// means something newer was played. nil means "push the single track".
func (s *Server) takeQueueResume(boxURL string, playTS time.Time) *queueResume {
	s.queueResumeMu.Lock()
	qr, onNAND := s.queueResume, s.queueResumeOnNAND
	s.queueResume, s.queueResumeOnNAND = nil, false
	s.queueResumeMu.Unlock()
	if onNAND {
		s.removeQueueResumeFile()
	}
	if qr == nil {
		return nil
	}
	switch {
	case qr.boxURL != boxURL || !qr.playTS.Equal(playTS):
		s.logger.Info("queue resume: something else was played after the folder, resuming that instead")
		return nil
	case time.Since(qr.at) >= resumeMaxAge:
		s.logger.Info("queue resume: the folder is too old to continue", "ageHours", int(time.Since(qr.at).Hours()))
		return nil
	}
	if qr.state.items == nil && !s.resolveQueueResumeKey(qr) {
		return nil
	}
	st := qr.state
	if st.pos < 0 || st.pos >= len(st.order) || len(st.order) != len(st.items) {
		return nil
	}
	if i := st.order[st.pos]; i < 0 || i >= len(st.items) || st.items[i].URL != qr.itemURL {
		s.logger.Info("queue resume: the folder no longer holds the track that played, resuming that track alone")
		return nil
	}
	return qr
}

// resolveQueueResumeKey fills in the tracks of a folder read back from NAND
// from its key, and reports whether the key still holds that folder.
func (s *Server) resolveQueueResumeKey(qr *queueResume) bool {
	if s.presets == nil || qr.slot <= 0 {
		return false
	}
	p, ok := s.presets.Get(qr.slot)
	if !ok || p.Type != "queue" {
		s.logger.Info("queue resume: the folder's key holds something else now, resuming the track alone", "slot", qr.slot)
		return false
	}
	items := presetItemsToQueue(p.Items)
	if len(items) != len(qr.state.order) {
		s.logger.Info("queue resume: the folder on the key changed, resuming the track alone",
			"slot", qr.slot, "tracks", len(items), "kept", len(qr.state.order))
		return false
	}
	seen := make([]bool, len(items))
	for _, i := range qr.state.order {
		if i < 0 || i >= len(items) || seen[i] {
			return false
		}
		seen[i] = true
	}
	qr.state.items = items
	if qr.card.key == "" {
		qr.card = slotQueueCard(qr.slot, p)
	}
	return true
}

// resumePushLocked plays a resume target: the folder a standby ended when the
// target is still that folder's track, started again at that track with its
// order and flags, else the single stream as the resume always did. label
// prefixes the log lines; auto marks an automatic resume (not the user asking
// for playback). folder reports which of the two played. The caller holds
// boxCmdMu.
func (s *Server) resumePushLocked(ctx context.Context, label string, auto bool, boxURL, title, art, mime string, playTS time.Time) (folder bool, err error) {
	if qr := s.takeQueueResume(boxURL, playTS); qr != nil {
		err := s.restartQueueLocked(ctx, label, qr, auto)
		if err == nil {
			return true, nil
		}
		s.logger.Warn(label+": the folder could not be started again, playing its last track alone", "err", err)
		s.stopQueue("the folder could not be started again after power-on")
	}
	if mime != "" {
		return false, s.renderer.PlayURLMime(ctx, boxURL, title, art, mime)
	}
	return false, s.renderer.PlayURL(ctx, boxURL, title, art)
}

// resumeQueue is resumePushLocked's folder half for a caller that does not
// hold boxCmdMu and has already taken the folder (the power-on endpoint).
func (s *Server) resumeQueue(ctx context.Context, label string, qr *queueResume) error {
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	if err := s.restartQueueLocked(ctx, label, qr, false); err != nil {
		s.stopQueue("the folder could not be started again after power-on")
		return err
	}
	return nil
}

// restartQueueLocked starts a kept folder again where the standby ended it.
// The caller holds boxCmdMu.
func (s *Server) restartQueueLocked(ctx context.Context, label string, qr *queueResume, auto bool) error {
	st := qr.state
	st.active = true
	start := st.order[st.pos]
	s.logger.Info(label+": continuing the folder the speaker was playing when it was switched off",
		"tracks", len(st.items), "pos", st.pos, "shuffle", st.shuffle, "repeat", st.repeat.String(),
		"slot", qr.slot, "title", st.items[start].Title)
	// A fresh play resets the auto-resume crash-loop guard (#381). The folder's
	// first push is recorded as one, but an automatic resume is no fresh user
	// play, so the count it took before this push is put back after it.
	var attempts int
	var at time.Time
	if auto {
		attempts, at = s.play.resumeGuard()
	}
	if qr.slot != 0 {
		s.armQueueRecall(qr.slot)
	}
	if err := s.runQueueStartLocked(ctx, queueStartSpec{items: st.items, start: start, shuffle: st.shuffle,
		rep: st.repeat, card: qr.card, resume: &st, userPlay: !auto}); err != nil {
		return err
	}
	if qr.savedSlot != 0 {
		s.queueMu.Lock()
		s.queueSavedSlot = qr.savedSlot
		s.queueMu.Unlock()
	}
	if auto && attempts > 0 {
		if lp := s.play.keepResumeGuard(attempts, at); lp != nil {
			s.persistLastPlay(lp, attempts, at)
		}
	}
	return nil
}
