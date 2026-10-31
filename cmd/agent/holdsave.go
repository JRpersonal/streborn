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
//
// A physical press on the sm2 ST10 that only the analytics line reports
// (origin stats) never gets a hold verdict at all: two holds on a live ST10
// (2026-10-16) left one stats press line each and nothing else, while each
// tap in the same run brought its activation and a tap verdict within half a
// second. So a stats press that holdConfirmAfter later still has neither is
// read as the hold itself (statsHold), and goes through the same
// confirmation and save as a paired one.
//
// A preset key held together with another key is never a hold of that key.
// The firmware's own combinations (1 and volume down for the factory reset,
// 2 or 5 and volume down for the setup network) start with a preset key, and
// the firmware still reads the preset as held on the way. Saving, swapping or
// drawing on the display at that moment got in the way of the combination
// (#1340), so a press with another physical key down around it is left to the
// firmware entirely (comboKey).
//
// The other key is not always visible: the sm2 ST10's analytics line reports
// the preset key and nothing else (6 + volume up on a live ST10, 2026-10-18,
// left one PRESET_6 line). So the firmware's own countdown for a real
// combination counts as the other key too (NoteCombination). A pair the
// firmware does not count down for still reads as a hold there; that is no
// firmware combination, and it only matters while STR's own stream plays.
//
// A hold that follows a tap of the same key is a different gesture: it saves
// what played before the tap onto the key's second row (tapholdsave.go). The
// confirmation (confirmAndSave, statsHold) hands it over before its own
// veto, which would read the tap's frame as "not a hold".

package main

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
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
	// heldScreenAfter is when, with a physical preset key still down, the
	// display shows the held-key screen (oled.KeyHeld). It must be on the
	// panel before the firmware's own reaction to the hold: on the Portable
	// the hold verdict, and with it the firmware's "action not available"
	// message for a UPnP push, comes 2.0 s after the press (field log
	// 2026-10-13: pressAgeMs=2003). A press released before this is a tap
	// on every chassis (the ST10's tap limit is under 1.75 s), and a tap
	// never touches the panel.
	heldScreenAfter = 1800 * time.Millisecond
	// comboLookback is how long before a preset press another key's press
	// still makes it a combination: people rarely hit both keys in the same
	// instant, and either may go first.
	comboLookback = 2 * time.Second
	// comboHeldFor bounds how long a key whose release the trace never
	// reported still counts as down. The longest firmware combination (the
	// factory reset) takes ten seconds.
	comboHeldFor = 15 * time.Second
)

// heldKeyScreen is the display's held-key session (oled.KeyHoldStart and
// friends). The outcome itself goes through notify like every notice; the
// display routes it into the running session.
type heldKeyScreen interface {
	Start(slot int, pressAt time.Time)
	// StartRowTwo is Start for the hold of a tap+hold, which saves onto the
	// key's second row (tapholdsave.go).
	StartRowTwo(slot int, pressAt time.Time)
	Released(slot int, at time.Time)
	Cancel(slot int, reason string)
}

// oledHeldScreen is heldKeyScreen on the speaker's panel.
type oledHeldScreen struct{}

func (oledHeldScreen) Start(slot int, pressAt time.Time) { oled.KeyHoldStart(slot, pressAt) }
func (oledHeldScreen) StartRowTwo(slot int, pressAt time.Time) {
	oled.KeyHoldStartRowTwo(slot, pressAt)
}
func (oledHeldScreen) Released(slot int, at time.Time) { oled.KeyHoldReleased(slot, at) }
func (oledHeldScreen) Cancel(slot int, reason string)  { oled.KeyHoldCancel(slot, reason) }

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
	// released is when the key went up again (key trace only); zero while
	// it is down or when the chassis reports no release.
	released time.Time
	// screen is set once the held-key screen started for this press.
	screen bool
	// combo is set once another physical key was seen down around this
	// press: a firmware key combination, which STR stays out of.
	combo bool
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
	// screen shows the held-key screen while a physical preset key is held
	// (oledHeldScreen). nil: off.
	screen heldKeyScreen
	// tapHold wires the tap+hold save onto the second row
	// (tapholdsave.go). Zero value: off.
	tapHold tapHoldWiring

	mu sync.Mutex
	// tap is the newest selection frame a person caused (tapholdsave.go).
	tap    tapMark
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
	// otherAt is the newest physical press (or repeat) of a key that is not
	// a preset key, otherName its key, otherUp its newest release (key trace
	// only; the analytics line reports no release).
	otherAt    time.Time
	otherName  string
	otherUp    time.Time
	otherTrace bool
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
	if slot == 0 {
		h.noteOtherKey(ev)
		return
	}
	if ev.State == boxlog.StateReleased {
		h.noteRelease(slot, ev.At)
		return
	}
	if ev.State != boxlog.StatePressed {
		return
	}
	p := holdPress{slot: slot, producer: ev.Producer, origin: ev.Origin, at: ev.At}
	h.mu.Lock()
	h.pressSeq++
	p.seq = h.pressSeq
	if p.producer.Physical() && h.otherDownLocked(p.at) {
		p.combo = true
		h.press = p
		h.hold = time.Time{}
		other := h.otherName
		h.mu.Unlock()
		h.logger.Info("hold-to-save: a key combination, left to the speaker",
			"slot", p.slot, "otherKey", other, "producer", p.producer.String())
		h.noteTapHoldCombo(p)
		return
	}
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
	if p.producer.Physical() && p.origin == boxlog.OriginStats && h.after != nil {
		h.after(max(p.at.Add(holdConfirmAfter).Sub(h.now()), 0), func() { h.statsHold(p) })
	}
	if h.emptyKey != nil && h.after != nil &&
		(p.producer.Physical() || (p.origin == boxlog.OriginWeb && h.appKeysAccepted())) {
		h.after(emptyKeyCheckAfter, func() { h.checkEmpty(p, false) })
	}
	if h.screen != nil && h.after != nil && p.producer.Physical() {
		// Counted from the press itself, not from when its line was read.
		h.after(max(p.at.Add(heldScreenAfter).Sub(h.now()), 0), func() { h.startHeldScreen(p) })
	}
}

// noteRelease records a preset key going up (key trace) and tells the
// held-key screen, which then stays until a firmware message drawn at the
// release is gone.
func (h *holdSaver) noteRelease(slot int, at time.Time) {
	h.mu.Lock()
	p := &h.press
	if p.slot != slot || !p.released.IsZero() || at.Before(p.at) {
		h.mu.Unlock()
		return
	}
	p.released = at
	screen := p.screen
	h.mu.Unlock()
	if screen {
		h.screen.Released(slot, at)
	}
}

// noteOtherKey records a physical key that is not a preset key. Pressed
// while a preset key is down (or shortly before), it turns that press into a
// firmware key combination: nothing is saved, and a held-key screen that
// already shows is taken down so the firmware has the panel.
func (h *holdSaver) noteOtherKey(ev boxlog.KeyEvent) {
	if !ev.Producer.Physical() {
		return
	}
	h.mu.Lock()
	if ev.State == boxlog.StateReleased {
		if ev.At.After(h.otherUp) {
			h.otherUp = ev.At
		}
		h.mu.Unlock()
		return
	}
	if ev.At.After(h.otherAt) || h.otherAt.IsZero() {
		h.otherAt, h.otherName, h.otherTrace = ev.At, ev.Name, ev.Origin == boxlog.OriginTrace
	}
	p := h.press
	hit := p.slot != 0 && !p.combo && p.producer.Physical() &&
		!ev.At.Before(p.at.Add(-comboLookback)) && ev.At.Sub(p.at) < comboHeldFor &&
		(p.released.IsZero() || ev.At.Before(p.released))
	if hit {
		h.press.combo = true
		h.hold = time.Time{}
	}
	h.mu.Unlock()
	if !hit {
		return
	}
	h.logger.Info("hold-to-save: a key combination, left to the speaker",
		"slot", p.slot, "otherKey", ev.Name, "producer", p.producer.String(),
		"otherAfterPressMs", ev.At.Sub(p.at).Milliseconds(), "saveUnderWay", p.used)
	h.noteTapHoldCombo(p)
	if p.screen {
		h.screen.Cancel(p.slot, "key combination")
	}
}

// NoteCombination hears the firmware counting a key combination down. It is
// handled like a second key going down, so it also covers a chassis whose
// key trace never reports the volume key (the sm2 SoundTouch 10).
func (h *holdSaver) NoteCombination(at time.Time) {
	h.noteOtherKey(boxlog.KeyEvent{Name: "COMBINATION", Producer: boxlog.ProducerConsole,
		State: boxlog.StatePressed, Origin: boxlog.OriginStats, At: at})
}

// otherDownLocked reports whether another physical key counts as down at t:
// pressed within comboLookback of it, or pressed earlier and not released
// yet. Callers hold h.mu.
func (h *holdSaver) otherDownLocked(t time.Time) bool {
	if h.otherAt.IsZero() {
		return false
	}
	d := t.Sub(h.otherAt)
	if d < 0 {
		d = -d
	}
	if d <= comboLookback {
		return true
	}
	return h.otherTrace && h.otherAt.Before(t) && t.Sub(h.otherAt) < comboHeldFor && h.otherUp.Before(h.otherAt)
}

// startHeldScreen is the one-shot look heldScreenAfter after a physical
// preset press: is the key still down? Then the held-key screen takes the
// panel before the firmware reacts to the hold. A tap (released, or a tap
// verdict or another press since) leaves the panel alone.
func (h *holdSaver) startHeldScreen(p holdPress) {
	h.mu.Lock()
	cur, sel := h.press, h.selectAt
	// The hold of a tap+hold may go down before the tap's own frame arrives.
	_, tapped := h.tapCandidateLocked(cur)
	ok := cur.seq == p.seq && cur.released.IsZero() && !cur.combo &&
		(sel.IsZero() || sel.Before(p.at) || (tapped && h.tapHoldEnabled())) &&
		(h.verdictAt.Before(p.at) || h.verdictHold)
	if ok {
		h.press.screen = true
	}
	h.mu.Unlock()
	if !ok {
		return
	}
	if tapped {
		if _, rowTwo, _ := h.tapHoldVerdict(cur); rowTwo {
			h.screen.StartRowTwo(p.slot, p.at)
			return
		}
	}
	h.screen.Start(p.slot, p.at)
}

// endHeldScreen ends the held-key screen of the newest press, if it runs and
// no outcome reached it yet.
func (h *holdSaver) endHeldScreen(slot int, reason string) {
	h.mu.Lock()
	on := h.press.screen && h.press.slot == slot
	h.mu.Unlock()
	if on {
		h.screen.Cancel(slot, reason)
	}
}

// checkEmpty is the one-shot look after a physical preset press: when the
// press is still the newest, no hold was paired with it, the speaker
// activated nothing since, and the key holds nothing in STR's store or the
// box's own list, the display says the key is empty and where to set it.
// The firmware itself shows nothing for an empty key.
func (h *holdSaver) checkEmpty(p holdPress, again bool) {
	h.mu.Lock()
	cur, sel := h.press, h.selectAt
	h.mu.Unlock()
	if cur.seq != p.seq || cur.used || cur.combo || (!sel.IsZero() && !sel.Before(p.at)) {
		return
	}
	if cur.screen && !again && h.after != nil {
		// The key is held and the held-key screen shows: the hold verdict
		// may land just after this check (2.0 s on the Portable), and then
		// the save's outcome is the answer, not EMPTY. Look once more when
		// any hold has shown itself.
		h.after(holdConfirmAfter-emptyKeyCheckAfter, func() { h.checkEmpty(p, true) })
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
	if p.slot != slot || p.at.IsZero() || p.combo || h.now().Sub(p.at) > d {
		return false
	}
	return p.producer.Physical() || (p.origin == boxlog.OriginWeb && h.appKeysAccepted())
}

// recentPhysicalHold is recentPhysicalPress narrowed to a press the speaker
// did not read as a tap: the marge keeper's "was this a person's HOLD".
//
// A press alone is not enough. STR's own boot-time key re-sync PUTs every slot
// through the same keeper, and on a SoundTouch 20 (field bundle 2026-10-22)
// the re-sync's PUT for slot 6 landed twelve seconds after a TAP on key 6 the
// firmware had read as a tap (the keys were dead, which is why it was being
// pressed): the display said "SAVED 6" for a store nobody made.
//
// A press counts when its hold was confirmed (paired with a hold verdict, or
// read as one by statsHold), or when a hold verdict of its own arrived late.
// A press with no verdict and no activation yet still counts: a chassis that
// only has the analytics line gets its hold reading from statsHold at
// holdConfirmAfter, which can land just after the firmware's own PUT. A tap
// verdict or a preset activation since shortly before the press is a tap.
func (h *holdSaver) recentPhysicalHold(slot int, d time.Duration) bool {
	if !h.recentPhysicalPress(slot, d) {
		return false
	}
	h.mu.Lock()
	p, veto, verdictAt, verdictHold := h.press, h.vetoAt, h.verdictAt, h.verdictHold
	h.mu.Unlock()
	if p.slot != slot {
		return false
	}
	if p.used {
		return true
	}
	from := p.at.Add(-holdVetoLookback)
	if !verdictAt.IsZero() && !verdictAt.Before(from) {
		return verdictHold
	}
	return veto.IsZero() || veto.Before(from)
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
	if cur.seq == p.seq && (cur.used || cur.combo) {
		return
	}
	if cur.seq == p.seq && cur.screen {
		h.screen.Cancel(p.slot, "no hold verdict")
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

// statsHold is the one-shot look holdConfirmAfter after a physical press
// reported only by the analytics line: when it is still the newest press, no
// verdict paired with it, and neither a tap verdict nor a preset activation
// came since shortly before it, the press was a hold the firmware gave no
// verdict for (#1217), and it is saved like a paired one.
func (h *holdSaver) statsHold(p holdPress) {
	from := p.at.Add(-holdVetoLookback)
	h.mu.Lock()
	cur, veto, verdictAt := h.press, h.vetoAt, h.verdictAt
	// The hold of a tap+hold: the tap's own frame and tap verdict lie just
	// before this press and are expected; only a tap verdict after it, or a
	// second frame of the key, says it was no hold (tapCandidateLocked,
	// tapHoldVerdict at the confirmation).
	_, tapped := h.tapCandidateLocked(cur)
	tapped = tapped && h.tapHoldEnabled()
	if cur.seq != p.seq || cur.used || cur.combo ||
		(!tapped && !veto.IsZero() && !veto.Before(from)) ||
		(!tapped && !verdictAt.IsZero() && !verdictAt.Before(from)) {
		h.mu.Unlock()
		return
	}
	h.press.used = true
	h.mu.Unlock()
	h.logger.Info("hold-to-save: no verdict and no activation after the press, reading it as a hold",
		"slot", p.slot, "producer", p.producer.String(), "origin", string(p.origin))
	h.paired(p, p.at)
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
		screen := p.screen
		h.mu.Unlock()
		if screen {
			h.screen.Cancel(p.slot, "a tap after all")
		}
		h.logger.Info("hold-to-save: the speaker read a preset key as a tap",
			"hold", false, "pressSlot", slot, "pressAgeMs", ageMs, "pressUsed", p.used)
		return
	}
	if p.combo && !g.At.Before(p.at) && g.At.Sub(p.at) < comboHeldFor {
		h.mu.Unlock()
		h.logger.Info("hold-to-save: the speaker read a preset key as a hold inside a key combination, not saving",
			"pressSlot", slot, "pressAgeMs", ageMs)
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
	// A hold that follows a tap of the same key saves onto the key's second
	// row, never onto the key itself (tapholdsave.go).
	if t, ok, why := h.tapHoldVerdict(p); ok {
		h.saveRowTwo(p, holdAt, t)
		return
	} else if why != "" {
		h.logger.Info("tap+hold on key "+strconv.Itoa(p.slot)+" ignored: "+why,
			"key", p.slot, "tapToPressMs", p.at.Sub(t.at).Milliseconds(), "frames", t.frames)
	}
	from := p.at
	if holdAt.Before(from) {
		from = holdAt
	}
	from = from.Add(-holdVetoLookback)
	h.mu.Lock()
	veto := h.vetoAt
	combo := h.press.seq == p.seq && h.press.combo
	h.mu.Unlock()
	if combo {
		// noteOtherKey logged it and took the held-key screen down.
		return
	}
	if !veto.IsZero() && !veto.Before(from) {
		h.logger.Info("hold-to-save: not saving, the speaker activated a key or read the press as a tap",
			"slot", p.slot, "vetoAfterPressMs", veto.Sub(p.at).Milliseconds())
		h.endHeldScreen(p.slot, "not saving")
		return
	}
	if h.save == nil {
		h.endHeldScreen(p.slot, "no saver")
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
		h.endHeldScreen(p.slot, "save failed")
	case saved.SwappedWith > 0:
		h.logger.Info("hold-to-save: swapped the keys, the held key took over what plays",
			"slot", p.slot, "kind", saved.Kind, "name", saved.Name,
			"otherSlot", saved.SwappedWith, "otherNow", saved.SwappedName,
			"producer", p.producer.String(), "holdAfterPressMs", holdAt.Sub(p.at).Milliseconds())
		h.notify(oled.Swapped(p.slot, saved.SwappedWith, saved.SwappedName))
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
