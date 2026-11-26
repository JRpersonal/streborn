// A press on a hardware preset key the speaker itself has lost (#1931).
//
// The firmware's own preset list can go empty while STR's store still holds
// every key: after a re-read of the presets from marge, or a power cycle that
// dropped the registrations. The reconcile puts the keys back, but its forced
// pass is held on a source the listener chose (userChosenSource) for as long
// as the speaker stays there, and it is deferred in standby. A ST10 that woke
// straight back onto Pandora kept six dead keys for its whole session: every
// press of key 1 or 3 blinked amber and did nothing.
//
// An empty key emits no selection frame at all (FIRMWARE-NOTES.md, "An EMPTY
// key emits no frame at all"), and on the sm2 chassis it gets no tap verdict
// either: the only trace is the analytics press line. That is also what a
// hold looks like there, which is why statsHold read the press as a hold and
// then found nothing to save on a source the speaker plays itself.
//
// So a physical press is a DEAD KEY when nothing (no activation, no hold
// verdict, no store of the firmware's own) came of it, STR's store holds a
// preset for the key, and the speaker's own /presets list (read fresh at that
// moment, one read per such press) lacks it. STR then plays the key itself
// through the app's recall path (webui PlaySlotForKey, the "preset slot recall
// (app)" lines), and asks the reconcile for the key re-sync. The press is the
// listener's consent to leave the source that plays, so that one re-sync is
// not held on it (resyncAsks.requestDeadKey, deadKeyConsent).
//
// The trade-off: on the sm2 chassis a tap and a hold of a dead key look the
// same while STR's own stream plays (the firmware refuses a UPnP store without
// a line). A dead key is read as a tap: the listener sees the key holding a
// station in the app and wants it, and saving the stream over it would lose
// that station. On a source the speaker plays itself, a hold still stores
// through the firmware's own gesture, whose PUT reaches the marge keeper
// (NoteFirmwareStore); that store cancels the dead-key play. A key empty in
// BOTH the store and the speaker's list is no dead key and keeps the
// hold-to-save behaviour (statsHold).

package main

import (
	"context"
	"strconv"
	"time"

	"github.com/JRpersonal/streborn/internal/oled"
	"github.com/JRpersonal/streborn/internal/presets"
)

const (
	// deadKeyDecideAfter is when, after the press, a dead key is played. The
	// firmware's own store of a held key reaches marge after its hold verdict
	// (about 1.6 s) within a second or two (heldNoticeWindow), so by then a
	// hold on a source the speaker stores itself has shown itself.
	deadKeyDecideAfter = 4 * time.Second
	// deadKeyHoldFrom is how long a press whose release the key trace
	// reported may have lasted and still be a tap (the ST10's tap limit is
	// under 1.75 s, the hold verdict comes at about 1.6 s).
	deadKeyHoldFrom = 1500 * time.Millisecond
)

// deadKeyVerdict is the agent's answer for a key a press produced nothing on.
type deadKeyVerdict struct {
	Dead bool
	// Name and Type describe the store's preset on the key.
	Name string
	Type string
	// Listed is how many keys the speaker's own list holds.
	Listed int
	// Source is what the speaker plays at the moment of the decision.
	Source string
	// Why says why the key is (or is not) dead, for the log.
	Why string
	// Double is set when the press was the second of two quick presses of
	// the key (doublePressWindow): the listener asked for the key's second
	// row, exactly as on a key the speaker still holds (#1110). Gap is the
	// time between the two presses.
	Double bool
	Gap    time.Duration
}

// deadKeyDoubleLocked reports whether press p is the second half of a double
// press: the press right before it was a physical press of the same key, no
// hold and no key combination, at most doublePressWindow earlier. A dead key
// emits no selection frame, so the frame-based detector (doublepress.go) never
// sees it; the key trace's presses are all there is, and they are physical by
// construction, so inside a group too. Callers hold h.mu.
func (h *holdSaver) deadKeyDoubleLocked(p holdPress) (bool, time.Duration) {
	prev := h.prev
	if prev.seq == 0 || prev.seq+1 != p.seq || prev.slot != p.slot || !prev.producer.Physical() ||
		prev.used || prev.combo || p.at.Before(prev.at) {
		return false, 0
	}
	gap := p.at.Sub(prev.at)
	if gap > doublePressWindow {
		return false, 0
	}
	return true, gap
}

// deadKeyFromList decides from the store's preset for slot (have false: the
// store holds nothing there) and the speaker's own list whether a press of
// slot hit a dead key. Pure, for the tests.
func deadKeyFromList(slot int, p presets.Preset, have bool, entries []boxPresetEntry, listErr error) deadKeyVerdict {
	if !presets.IsBoxSlot(slot) || !have {
		return deadKeyVerdict{Why: "the store holds nothing on this key"}
	}
	v := deadKeyVerdict{Name: p.Name, Type: p.Type}
	if listErr != nil {
		v.Why = "the speaker's key list is unreadable: " + listErr.Error()
		return v
	}
	v.Listed = len(entries)
	for _, e := range entries {
		if e.Slot == slot {
			v.Why = "the speaker lists this key"
			return v
		}
	}
	v.Dead = true
	v.Why = "the store holds a preset on this key, the speaker's own list lacks it"
	return v
}

// deadKeyOnBox is deadKeyFromList on the live store and speaker: the store
// first (no speaker read for a key it does not hold), then one /presets read.
func deadKeyOnBox(store *presets.Store, boxHost string, slot int) deadKeyVerdict {
	if store == nil {
		return deadKeyVerdict{Why: "no preset store"}
	}
	p, have := store.Get(slot)
	if !have || !presets.IsBoxSlot(slot) {
		return deadKeyFromList(slot, p, have, nil, nil)
	}
	entries, err := fetchBoxPresetsFullFn(boxHost)
	v := deadKeyFromList(slot, p, have, entries, err)
	if v.Dead {
		v.Source = boxNowPlayingSourceFn(boxHost)
	}
	return v
}

// NoteFirmwareStore records that the firmware's own hold-to-store put an item
// for slot to marge (the keeper, holdstore.go): that press was a hold.
func (h *holdSaver) NoteFirmwareStore(slot int) {
	h.mu.Lock()
	h.fwStoreSlot, h.fwStoreAt = slot, h.now()
	h.mu.Unlock()
}

// deadKeyCandidateLocked reports whether nothing came of press p so far that
// rules out a dead key: it is still the newest press, no hold was paired with
// it or read from it, it is no key combination, the speaker activated nothing
// and gave no hold verdict since shortly before it, a key trace release does
// not make it a hold, and the firmware stored nothing on the key since.
// Callers hold h.mu.
func (h *holdSaver) deadKeyCandidateLocked(p holdPress) (ok bool, why string) {
	from := p.at.Add(-holdVetoLookback)
	cur := h.press
	switch {
	case cur.seq != p.seq:
		return false, "a newer press"
	case cur.used:
		return false, "the press was a hold"
	case cur.combo:
		return false, "a key combination"
	case !h.selectAt.IsZero() && !h.selectAt.Before(from):
		return false, "the speaker activated a key"
	case h.verdictHold && !h.verdictAt.IsZero() && !h.verdictAt.Before(from):
		return false, "the speaker read the press as a hold"
	case !cur.released.IsZero() && cur.released.Sub(cur.at) >= deadKeyHoldFrom:
		return false, "the key was held"
	case h.fwStoreSlot == p.slot && !h.fwStoreAt.IsZero() && !h.fwStoreAt.Before(p.at):
		return false, "the speaker stored what plays on the key itself"
	}
	return true, ""
}

// deadKeyPress is the dead-key decision for physical press p, from statsHold
// (the analytics-line press, before it is read as a hold) or from the
// one-shot armed for every other physical press. It reports whether the key
// is dead: then the press is no hold whatever follows, and it is played once
// deadKeyDecideAfter has passed, unless a store, an activation or a newer
// press shows up first.
func (h *holdSaver) deadKeyPress(p holdPress) bool {
	if h.deadKey == nil || h.playDeadKey == nil || !p.producer.Physical() {
		return false
	}
	h.mu.Lock()
	ok, _ := h.deadKeyCandidateLocked(p)
	h.mu.Unlock()
	if !ok {
		return false
	}
	v := h.deadKey(p.slot)
	if !v.Dead {
		return false
	}
	if wait := p.at.Add(deadKeyDecideAfter).Sub(h.now()); wait > 0 {
		h.sleep(wait)
	}
	// The group's main speaker got the same remote press: it was meant for
	// that one, and this speaker plays nothing of its own for it
	// (groupoverride.go). Answered as decided, so no hold is read from it.
	if h.masterOverrides(p, "the dead key's play") {
		return true
	}
	h.mu.Lock()
	ok, why := h.deadKeyCandidateLocked(p)
	if ok {
		h.press.used = true
		h.press.dead = true
		v.Double, v.Gap = h.deadKeyDoubleLocked(p)
	}
	screen := h.press.seq == p.seq && h.press.screen
	h.mu.Unlock()
	if screen {
		h.screen.Cancel(p.slot, "dead key")
	}
	if !ok {
		h.logger.Info("dead key: the press on a key the speaker lost turned out to be something else, not playing it",
			"slot", p.slot, "source", v.Source, "because", why)
		return true
	}
	h.logger.Info("dead key: the speaker lost key "+strconv.Itoa(p.slot)+" and the press did nothing, STR plays it and re-registers the keys",
		"slot", p.slot, "source", v.Source, "why", v.Why, "name", v.Name, "type", v.Type,
		"listedKeys", v.Listed, "producer", p.producer.String(), "origin", string(p.origin),
		"double", v.Double, "gapMs", v.Gap.Milliseconds())
	h.spawn(func() { h.playDeadKey(p.slot, v) })
	return true
}

// checkDeadKey is the one-shot for a physical press the analytics line did
// not report (statsHold covers those).
func (h *holdSaver) checkDeadKey(p holdPress) { h.deadKeyPress(p) }

// playDeadKey plays a dead key through the app's recall path and asks for the
// key re-sync the press consents to (deadkey.go header). A double press plays
// the key's second row when that is set, as it does on a key the speaker still
// holds; with an empty second row the key plays its own preset, also as there.
func (h *presetWsHandler) playDeadKey(ctx context.Context, slot int, v deadKeyVerdict) {
	h.pressSeq.Add(1)
	h.firstPress.cancel()
	noteRecallActivity()
	if h.noteUserPlay != nil {
		h.noteUserPlay()
	}
	h.resync.requestDeadKey(h.logger, slot, v.Source)
	if h.playSlotFn == nil {
		h.logger.Warn("dead key: no recall path wired, the key cannot play", "slot", slot)
		return
	}
	play, notice := deadKeyTarget(h.store, slot, v)
	if play != slot {
		h.logger.Info("dead key: double press on key "+strconv.Itoa(slot)+" -> row 2 slot "+strconv.Itoa(play)+" '"+notice+"'",
			"key", slot, "slot", play, "gapMs", v.Gap.Milliseconds())
		notice = "2x " + notice
	} else if v.Double {
		h.logger.Info("dead key: double press on key "+strconv.Itoa(slot)+": row 2 slot "+strconv.Itoa(presets.Row2Slot(slot))+" empty, playing row 1",
			"key", slot, "gapMs", v.Gap.Milliseconds())
	}
	showNotice(oled.PresetPlaying(slot, notice))
	playCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	name, err := h.playSlotFn(playCtx, play)
	if err != nil {
		h.logger.Warn("dead key: slot "+strconv.Itoa(play)+" did not start", "slot", play, "key", slot, "source", v.Source, "err", err)
		return
	}
	h.logger.Info("dead key: playing slot "+strconv.Itoa(play)+" '"+name+"'", "slot", play, "key", slot, "was", v.Source)
}

// deadKeyTarget picks the slot a dead key's press plays and the name for its
// notice: the key's second row on a double press when that row is set, the
// key's own slot otherwise.
func deadKeyTarget(store *presets.Store, slot int, v deadKeyVerdict) (int, string) {
	if v.Double && store != nil && presets.IsBoxSlot(slot) {
		if p, ok := store.Get(presets.Row2Slot(slot)); ok {
			return p.Slot, p.Name
		}
	}
	return slot, v.Name
}
