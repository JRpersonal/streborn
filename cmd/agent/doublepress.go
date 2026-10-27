// Pressing a preset key twice quickly plays the second row of presets (#1110).
//
// STR keeps twelve presets: slots 1..6 are the speaker's own keys, slots 7..12
// (the second row) live only in STR's store, and slot 6+n is "key n pressed
// twice". The speaker knows nothing about this. What it does give us, measured
// with a gabbo :8080 watcher on a Portable (taigan), an ST30 and an ST10 (sm2)
// on 2026-10-27:
//
//   - Every physical press of a preset key on a speaker that is not grouped
//     emits exactly one nowSelectionUpdated <preset id="N"> about 0.13 s
//     after the release, a re-press of the key that already plays included.
//     Double presses on the top keys arrived 151 to 322 ms apart.
//   - After each frame the speaker buffers and reaches PLAY 2 to 3 s later, so
//     switching to row 2 on the second frame happens before row 1 is heard.
//   - Inside a group (an ST10 member of a fixed group led by an ST30) every
//     single press emitted the frame for that key TWICE, about 350 ms apart.
//     So in a zone two frames prove nothing; only the key trace
//     (internal/boxlog: the IrDevice/ConsoleButtons lines, or the sm2
//     analytics line) can say whether a person pressed twice.
//
// So the rule is: a second frame for the same key within doublePressWindow of
// the first is a double press. Standalone that is enough. In a zone (or while
// STR does not know yet whether the speaker is in one) the key trace has to
// show two physical presses of that key; without such evidence the double
// press is off and row 1 plays exactly as it does today.
//
// A selection STR caused itself (its own `sys presetkey`, an item it handed to
// /select) never counts, neither as a first nor as a second press.
//
// A single press is never delayed: row 1 starts on the first frame as it
// always has, and row 2 supersedes it on the second.

package main

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/boxwrites"
	"github.com/JRpersonal/streborn/internal/oled"
	"github.com/JRpersonal/streborn/internal/presets"
)

const (
	// doublePressWindow is the longest gap between the two selection frames
	// of a double press. Measured double presses were 151 to 322 ms apart;
	// the firmware's hold-to-save gesture needs a key down for about 1.5 s,
	// so a window well under that never reads a hold as a double press.
	doublePressWindow = 700 * time.Millisecond
	// ownSelectionWindow is how long after STR pressed a key itself (or
	// handed the speaker an item) a selection frame is taken for that echo.
	ownSelectionWindow = 2 * time.Second
	// doublePressTraceLookback is how far before the first frame the key
	// trace is searched for the first press: the key goes down before the
	// release the frame follows, and a tap is well under this.
	doublePressTraceLookback = 800 * time.Millisecond
)

// doublePressTraceWait bounds how long a double press inside a group waits
// for the key trace to show the second press. The trace line was measured to
// trail its press by up to about a second on the Portable (keyTraceWait). A
// var so the tests can shorten it.
var doublePressTraceWait = keyTraceWait

// doublePressTracePoll is how often that wait looks at the trace.
var doublePressTracePoll = 100 * time.Millisecond

// doublePressDetector pairs selection frames into double presses. Zero value
// ready.
type doublePressDetector struct {
	mu sync.Mutex
	// key and at are the newest frame that can still become the first half
	// of a double press; armed is false once it was used or invalidated.
	key   int
	at    time.Time
	armed bool
}

// observe records a selection frame for key at t and reports whether it is the
// second half of a double press, and the gap to the first half. own frames
// (STR's own selections) disarm the detector and are never a second press.
func (d *doublePressDetector) observe(key int, t time.Time, own bool) (second bool, first time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if own {
		d.armed = false
		return false, time.Time{}
	}
	if d.armed && d.key == key && !t.Before(d.at) && t.Sub(d.at) <= doublePressWindow {
		// A third quick press starts a new pair rather than extending this one.
		d.armed = false
		return true, d.at
	}
	d.key, d.at, d.armed = key, t, true
	return false, time.Time{}
}

// Group state as the zone and pair frames report it.
const (
	groupUnknown int32 = iota
	groupAlone
	groupMember
)

// groupMembership remembers whether the speaker is in a zone or stereo pair,
// from the gabbo zone and group frames, seeded by one /getZone read per bus
// connect. Zero value: unknown.
type groupMembership struct {
	zone atomic.Int32
	pair atomic.Int32
}

// grouped reports whether the speaker is (or may be) grouped: true when a
// zone or a pair is known, and when the zone state is not known at all yet.
func (g *groupMembership) grouped() (grouped, known bool) {
	z, p := g.zone.Load(), g.pair.Load()
	if z == groupMember || p == groupMember {
		return true, true
	}
	if z == groupUnknown {
		return true, false
	}
	return false, true
}

func membership(in bool) int32 {
	if in {
		return groupMember
	}
	return groupAlone
}

// ownSelection reports whether a selection of key arriving now is the echo of
// STR's own doing: a key it pressed over the CLI (the first-press rescue, the
// box-preset play) or an item it handed to the speaker's /select (a native
// station or service recall).
func ownSelection(key int) bool {
	return boxwrites.SlotNotedWithin("presetkey", key, ownSelectionWindow) ||
		boxwrites.SlotNotedWithin("select", 0, ownSelectionWindow)
}

// doublePressPlan is what OnPresetSelected does with a frame.
type doublePressPlan int

const (
	// planSingle: an ordinary press, handled exactly as before row 2 existed.
	planSingle doublePressPlan = iota
	// planRow2: a confirmed double press with row 2 set; play row 2 now.
	planRow2
	// planConfirm: a possible double press inside a group; the key trace
	// decides between row 2 and an ordinary press.
	planConfirm
)

// planPress classifies a selection frame of key at t. row2 is the preset on
// the key's second-row slot when the frame is a second press and that slot is
// set; first is when the first half arrived.
func (h *presetWsHandler) planPress(key int, t time.Time) (plan doublePressPlan, row2 presets.Preset, first time.Time) {
	if !presets.IsBoxSlot(key) {
		return planSingle, presets.Preset{}, time.Time{}
	}
	own := h.ownSelectionFn
	if own == nil {
		own = ownSelection
	}
	second, first := h.doublePress.observe(key, t, own(key))
	if !second {
		return planSingle, presets.Preset{}, time.Time{}
	}
	slot := presets.Row2Slot(key)
	gapMs := t.Sub(first).Milliseconds()
	p, ok := h.store.Get(slot)
	if !ok {
		h.logger.Info("double press on key "+strconv.Itoa(key)+": row 2 slot "+strconv.Itoa(slot)+" empty, staying on row 1",
			"key", key, "slot", slot, "gapMs", gapMs)
		return planSingle, presets.Preset{}, time.Time{}
	}
	if grouped, known := h.group.grouped(); grouped {
		h.logger.Info("double press on key "+strconv.Itoa(key)+": speaker may be grouped, asking the key trace",
			"key", key, "slot", slot, "gapMs", gapMs, "zoneKnown", known)
		return planConfirm, p, first
	}
	return planRow2, p, first
}

// tracePresses counts the physical presses of key the key trace read since t.
// Seam-aware for the tests; 0 when no trace runs on this speaker.
func (h *presetWsHandler) tracePresses(key int, since time.Time) int {
	if h.tracePressesFn != nil {
		return h.tracePressesFn(key, since)
	}
	if h.keyTrace == nil {
		return 0
	}
	return h.keyTrace.PhysicalPressesSince("PRESET_"+strconv.Itoa(key), since)
}

// confirmDoublePress decides a possible double press inside a group from the
// key trace: two physical presses of the key mean the person pressed twice
// and row 2 plays; one means the second frame was the group's echo; none means
// this speaker gives no evidence, and the double press stays off. In both of
// the latter cases the second frame is then handled as the ordinary press it
// was before row 2 existed. Blocking; runs on its own goroutine.
func (h *presetWsHandler) confirmDoublePress(ctx context.Context, key int, first, pressAt time.Time, row2 presets.Preset, location, title string) {
	since := first.Add(-doublePressTraceLookback)
	deadline := time.Now().Add(doublePressTraceWait)
	n := h.tracePresses(key, since)
	for n < 2 && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(doublePressTracePoll):
		}
		n = h.tracePresses(key, since)
	}
	if n >= 2 {
		seq := h.pressSeq.Add(1)
		h.playRow2(ctx, seq, key, row2, pressAt.Sub(first), "key trace confirms two presses")
		return
	}
	if n == 1 {
		h.logger.Info("double press on key "+strconv.Itoa(key)+": ignored, group echo (the key trace shows one press)",
			"key", key, "slot", row2.Slot)
	} else {
		h.logger.Info("double press on key "+strconv.Itoa(key)+": ignored, group echo without trace",
			"key", key, "slot", row2.Slot)
	}
	h.ordinaryPress(ctx, pressAt, key, location, title, false)
}

// playRow2 plays the second-row preset of key through the app's recall path,
// superseding the first press: seq was bumped for this press, so the first
// press's recall, verify, 1036 recovery and last-play record stand down, and
// the first-press rescue may no longer press key again.
func (h *presetWsHandler) playRow2(ctx context.Context, seq uint64, key int, p presets.Preset, gap time.Duration, why string) {
	h.firstPress.cancel()
	h.logger.Info("double press on key "+strconv.Itoa(key)+" -> row 2 slot "+strconv.Itoa(p.Slot)+" '"+p.Name+"'",
		"key", key, "slot", p.Slot, "type", p.Type, "gapMs", gap.Milliseconds(), "why", why)
	showNotice(oled.PresetPlaying(key, "2x "+p.Name))
	if h.noteUserPlay != nil {
		h.noteUserPlay()
	}
	if h.playSlotFn == nil {
		h.logger.Warn("double press: no recall path wired, row 2 cannot play", "slot", p.Slot)
		return
	}
	if h.pressSeq.Load() != seq {
		h.logger.Info("double press superseded by a newer press before row 2 started", "slot", p.Slot)
		return
	}
	playCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := h.playSlotFn(playCtx, p.Slot); err != nil {
		h.logger.Warn("double press: row 2 did not start", "key", key, "slot", p.Slot, "err", err)
	}
}

// refreshGroupMembership reads the speaker's zone once, so the double-press
// detector knows the group state before the first zone frame arrives. Called
// on every bus (re)connect; a frame that arrives meanwhile is newer and wins.
func (h *presetWsHandler) refreshGroupMembership(ctx context.Context) {
	if h.boxHost == "" && h.zoneProbe == nil {
		return
	}
	probe := h.zoneProbe
	if probe == nil {
		probe = func(ctx context.Context) (bool, error) {
			z, err := boxapi.New(h.boxHost).GetZone(ctx)
			if err != nil {
				return false, err
			}
			return z.Master != "" || len(z.Members) > 0, nil
		}
	}
	rctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	before := h.group.zone.Load()
	in, err := probe(rctx)
	if err != nil {
		h.logger.Debug("double press: zone not readable, group state stays unknown", "err", err)
		return
	}
	h.group.zone.CompareAndSwap(before, membership(in))
}
