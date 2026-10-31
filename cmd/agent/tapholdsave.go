// Tap a preset key, then hold it: save what played onto the key's second
// row (#1110).
//
// A double press of key n plays slot 6+n (doublepress.go). Saving onto that
// slot from the speaker is "double press, hold the second press": tap key n,
// press it again within a moment and keep it down. The gesture is built only
// from the signals the hold-to-save already reads (holdsave.go):
//
//   - The tap starts key n's own station at once and announces it with a
//     nowSelectionUpdated frame about 0.13 s after the release. That cannot be
//     prevented, so the moment that frame is handled is when STR notes what
//     played BEFORE the tap: the last now-playing document the bus carried
//     before the press (the firmware has already switched, a fresh read would
//     name key n's station) and the agent's in-memory record of its own play
//     (webui.SnapshotPlaying). Nothing is read from the speaker for it.
//   - The hold is the hold-to-save's own hold: a press line of key n paired
//     with the firmware's hold verdict (BCO, taigan), or on the sm2 chassis an
//     analytics press line that holdConfirmAfter later has neither a tap
//     verdict nor an activation (statsHold). The same holdConfirmAfter safety
//     applies before anything is written.
//   - The two belong together when the hold's press line lies within
//     tapHoldWindow after the tap's frame. The tap's own frame and tap
//     verdict, which make a plain hold right after a tap a "not saving" for
//     the hold-to-save, are what this gesture expects; only a tap verdict
//     after the hold's press, or a second frame of the key (a double press),
//     says it was no hold.
//
// Then slot 6+n gets what played before the tap, through the save paths the
// app uses (webui.SaveSnapshot): a duplicate on another key is refused like
// the app's save refuses it (no move, no swap), the display says where it
// sits, and that key plays so the station comes back. A save ends with slot
// 6+n playing through the double press's own path (PlaySlotForKey).
//
// Row 1 stays as it was: STR's hold-to-save never runs for this hold, and the
// firmware's own hold-to-store (it would store key n's own station on key n)
// is refused at marge for that key (tapHoldClaims, holdstore.go): refusing is
// loss-free, the firmware keeps its key as it was.
//
// A key combination (1 and volume down, holdsave.go comboKey) is never this
// gesture. Inside a zone or stereo pair one press can arrive as two frames,
// so there, as for the double press, the key trace has to show both presses
// (the tap and the hold); without it the gesture is off and the hold is what
// it was before.

package main

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxlog"
	"github.com/JRpersonal/streborn/internal/boxws"
	"github.com/JRpersonal/streborn/internal/oled"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/webui"
)

const (
	// tapHoldLineLag is how far a preset key's press line may trail the press
	// itself: the trace line was measured up to about a second behind on the
	// Portable (keyTraceWait), and the hold's press line is compared with the
	// tap's frame, which is not delayed that way.
	tapHoldLineLag = time.Second
	// tapHoldWindow is the latest a hold's press line may follow the tap's
	// selection frame and still make one gesture: the double press's window
	// on the speaker's own timeline, plus the line's lag. A plain hold of
	// that key so soon after a tap would store the key's own station on
	// itself, so nothing that made sense before is taken away.
	tapHoldWindow = doublePressWindow + tapHoldLineLag
	// tapHoldLead is how far before the tap's frame the hold's press line may
	// be read: the second key-down can come before the release+0.13 s frame
	// of a quick tap.
	tapHoldLead = 300 * time.Millisecond
	// tapNowPlayingGuard: a now-playing frame read this close before the
	// tap's selection frame may already be the firmware's switch to the
	// key's station (the key goes down 0.2 to 0.5 s before its frame), so
	// what played before the tap is taken from an older one.
	tapNowPlayingGuard = 600 * time.Millisecond
	// tapHoldPlayBudget bounds the save and the play of slot 6+n.
	tapHoldPlayBudget = 30 * time.Second
	// nowPlayingHistoryLen is how many now-playing frames are kept.
	nowPlayingHistoryLen = 6
)

// tapMark is the newest selection frame a person caused: the tap a hold may
// follow.
type tapMark struct {
	key int
	at  time.Time
	// frames counts this key's frames within doublePressWindow of the first:
	// 2 standalone is a double press, inside a group an echo.
	frames int
	// snap is what played before the tap.
	snap webui.PlayingSnapshot
}

// tapHoldWiring is what the tap+hold save needs beyond the hold saver. A nil
// save switches the gesture off.
type tapHoldWiring struct {
	// save stores the snapshot on a slot (webui.Server.SaveSnapshot).
	save func(ctx context.Context, snap webui.PlayingSnapshot, slot int) (webui.HoldSaved, error)
	// play starts slot for key the way the double press does
	// (presetWsHandler.playTapHoldSlot).
	play func(ctx context.Context, key, slot int)
	// grouped reports whether the speaker is (or may be) in a zone or pair.
	grouped func() (grouped, known bool)
	// tracePresses counts the key trace's physical presses of key since t.
	tracePresses func(key int, since time.Time) int
}

// NoteTap records a selection frame of key at at. own frames (STR's own
// presses and /select items) are no tap and clear the mark. snapshot is
// called once per new mark, synchronously: it must read memory only.
func (h *holdSaver) NoteTap(key int, at time.Time, own bool, snapshot func() webui.PlayingSnapshot) {
	if !presets.IsBoxSlot(key) {
		return
	}
	h.mu.Lock()
	if own {
		h.tap = tapMark{}
		h.mu.Unlock()
		return
	}
	if t := h.tap; t.key == key && !at.Before(t.at) && at.Sub(t.at) <= doublePressWindow {
		h.tap.frames++
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()
	var snap webui.PlayingSnapshot
	if snapshot != nil {
		snap = snapshot()
	}
	h.mu.Lock()
	h.tap = tapMark{key: key, at: at, frames: 1, snap: snap}
	h.mu.Unlock()
}

func (h *holdSaver) tapHoldEnabled() bool { return h.tapHold.save != nil }

// tapCandidateLocked reports whether press p follows the tap mark closely
// enough to be its hold, on timing and the verdicts alone. Callers hold h.mu.
func (h *holdSaver) tapCandidateLocked(p holdPress) (tapMark, bool) {
	t := h.tap
	if t.key == 0 || t.key != p.slot || p.at.IsZero() {
		return t, false
	}
	if d := p.at.Sub(t.at); d < -tapHoldLead || d > tapHoldWindow {
		return t, false
	}
	// A tap verdict after the hold's press line: that press was a tap. The
	// tap's own verdict comes at its release, before the second key-down,
	// and the syslog reader reads the lines in order.
	if !h.verdictAt.IsZero() && h.verdictAt.After(p.at) && !h.verdictHold {
		return t, false
	}
	return t, true
}

// tapHoldVerdict decides whether press p is the hold of a tap+hold. ok with
// the tap's mark; otherwise why is "" when p has nothing to do with a tap,
// and the reason the gesture was turned down when it does.
func (h *holdSaver) tapHoldVerdict(p holdPress) (t tapMark, ok bool, why string) {
	if !h.tapHoldEnabled() {
		return tapMark{}, false, ""
	}
	h.mu.Lock()
	t, cand := h.tapCandidateLocked(p)
	combo := p.combo || (h.press.seq == p.seq && h.press.combo)
	h.mu.Unlock()
	if !cand {
		return t, false, ""
	}
	if combo {
		return t, false, "a key combination"
	}
	grouped, known := true, false
	if h.tapHold.grouped != nil {
		grouped, known = h.tapHold.grouped()
	}
	if !grouped {
		if t.frames > 1 {
			// Two frames of the key standalone: a double press, which played
			// row 2 already.
			return t, false, "the key was pressed twice before the hold"
		}
		return t, true, ""
	}
	n := 0
	if h.tapHold.tracePresses != nil {
		n = h.tapHold.tracePresses(p.slot, t.at.Add(-doublePressTraceLookback))
	}
	switch {
	case n >= 2:
		return t, true, ""
	case n == 1:
		return t, false, "grouped, the key trace shows one press"
	}
	if !known {
		return t, false, "group state unknown, no key trace"
	}
	return t, false, "grouped without trace"
}

// tapHoldClaims reports whether the newest press of slot is the hold of a
// tap+hold, so the firmware's own store of that key must be refused (the
// marge keeper, holdstore.go). The firmware's PUT follows its hold verdict,
// so everything the verdict needs is known by then.
func (h *holdSaver) tapHoldClaims(slot int) bool {
	if !h.tapHoldEnabled() {
		return false
	}
	h.mu.Lock()
	p := h.press
	h.mu.Unlock()
	if p.slot != slot || p.at.IsZero() || p.combo || h.now().Sub(p.at) > heldNoticeWindow {
		return false
	}
	if !p.producer.Physical() && (p.origin != boxlog.OriginWeb || !h.appKeysAccepted()) {
		return false
	}
	_, ok, _ := h.tapHoldVerdict(p)
	return ok
}

// noteTapHoldCombo logs a combination that started with a tap+hold candidate,
// so the decision is in the log like every other one.
func (h *holdSaver) noteTapHoldCombo(p holdPress) {
	if !h.tapHoldEnabled() {
		return
	}
	h.mu.Lock()
	_, cand := h.tapCandidateLocked(p)
	h.mu.Unlock()
	if cand {
		h.logger.Info("tap+hold on key "+strconv.Itoa(p.slot)+" ignored: a key combination, left to the speaker",
			"key", p.slot)
	}
}

// saveRowTwo stores what played before the tap on key's second row and plays
// it. Row 1 is never written here.
func (h *holdSaver) saveRowTwo(p holdPress, holdAt time.Time, t tapMark) {
	key := p.slot
	slot := presets.Row2Slot(key)
	prefix := "tap+hold on key " + strconv.Itoa(key)
	attrs := []any{"key", key, "slot", slot, "producer", p.producer.String(),
		"tapToPressMs", p.at.Sub(t.at).Milliseconds(), "holdAfterPressMs", holdAt.Sub(p.at).Milliseconds()}
	if !t.snap.Savable() {
		h.logger.Info(prefix+" -> nothing to save, nothing that can be kept played before the tap",
			append(attrs, "before", t.snap.Source, "code", t.snap.Code)...)
		h.notify(oled.NotSaved(key))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), tapHoldPlayBudget)
	defer cancel()
	saved, err := h.tapHold.save(ctx, t.snap, slot)
	var refusal *webui.HoldSaveError
	switch {
	case errors.As(err, &refusal) && refusal.Code == "already-on-slot" && presets.ValidSlot(refusal.OtherSlot):
		h.logger.Info(prefix+" -> '"+t.snap.Name()+"' is already on slot "+strconv.Itoa(refusal.OtherSlot)+", not saved on row 2",
			append(attrs, "kind", t.snap.Kind, "otherSlot", refusal.OtherSlot, "otherName", refusal.OtherName)...)
		h.notify(oled.AlreadyOnKey(refusal.OtherSlot))
		// The station comes back from where it sits. Key n's own station,
		// which the tap started, already plays.
		if refusal.OtherSlot != key && h.tapHold.play != nil {
			h.tapHold.play(ctx, key, refusal.OtherSlot)
		}
	case errors.As(err, &refusal):
		h.logger.Info(prefix+" -> not saved, the preset rules refused it",
			append(attrs, "kind", t.snap.Kind, "code", refusal.Code, "reason", refusal.Error())...)
		h.notify(oled.NotSaved(key))
	case err != nil:
		h.logger.Warn(prefix+" -> the save failed", append(attrs, "err", err)...)
		h.endHeldScreen(key, "save failed")
	default:
		h.logger.Info(prefix+" -> saved '"+saved.Name+"' on row 2 slot "+strconv.Itoa(slot),
			append(attrs, "kind", saved.Kind, "unchanged", saved.Unchanged)...)
		h.notify(oled.SavedOnRowTwo(key, saved.Name))
		if h.tapHold.play != nil {
			h.tapHold.play(ctx, key, slot)
		}
	}
}

// nowPlayingHistory keeps the last few now-playing documents the bus carried,
// so a tap can say what played before it. Zero value ready.
type nowPlayingHistory struct {
	mu     sync.Mutex
	frames []boxws.NowPlayingFrame
}

func (n *nowPlayingHistory) add(f boxws.NowPlayingFrame) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.frames = append(n.frames, f)
	if len(n.frames) > nowPlayingHistoryLen {
		n.frames = append(n.frames[:0], n.frames[len(n.frames)-nowPlayingHistoryLen:]...)
	}
}

// seed records a document read over HTTP when the bus has carried none since
// the read began, so a tap soon after a reconnect still knows what played.
func (n *nowPlayingHistory) seed(raw []byte, readAt time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if k := len(n.frames); k > 0 && !n.frames[k-1].At.Before(readAt) {
		return
	}
	n.frames = append(n.frames, boxws.NowPlayingFrame{At: readAt, Raw: raw})
	if len(n.frames) > nowPlayingHistoryLen {
		n.frames = n.frames[len(n.frames)-nowPlayingHistoryLen:]
	}
}

// before returns the newest document that arrived before cut, nil when none.
func (n *nowPlayingHistory) before(cut time.Time) []byte {
	n.mu.Lock()
	defer n.mu.Unlock()
	for i := len(n.frames) - 1; i >= 0; i-- {
		if n.frames[i].At.Before(cut) {
			return n.frames[i].Raw
		}
	}
	return nil
}

// OnNowPlayingFrame keeps the bus's now-playing documents (boxws optional
// hook) and hands each one to the webui's woken-stop mark. Must not block.
func (h *presetWsHandler) OnNowPlayingFrame(f boxws.NowPlayingFrame) {
	h.nowPlaying.add(f)
	if h.onNowPlaying != nil {
		h.onNowPlaying(f.Raw)
	}
}

// noteTap hands a selection frame to the hold saver as a possible tap, with
// what played before it.
func (h *presetWsHandler) noteTap(key int, at time.Time) {
	if h.holdSaver == nil || !h.holdSaver.tapHoldEnabled() {
		return
	}
	h.holdSaver.NoteTap(key, at, h.isOwnSelection(key), func() webui.PlayingSnapshot {
		doc := h.nowPlaying.before(at.Add(-tapNowPlayingGuard))
		if h.snapshotFn == nil {
			return webui.PlayingSnapshot{Code: "no-web-server"}
		}
		return h.snapshotFn(doc)
	})
}

// isOwnSelection is ownSelection with the test seam.
func (h *presetWsHandler) isOwnSelection(key int) bool {
	if h.ownSelectionFn != nil {
		return h.ownSelectionFn(key)
	}
	return ownSelection(key)
}

// playTapHoldSlot plays slot after a tap+hold of key, superseding the tap's
// own recall of key's row-1 preset the way a double press does.
func (h *presetWsHandler) playTapHoldSlot(ctx context.Context, key, slot int) {
	h.pressSeq.Add(1)
	h.firstPress.cancel()
	if h.noteUserPlay != nil {
		h.noteUserPlay()
	}
	if h.playSlotFn == nil {
		h.logger.Warn("tap+hold: no recall path wired, the slot cannot play", "key", key, "slot", slot)
		return
	}
	name, err := h.playSlotFn(ctx, slot)
	if err != nil {
		h.logger.Warn("tap+hold on key "+strconv.Itoa(key)+": slot "+strconv.Itoa(slot)+" did not start",
			"key", key, "slot", slot, "err", err)
		return
	}
	h.logger.Info("tap+hold on key "+strconv.Itoa(key)+": playing slot "+strconv.Itoa(slot)+" '"+name+"'",
		"key", key, "slot", slot)
}
