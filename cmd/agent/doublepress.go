// Pressing a preset key twice quickly plays the second row of presets (#1110).
//
// STR keeps up to ten rows of six presets: slots 1..6 are the speaker's own
// keys, and each later row is reached by pressing that key the same number of
// times. The extra rows live only in STR's store. What the speaker gives
// us, measured
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
	"fmt"
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

// doublePressDetector pairs selection frames into physical taps on grouped
// speakers, where each tap echoes as two frames. It retains a short tap-burst
// start so the key trace can distinguish a third physical press as well.
type doublePressDetector struct {
	mu sync.Mutex
	// key and at are the newest frame that can still become the second half
	// of a physical tap's frame pair; armed is false once used or invalidated.
	key   int
	at    time.Time
	armed bool
	// burst tracks consecutive pairs for one key. Later grouped taps use the
	// original start time so the trace includes the whole physical sequence.
	burstKey   int
	burstAt    time.Time
	burstStart time.Time
}

func (d *doublePressDetector) reset() {
	d.mu.Lock()
	d.key, d.at, d.armed = 0, time.Time{}, false
	d.burstKey, d.burstAt, d.burstStart = 0, time.Time{}, time.Time{}
	d.mu.Unlock()
}

// observe records a selection frame for key at t and reports whether it is the
// second half of a double press, and the gap to the first half. own frames
// (STR's own selections) disarm the detector and are never a second press.
func (d *doublePressDetector) observe(key int, t time.Time, own bool) (second bool, first time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if own {
		d.key, d.at, d.armed = 0, time.Time{}, false
		d.burstKey, d.burstAt, d.burstStart = 0, time.Time{}, time.Time{}
		return false, time.Time{}
	}
	if !d.burstStart.IsZero() && d.burstKey == key && !t.Before(d.burstAt) && t.Sub(d.burstAt) <= doublePressWindow {
		d.burstAt = t
	} else {
		d.burstKey, d.burstAt, d.burstStart = key, t, t
	}
	if d.armed && d.key == key && !t.Before(d.at) && t.Sub(d.at) <= doublePressWindow {
		d.armed = false
		first := d.burstStart
		return true, first
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
	return boxwrites.OwnSelectionWithin(key, ownSelectionWindow)
}

// doublePressPlan is what OnPresetSelected does with a frame.
type doublePressPlan int

const (
	// planSingle: an ordinary press, handled exactly as before row 2 existed.
	planSingle doublePressPlan = iota
	// planExtraRow: a confirmed multiple press with that row set; play it now.
	planExtraRow
	// planConfirm: a possible multiple press inside a group; the key trace
	// decides which active extra row to play, or whether this was one press.
	planConfirm
)

func (h *presetWsHandler) presetRows() int {
	rows := presets.Rows
	if h.presetRowsFn != nil {
		rows = h.presetRowsFn()
	}
	if rows < 1 {
		return 1
	}
	if rows > presets.Rows {
		return presets.Rows
	}
	return rows
}

// planPress classifies a selection frame of key at t. p is the extra-row
// preset selected by this frame, when one is active; first is the first frame
// of the press sequence.
func (h *presetWsHandler) planPress(key int, t time.Time) (plan doublePressPlan, p presets.Preset, first time.Time) {
	if !presets.IsBoxSlot(key) {
		h.pressSequence.reset()
		h.doublePress.reset()
		return planSingle, presets.Preset{}, time.Time{}
	}
	own := h.ownSelectionFn
	if own == nil {
		own = ownSelection
	}
	rows := h.presetRows()
	if grouped, _ := h.group.grouped(); !grouped {
		h.doublePress.reset()
		count, start := h.pressSequence.observe(key, t, own(key))
		if count > 1 && count <= rows {
			if extra, ok := h.store.Get(presets.RowSlot(count, key)); ok {
				return planExtraRow, extra, start
			}
		}
		return planSingle, presets.Preset{}, time.Time{}
	}
	h.pressSequence.reset()
	second, first := h.doublePress.observe(key, t, own(key))
	if !second {
		return planSingle, presets.Preset{}, time.Time{}
	}
	gapMs := t.Sub(first).Milliseconds()
	var hasExtra bool
	for row := 2; row <= rows; row++ {
		if _, ok := h.store.Get(presets.RowSlot(row, key)); ok {
			hasExtra = true
			break
		}
	}
	if !hasExtra {
		h.logger.Info("double press on key "+strconv.Itoa(key)+": no active extra row, staying on row 1",
			"key", key, "gapMs", gapMs, "rows", rows)
		return planSingle, presets.Preset{}, time.Time{}
	}
	_, known := h.group.grouped()
	h.logger.Info("multiple press on key "+strconv.Itoa(key)+": speaker may be grouped, asking the key trace",
		"key", key, "gapMs", gapMs, "zoneKnown", known)
	return planConfirm, presets.Preset{}, first
}

// pressSequenceDetector counts consecutive frames from a standalone speaker.
// Unlike a grouped speaker, it emits one frame for each physical press.
type pressSequenceDetector struct {
	mu    sync.Mutex
	key   int
	at    time.Time
	start time.Time
	count int
}

func (d *pressSequenceDetector) reset() {
	d.mu.Lock()
	d.key, d.at, d.start, d.count = 0, time.Time{}, time.Time{}, 0
	d.mu.Unlock()
}

func (d *pressSequenceDetector) observe(key int, t time.Time, own bool) (count int, first time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if own {
		d.key, d.at, d.start, d.count = 0, time.Time{}, time.Time{}, 0
		return 0, time.Time{}
	}
	if d.count > 0 && d.key == key && !t.Before(d.at) && t.Sub(d.at) <= doublePressWindow {
		d.count++
		d.at = t
	} else {
		d.key, d.at, d.start, d.count = key, t, t, 1
	}
	count, first = d.count, d.start
	if count > presets.Rows {
		count = 1
		d.key, d.at, d.start, d.count = key, t, t, 1
		first = t
	}
	return count, first
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

// confirmDoublePress decides a possible multiple press inside a group from the
// key trace: N physical presses mean row N; one means the later frame was the
// group's echo; none means this speaker gives no evidence and the multiple
// press stays off. In either case, the second frame is handled as an ordinary
// press. This runs on its own goroutine and may wait for the key trace.
func (h *presetWsHandler) confirmDoublePress(ctx context.Context, order uint64, key int, first, pressAt time.Time, row2Already bool, location, title string) {
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
	if order != h.selectionSeq.Load() {
		h.logger.Debug("multiple press on key "+strconv.Itoa(key)+": older trace result superseded", "key", key, "presses", n)
		return
	}
	rows := h.presetRows()
	if n >= 2 {
		if row2Already && n == 2 {
			h.logger.Info("multiple press on key "+strconv.Itoa(key)+": row 2 already started by its empty row-1 key",
				"key", key, "slot", presets.Row2Slot(key))
			return
		}
		if n <= rows {
			if extra, ok := h.store.Get(presets.RowSlot(n, key)); ok {
				seq := h.pressSeq.Add(1)
				h.playExtraRow(ctx, seq, key, extra, n, pressAt.Sub(first), true,
					"key trace confirms "+strconv.Itoa(n)+" presses")
				return
			}
		}
		h.logger.Info("multiple press on key "+strconv.Itoa(key)+": trace confirms an extra press, but that row is empty or disabled",
			"key", key, "presses", n, "rows", rows)
		return
	}
	if row2Already {
		h.logger.Info("multiple press on key "+strconv.Itoa(key)+": row 2 already started by its empty row-1 key",
			"key", key, "slot", presets.Row2Slot(key))
		return
	}
	if n == 1 {
		h.logger.Info("multiple press on key "+strconv.Itoa(key)+": ignored, group echo (the key trace shows one press)", "key", key)
	} else {
		h.logger.Info("multiple press on key "+strconv.Itoa(key)+": ignored, group echo without trace", "key", key)
	}
	h.ordinaryPress(ctx, pressAt, key, location, title, false)
}

// playRow2 plays the second-row preset of key through the app's recall path,
// superseding the first press: seq was bumped for this press, so the first
// press's recall, verify, 1036 recovery and last-play record stand down, and
// the first-press rescue may no longer press key again. double is false for
// the single press of a key with nothing on row 1 (#1910), which plays its
// second row directly.
func (h *presetWsHandler) playRow2(ctx context.Context, seq uint64, key int, p presets.Preset, gap time.Duration, double bool, why string) {
	h.playExtraRow(ctx, seq, key, p, 2, gap, double, why)
}

func (h *presetWsHandler) playExtraRow(ctx context.Context, seq uint64, key int, p presets.Preset, row int, gap time.Duration, multiple bool, why string) {
	h.firstPress.cancel()
	press := "press"
	if multiple {
		press = "multiple press"
	}
	label := strconv.Itoa(row) + "x "
	h.logger.Info(press+" on key "+strconv.Itoa(key)+" -> row "+strconv.Itoa(row)+" slot "+strconv.Itoa(p.Slot)+" '"+p.Name+"'",
		"key", key, "slot", p.Slot, "row", row, "type", p.Type, "gapMs", gap.Milliseconds(), "why", why)
	showNotice(oled.PresetPlaying(key, label+p.Name))
	if h.noteUserPlay != nil {
		h.noteUserPlay()
	}
	if h.playSlotFn == nil {
		h.logger.Warn(press+": no recall path wired, extra preset row cannot play", "slot", p.Slot, "row", row)
		return
	}
	if h.pressSeq.Load() != seq {
		h.logger.Info(press+" superseded by a newer press before extra row started", "slot", p.Slot, "row", row)
		return
	}
	playCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := h.playSlotFn(playCtx, p.Slot); err != nil {
		h.logger.Warn(press+": extra row did not start", "key", key, "slot", p.Slot, "row", row, "err", err)
	}
}

// placeholderPress reports whether a press of key landed on the placeholder
// the reconcile keeps on a key with nothing on row 1 (placeholderKeys): no
// row-1 preset in the store, the box's location in STR's own UPnP form (or
// none at all), and a second-row station or a per-key webhook behind the key.
// row2 is the key's second-row preset, nil when the key has only a webhook.
//
// A box location STR did not write (a speaker-cached Deezer or TuneIn key) or
// a native station is no placeholder: that key plays what it holds, exactly
// as before. Nor is an STR-form key with neither a second row nor a webhook:
// that is a key left over from a reinstall (#882), handled where it was.
func (h *presetWsHandler) placeholderPress(key int, location string) (row2 *presets.Preset, holder bool) {
	if !presets.IsBoxSlot(key) || h.store == nil {
		return nil, false
	}
	if _, ok := h.store.Get(key); ok {
		return nil, false
	}
	if location != "" && !ownBoxPresetLocRe.MatchString(location) {
		return nil, false
	}
	for row := 2; row <= presets.Rows; row++ {
		if extra, ok := h.store.Get(presets.RowSlot(row, key)); ok {
			if row == 2 && h.presetRows() >= 2 {
				return &extra, true
			}
			return nil, true
		}
	}
	if h.webhooks != nil {
		if _, ok := h.webhooks.Button(fmt.Sprintf("preset%d", key)); ok {
			return nil, true
		}
	}
	return nil, false
}

// row2SinglePress remembers the newest single press that played a key's
// second row because the key has nothing on row 1. Zero value ready.
type row2SinglePress struct {
	mu  sync.Mutex
	key int
	at  time.Time
}

// note records that the press of key at t started the key's second row.
func (r *row2SinglePress) note(key int, t time.Time) {
	r.mu.Lock()
	r.key, r.at = key, t
	r.mu.Unlock()
}

// startedBy reports whether the press of key at first already started the
// key's second row, i.e. the double press whose first half it was has
// nothing left to do.
func (r *row2SinglePress) startedBy(key int, first time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.key == key && !first.IsZero() && r.at.Equal(first)
}

// startedWithin reports whether the grouped tap's first selection frame
// already started row 2 just before its paired echo frame.
func (r *row2SinglePress) startedWithin(key int, at time.Time, d time.Duration) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.key == key && !r.at.IsZero() && !at.Before(r.at) && at.Sub(r.at) <= d
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
