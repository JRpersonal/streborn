// Holding a hardware preset key while STR's own stream plays (#1217).
//
// The firmware's hold-to-store (holdstore.go) only ever covers what the
// speaker plays by itself. STR's Spotify stream, a single music-library file
// and a library folder all reach the speaker as a UPnP push, and for a UPnP
// push the firmware refuses the gesture (a white double blink) and never asks
// marge to keep anything. Holding a key there did nothing at all.
//
// The hold is still visible. The key line (internal/boxlog keys.go) says
// which preset key went down and who pressed it, and the firmware's system
// controller says what it made of the press: EVT_KEY_PRESET_PRESS for a tap
// (at the release), EVT_KEY_PRESET_PRESS_AND_HOLD once the key has been down
// for about 1.6 s (presetgesture.go, measured on an ST10). Neither line alone
// is enough: the key line cannot tell a tap from a hold on the sm2 chassis,
// and the verdict does not name the key. Paired, they say "key N was held".
//
// A pairing is then confirmed against time before anything is written. The
// longest press the firmware still treats as a tap is under 1.75 s, and the
// activation that follows a tap lands about 0.25 s after the release, so
// nothing is saved before holdConfirmAfter has passed since the press, and a
// tap verdict or a preset activation anywhere in that window cancels it.

package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JRpersonal/streborn/internal/boxlog"
	"github.com/JRpersonal/streborn/internal/oled"
	"github.com/JRpersonal/streborn/internal/webui"
)

const (
	// holdPairWindow is how far apart a preset key's press line and the
	// firmware's hold verdict may lie and still describe one press. The
	// verdict follows the press by about 1.6 s (measured 1.5 to 1.75 s); the
	// analytics line on sm2 may trail its press, so the window allows for
	// either order and some lag.
	holdPairWindow = 4 * time.Second
	// holdConfirmAfter is the earliest moment after the press a save may run.
	// A press released before about 1.6 s is a tap, and its activation lands
	// about 0.25 s after the release (measured on an ST10: 0.13 s after a
	// 0.1 s tap, 0.23 s after a 1.5 s one), so by 2.5 s any tap has shown
	// itself.
	holdConfirmAfter = 2500 * time.Millisecond
	// holdVetoLookback extends the veto window to before the press: the tap
	// verdict for a press can be read before an analytics line that lags it.
	holdVetoLookback = time.Second
	// holdSaveBudget bounds one save end to end (a now-playing read, the
	// Spotify title lookup, the box registration).
	holdSaveBudget = 30 * time.Second
	// holdUnpairedCheckAfter is when, after a physical preset press, the
	// saver looks back once and says so if no hold verdict was paired with
	// it (#1217: a reporter's held key left no hold-to-save line at all).
	// The pair window plus a margin for a verdict read late.
	holdUnpairedCheckAfter = holdPairWindow + 500*time.Millisecond
	// emptyKeyCheckAfter is when, after a physical preset press, the saver
	// looks whether the key held anything at all (display notice "KEY N
	// EMPTY"). A tap is released and its activation announced well within it
	// (holdConfirmAfter), and a hold has its verdict by about 1.6 s.
	emptyKeyCheckAfter = 2 * time.Second
)

// holdSaveFunc stores what STR plays on a slot (webui.Server.HoldSaveLive).
type holdSaveFunc func(ctx context.Context, slot int) (webui.HoldSaved, error)

// holdPress is the newest preset key press the speaker reported.
type holdPress struct {
	slot     int
	producer boxlog.Producer
	origin   boxlog.Origin
	at       time.Time
	used     bool
	// seq numbers the presses so the unpaired check can tell whether the
	// press it was armed for is still the newest one.
	seq uint64
}

// holdSaver pairs preset presses with the firmware's hold verdict and saves
// what STR plays onto a held key. Zero value is not usable; see newHoldSaver.
type holdSaver struct {
	logger *slog.Logger
	save   holdSaveFunc
	now    func() time.Time
	sleep  func(time.Duration)
	// spawn runs the confirmation and save off the log reader's goroutine.
	// Tests run it inline.
	spawn func(func())
	// after runs f once after d (time.AfterFunc): the one-shot unpaired
	// check of a physical press. Tests fire it by hand.
	after func(d time.Duration, f func())
	// acceptAppUntil (unix nanos) is the test-only switch that lets a key
	// sent over :8090/key count like a physical one; 0 = off.
	acceptAppUntil atomic.Int64
	// notify puts a notice on the speaker display (oled.Show; tests record).
	notify func(oled.Notice)
	// emptyKey reports whether a key holds nothing at all, and whether that
	// is known (webui.Server.KeyEmpty). nil: the empty-key notice is off.
	emptyKey func(slot int) (empty, known bool)

	mu     sync.Mutex
	press  holdPress
	hold   time.Time // a hold verdict no press has been paired with yet
	vetoAt time.Time // newest tap verdict or preset activation
	// selectAt is the newest preset activation the speaker announced.
	selectAt time.Time
	// pressSeq counts preset presses (holdPress.seq).
	pressSeq uint64
	// verdictAt / verdictHold describe the newest tap/hold verdict, whatever
	// became of it, for the forensic lines.
	verdictAt   time.Time
	verdictHold bool
}

func newHoldSaver(logger *slog.Logger, save holdSaveFunc) *holdSaver {
	if logger == nil {
		logger = slog.Default()
	}
	return &holdSaver{
		logger: logger,
		save:   save,
		now:    time.Now,
		sleep:  time.Sleep,
		spawn:  func(f func()) { go f() },
		after:  func(d time.Duration, f func()) { time.AfterFunc(d, f) },
		notify: func(n oled.Notice) { showNotice(n) },
	}
}

// presetKeySlot maps PRESET_1..PRESET_6 to 1..6, 0 for every other key.
func presetKeySlot(name string) int {
	if len(name) == len("PRESET_1") && name[:7] == "PRESET_" && name[7] >= '1' && name[7] <= '6' {
		return int(name[7] - '0')
	}
	return 0
}

// NotePress records a preset key going down, from the key trace (physical
// keys) or the firmware's line for keys sent over its API.
func (h *holdSaver) NotePress(ev boxlog.KeyEvent) {
	slot := presetKeySlot(ev.Name)
	if slot == 0 || ev.State != boxlog.StatePressed {
		return
	}
	p := holdPress{slot: slot, producer: ev.Producer, origin: ev.Origin, at: ev.At}
	h.mu.Lock()
	h.pressSeq++
	p.seq = h.pressSeq
	hold := h.hold
	if !hold.IsZero() && ev.At.Sub(hold) < holdPairWindow {
		// The verdict was read first; this press line trails it.
		h.hold = time.Time{}
		p.used = true
		h.press = p
		h.mu.Unlock()
		h.paired(p, hold)
		return
	}
	h.press = p
	h.mu.Unlock()
	if p.producer.Physical() && h.after != nil {
		// One timer per human press, so bounded by how fast a person
		// presses; it only reads state and logs.
		h.after(holdUnpairedCheckAfter, func() { h.checkUnpaired(p) })
	}
	if h.emptyKey != nil && h.after != nil &&
		(p.producer.Physical() || (p.origin == boxlog.OriginWeb && h.appKeysAccepted())) {
		h.after(emptyKeyCheckAfter, func() { h.checkEmpty(p) })
	}
}

// checkEmpty is the one-shot look after a physical preset press: when the
// press is still the newest, no hold was paired with it, the speaker
// activated nothing since, and the key holds nothing in STR's store or the
// box's own list, the display says the key is empty and where to set it.
// The firmware itself shows nothing for an empty key.
func (h *holdSaver) checkEmpty(p holdPress) {
	h.mu.Lock()
	cur, sel := h.press, h.selectAt
	h.mu.Unlock()
	if cur.seq != p.seq || cur.used || (!sel.IsZero() && !sel.Before(p.at)) {
		return
	}
	empty, known := h.emptyKey(p.slot)
	if !known || !empty {
		return
	}
	h.logger.Info("preset key pressed while it holds nothing", "slot", p.slot, "producer", p.producer.String())
	h.notify(oled.EmptyKey(p.slot))
}

// recentPhysicalPress reports whether the newest preset press was a physical
// press of slot within d (the marge keeper's "was this a person's hold").
func (h *holdSaver) recentPhysicalPress(slot int, d time.Duration) bool {
	h.mu.Lock()
	p := h.press
	h.mu.Unlock()
	if p.slot != slot || p.at.IsZero() || h.now().Sub(p.at) > d {
		return false
	}
	return p.producer.Physical() || (p.origin == boxlog.OriginWeb && h.appKeysAccepted())
}

// checkUnpaired is the one-shot look back after a physical preset press: if
// no hold verdict was paired with it, say so, together with the newest
// verdict and how it relates to the press (#1217). Every tap ends here too,
// which is expected (a tap gets a tap verdict, never a hold one); a held key
// ending here is the case the line exists for.
func (h *holdSaver) checkUnpaired(p holdPress) {
	h.mu.Lock()
	cur := h.press
	verdictAt, verdictHold := h.verdictAt, h.verdictHold
	unclaimed := !h.hold.IsZero()
	h.mu.Unlock()
	if cur.seq == p.seq && cur.used {
		return
	}
	verdict, afterPressMs, ageMs := "none", int64(0), int64(-1)
	if !verdictAt.IsZero() {
		verdict = "tap"
		if verdictHold {
			verdict = "hold"
		}
		afterPressMs = verdictAt.Sub(p.at).Milliseconds()
		ageMs = h.now().Sub(verdictAt).Milliseconds()
	}
	h.logger.Info("hold-to-save: no hold verdict paired",
		"slot", p.slot, "producer", p.producer.String(), "origin", string(p.origin),
		"superseded", cur.seq != p.seq,
		"lastVerdict", verdict, "lastVerdictAgeMs", ageMs,
		"lastVerdictAfterPressMs", afterPressMs, "unclaimedHold", unclaimed)
}

// NoteGesture receives the firmware's tap/hold verdict on a preset key. Each
// verdict is logged at INFO: it only follows a human gesture or a key sent
// over :8090/key, so the line is bounded, and #1217 showed a held key that
// otherwise left no trace at all.
func (h *holdSaver) NoteGesture(g boxlog.PresetGesture) {
	h.mu.Lock()
	h.verdictAt, h.verdictHold = g.At, g.Hold
	p := h.press
	slot, ageMs := 0, int64(-1)
	if p.slot != 0 {
		slot, ageMs = p.slot, g.At.Sub(p.at).Milliseconds()
	}
	if !g.Hold {
		h.vetoAt = g.At
		h.hold = time.Time{}
		h.mu.Unlock()
		h.logger.Info("hold-to-save: the speaker read a preset key as a tap",
			"hold", false, "pressSlot", slot, "pressAgeMs", ageMs, "pressUsed", p.used)
		return
	}
	if p.slot != 0 && !p.used && !g.At.Before(p.at) && g.At.Sub(p.at) < holdPairWindow {
		h.press.used = true
		h.mu.Unlock()
		h.logger.Info("hold-to-save: the speaker read a preset key as a hold",
			"hold", true, "pressSlot", slot, "pressAgeMs", ageMs, "paired", true)
		h.paired(p, g.At)
		return
	}
	h.hold = g.At
	h.mu.Unlock()
	h.logger.Info("hold-to-save: the speaker reported a held preset key, no press to pair it with",
		"hold", true, "pressSlot", slot, "pressAgeMs", ageMs, "pressUsed", p.used)
}

// NoteSelection records a preset activation the speaker announced (a tap's
// consequence), which cancels any hold being confirmed.
func (h *holdSaver) NoteSelection() {
	h.mu.Lock()
	h.vetoAt = h.now()
	h.selectAt = h.vetoAt
	h.mu.Unlock()
}

// SetAcceptAppKeys is the test-only switch behind /api/debug/hold-save-test.
// It returns until when it is on.
func (h *holdSaver) SetAcceptAppKeys(on bool, d time.Duration) time.Time {
	if !on {
		h.acceptAppUntil.Store(0)
		return time.Time{}
	}
	until := h.now().Add(d)
	h.acceptAppUntil.Store(until.UnixNano())
	return until
}

func (h *holdSaver) appKeysAccepted() bool {
	u := h.acceptAppUntil.Load()
	return u != 0 && h.now().UnixNano() < u
}

// paired runs once a press and a hold verdict were matched.
func (h *holdSaver) paired(p holdPress, holdAt time.Time) {
	if !p.producer.Physical() {
		if p.origin != boxlog.OriginWeb || !h.appKeysAccepted() {
			// A key the app or STR itself sent: the hold is a person's
			// gesture at the speaker, never a network client's.
			h.logger.Info("hold-to-save: not saving, the held key came over the network, not from the speaker or its remote",
				"slot", p.slot, "producer", p.producer.String(), "origin", string(p.origin))
			return
		}
		h.logger.Info("hold-to-save: a key sent over the speaker's API counts as physical, the test switch is on",
			"slot", p.slot)
	}
	h.spawn(func() { h.confirmAndSave(p, holdAt) })
}

// confirmAndSave waits out the tap window, checks nothing contradicted the
// hold, and saves.
func (h *holdSaver) confirmAndSave(p holdPress, holdAt time.Time) {
	if wait := p.at.Add(holdConfirmAfter).Sub(h.now()); wait > 0 {
		h.sleep(wait)
	}
	from := p.at
	if holdAt.Before(from) {
		from = holdAt
	}
	from = from.Add(-holdVetoLookback)
	h.mu.Lock()
	veto := h.vetoAt
	h.mu.Unlock()
	if !veto.IsZero() && !veto.Before(from) {
		h.logger.Info("hold-to-save: not saving, the speaker activated a key or read the press as a tap",
			"slot", p.slot, "vetoAfterPressMs", veto.Sub(p.at).Milliseconds())
		return
	}
	if h.save == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), holdSaveBudget)
	defer cancel()
	saved, err := h.save(ctx, p.slot)
	var refusal *webui.HoldSaveError
	switch {
	case errors.Is(err, webui.ErrHoldNotUPnP):
		// The speaker's own hold-to-store covers it (holdstore.go).
		h.logger.Info("hold-to-save: nothing to do, the speaker plays a source of its own and stores it itself",
			"slot", p.slot, "detail", err.Error())
	case errors.Is(err, webui.ErrHoldUnknownStream):
		h.logger.Info("hold-to-save: not saved, the speaker plays a stream STR did not start or no longer knows",
			"slot", p.slot)
		h.notify(oled.NotSaved(p.slot))
	case errors.As(err, &refusal):
		h.logger.Info("hold-to-save: not saved, the preset rules refused it",
			"slot", p.slot, "code", refusal.Code, "reason", refusal.Error())
		if refusal.Code == "already-on-slot" && refusal.OtherSlot >= 1 {
			h.notify(oled.AlreadyOnKey(refusal.OtherSlot))
		} else {
			h.notify(oled.NotSaved(p.slot))
		}
	case err != nil:
		h.logger.Warn("hold-to-save: the save failed", "slot", p.slot, "err", err)
	case saved.Unchanged:
		h.logger.Info("hold-to-save: the key already holds what plays, nothing written",
			"slot", p.slot, "kind", saved.Kind, "name", saved.Name)
		h.notify(oled.SavedByKey(p.slot, saved.Name))
	default:
		h.logger.Info("hold-to-save: stored what STR plays on the held key",
			"slot", p.slot, "kind", saved.Kind, "name", saved.Name,
			"producer", p.producer.String(), "holdAfterPressMs", holdAt.Sub(p.at).Milliseconds())
		h.notify(oled.SavedByKey(p.slot, saved.Name))
	}
}
