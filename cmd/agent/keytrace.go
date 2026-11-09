package main

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxlog"
	"github.com/JRpersonal/streborn/internal/keyactions"
	"github.com/JRpersonal/streborn/internal/oled"
	"github.com/JRpersonal/streborn/internal/webhooks"
)

// keyLogDedup collapses the log lines for a key the user tends to hammer
// (volume ramps) to one per window, mirroring boxws's userActivity dedup so
// the NAND log does not churn. Every other key is rare and human-paced and is
// logged on each press and release.
const keyLogDedup = 3 * time.Second

// keyTraceStandDown is how recently a decoded key event must have arrived for
// the gabbo bare-frame heuristic (OnThumbActivity) to leave the webhook to the
// key trace: generous against the trace's own lag, still shorter than any
// deliberate second press.
const keyTraceStandDown = 2 * time.Second

// keyTraceWait bounds how long the bare-frame heuristic waits for the trace
// line to land before it falls back to its old behaviour. The frame settles
// 500 ms after the press; the syslog line was measured to trail it by up to
// about a second on the Portable. The wait only runs while the trace is
// healthy, so a box without it keeps the old timing.
const keyTraceWait = 1500 * time.Millisecond

// boxReasonWindow is how recent the firmware's own playback failure line must
// be to count as the reason a recall verify gave up. The verify retries for
// well under a minute, so a reason older than this belongs to an earlier
// attempt.
const boxReasonWindow = 30 * time.Second

var (
	keyLogMu   sync.Mutex
	keyLogLast = map[string]time.Time{}
	// powerReleaseAt is when the speaker last read a physical POWER key as
	// let go (guarded by keyLogMu), for powerKeyBounced.
	powerReleaseAt time.Time
)

// powerBounceWindow is how soon after a POWER release a new POWER press reads
// as the same push of the key, not a second one. A person who sees the speaker
// go dark and presses again takes longer; the #1798 trace had the second
// press 80 to 160 ms after the release.
const powerBounceWindow = 400 * time.Millisecond

// powerKeyBounced reports whether a POWER press at pressAt follows the last
// POWER release closely enough to be the same push read twice. The speaker
// turns each press into a power toggle, so such a pair switches it off and
// straight back on (#1798). Times are when the trace lines were read, so the
// gap is approximate.
func powerKeyBounced(lastRelease, pressAt time.Time) (time.Duration, bool) {
	if lastRelease.IsZero() || pressAt.Before(lastRelease) {
		return 0, false
	}
	gap := pressAt.Sub(lastRelease)
	return gap, gap <= powerBounceWindow
}

// boxFailureAttrs returns the firmware's own reason for a stream that did not
// start (a BAD_URL, no first frame, a terminal server error, an underrun) as
// slog attributes, when the speaker logged one inside boxReasonWindow. Nil
// otherwise, so the caller can append it unconditionally.
func (h *presetWsHandler) boxFailureAttrs() []any {
	if h.keyTrace == nil {
		return nil
	}
	ev, ok := h.keyTrace.LastPlaybackFailure(boxReasonWindow)
	if !ok {
		return nil
	}
	return []any{"boxReason", string(ev.Class), "boxMsg", ev.Message}
}

// groupKeyToggleBudget bounds one group-key press end to end: a zone form may
// wake the main speaker (8 s) before the firmware call, and the resume after
// it wakes again on a slow chassis.
const groupKeyToggleBudget = 75 * time.Second

// OnKeyEvent receives every key state change the speaker itself decoded (see
// internal/boxlog). It logs the event and, for a physical press of one of the
// transport keys, fires the per-key webhook. Presets, AUX and power keep their
// existing paths (the box's own selection/source/power frames), so nothing
// fires twice; volume and the rest are logged only.
//
// A thumbs key that carries a saved group (#863) toggles that group instead,
// one that carries the sleep timer steps it, one that carries a bass step
// moves the bass, and one that carries snooze snoozes a ringing alarm: a key
// carries one action, and any binding wins over a webhook on the same key.
// Group and sleep
// timer exclude each other at save time; should a hand-edited file carry
// both, the group wins, as it was there first.
func (h *presetWsHandler) OnKeyEvent(ev boxlog.KeyEvent) {
	h.logKeyEvent(ev)
	// A preset key may be the start of a hold on STR's own stream; the hold
	// saver checks the producer itself (holdsave.go).
	if h.holdSaver != nil {
		h.holdSaver.NotePress(ev)
	}
	if !ev.Pressed() || !ev.Producer.Physical() {
		return
	}
	// During a round of the hidden game (internal/oled) the remote keys are
	// the game's controls: no webhook, no group toggle. Otherwise every press
	// also goes to the code detector that starts a round.
	if oled.GameActive() {
		return
	}
	oled.FeedKey(ev.Name)
	if (ev.Name == "NEXT_TRACK" || ev.Name == "PREV_TRACK") && h.onRadioStepKey != nil {
		// While a radio key plays, Next and Previous step through the radio
		// keys (#1619); the webui pairs this report with the gabbo skip frame
		// of the same press and does nothing on any other source. Off the
		// reader's goroutine: a step recalls a station. The key's webhook
		// below still fires, as it always did.
		go h.onRadioStepKey(ev.Name == "NEXT_TRACK")
	}
	if ev.Name == "AUX_INPUT" && h.keyActions.Action(keyactions.KeyAux) == keyactions.ActionSnooze && h.onAuxSnoozeKey != nil {
		// AUX has no webhook trigger here (its webhook follows the source
		// change), but a trace that names the key is the surest sign of an
		// AUX press for snooze; the source change of the same press is then a
		// repeat. Off the reader's goroutine: a snooze sends a stop.
		h.logger.Info("snooze key pressed", "key", ev.Name, "producer", ev.Producer.String())
		go h.onAuxSnoozeKey()
		return
	}
	id := webhooks.KeyTriggerID(ev.Name)
	if id == "" {
		return
	}
	if h.groupKeys.Bound(id) {
		// Off the reader's goroutine, same as the webhook: a form takes
		// seconds and must not stall the log stream.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), groupKeyToggleBudget)
			defer cancel()
			h.logger.Info("group key pressed", "key", ev.Name, "producer", ev.Producer.String())
			h.groupKeys.Toggle(ctx, id)
		}()
		return
	}
	if h.keyActions.Action(id) == keyactions.ActionSleep && h.onSleepKey != nil {
		// Synchronous: stepping the timer is a few field writes and a timer
		// reset, and keeping it on the reader's goroutine keeps quick presses
		// in order. The group push runs on its own goroutine.
		h.onSleepKey()
		return
	}
	if m := keyactions.SleepMinutes(h.keyActions.Action(id)); m > 0 && h.onSleepFixedKey != nil {
		// A fixed sleep length (#1242): synchronous like the stepping key.
		h.onSleepFixedKey(m)
		return
	}
	if a := h.keyActions.Action(id); keyactions.IsPlayMode(a) && h.onPlayModeKey != nil {
		// Shuffle or repeat (#1679). Off the reader's goroutine: a press
		// reads the engine and writes the new state.
		h.logger.Info("play mode key pressed", "key", ev.Name, "action", a, "producer", ev.Producer.String())
		go h.onPlayModeKey(a)
		return
	}
	if a := h.keyActions.Action(id); keyactions.IsBass(a) && h.onBassKey != nil {
		// Off the reader's goroutine: a step reads the speaker's bass and
		// writes the new level. The handler runs presses one at a time.
		h.logger.Info("bass key pressed", "key", ev.Name, "action", a, "producer", ev.Producer.String())
		go h.onBassKey(a)
		return
	}
	if h.keyActions.Action(id) == keyactions.ActionSnooze && h.onSnoozeKey != nil {
		// Off the reader's goroutine: deciding reads the speaker once and a
		// snooze sends a stop. Outside a ringing alarm it does nothing, and
		// the webhook still does not fire: the key carries snooze.
		h.logger.Info("snooze key pressed", "key", ev.Name, "producer", ev.Producer.String())
		go h.onSnoozeKey(id)
		return
	}
	if h.webhooks == nil {
		return
	}
	// Off the reader's goroutine: a webhook request must not stall the log
	// stream, and the fire has its own 8 s HTTP timeout.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if fired := h.webhooks.FireKey(ctx, id); fired != "" {
			h.logger.Info("key webhook fired", "key", ev.Name, "producer", ev.Producer.String(), "trigger", fired)
		}
	}()
}

// logKeyEvent writes one INFO line per press/release, deduplicated for the
// volume keys. Repeat/hold states stay at DEBUG: a held key emits them in a
// burst.
func (h *presetWsHandler) logKeyEvent(ev boxlog.KeyEvent) {
	if ev.State != boxlog.StatePressed && ev.State != boxlog.StateReleased {
		h.logger.Debug("box key", "key", ev.Name, "producer", ev.Producer.String(), "state", ev.State.String(), "origin", string(ev.Origin))
		return
	}
	switch ev.Name {
	case "VOLUME_UP", "VOLUME_DOWN", "MUTE":
		keyLogMu.Lock()
		last := keyLogLast[ev.Name]
		if time.Since(last) < keyLogDedup {
			keyLogMu.Unlock()
			return
		}
		keyLogLast[ev.Name] = time.Now()
		keyLogMu.Unlock()
	}
	h.logger.Info("box key", "key", ev.Name, "producer", ev.Producer.String(), "state", ev.State.String(), "origin", string(ev.Origin))
	if ev.Name == "POWER" && ev.Producer.Physical() {
		h.notePowerKey(ev)
	}
}

// notePowerKey writes a WARN when the speaker read one push of the power key
// as two presses: the firmware switches off on the first and straight back on
// on the second, so the speaker "does not stay off" (#1798). Logging only;
// the bundle then shows whether the double read still happens and what came
// right before it.
func (h *presetWsHandler) notePowerKey(ev boxlog.KeyEvent) {
	at := ev.At
	if at.IsZero() {
		at = time.Now()
	}
	keyLogMu.Lock()
	last := powerReleaseAt
	if ev.State == boxlog.StateReleased {
		powerReleaseAt = at
	}
	keyLogMu.Unlock()
	if ev.State != boxlog.StatePressed {
		return
	}
	if gap, ok := powerKeyBounced(last, at); ok {
		h.logger.Warn("box key: the speaker read the power key as pressed again right after it was let go; it takes that as a second power press and switches straight back on (#1798)",
			"producer", ev.Producer.String(), "gapMs", gap.Milliseconds(), "panelHeld", oled.TrackShowing())
	}
}

// keyTraceExplainsBareFrame reports whether the key trace just named the key
// behind a bare userActivityUpdate as one of the transport keys (thumbs,
// back, forward, play/pause). Then the frame is fully explained: the per-key
// webhook already fired from OnKeyEvent, and the press was not a dead preset
// key, so the bare-frame heuristic has nothing left to do. A preset key named
// by the trace deliberately does NOT count: a lone frame after a PRESET_n
// press is the #342 dead-key-layer signature and must keep scheduling the
// re-sync.
func (h *presetWsHandler) keyTraceExplainsBareFrame() bool {
	if h.keyTrace == nil {
		return false
	}
	ev, ok := h.keyTrace.LastKeyEventWithin(keyTraceStandDown)
	if !ok || !ev.Producer.Physical() {
		return false
	}
	return webhooks.KeyTriggerID(ev.Name) != ""
}

// presetPressTrace is the part of the key trace a folder key's press check
// reads; *boxlog.Reader in the agent, a fake in the tests.
type presetPressTrace interface {
	Healthy() bool
	PresetKeysTraced() bool
	LastPhysicalPressSince(name string, since time.Time) (time.Time, bool)
}

// folderKeyTracePoll is how often folderKeyPress looks at the trace while it
// waits for a trailing line. A var so the tests can shorten it.
var folderKeyTracePoll = 100 * time.Millisecond

// folderKeyPress is the key trace's answer for a selection frame of key
// (webui.SlotPress.KeyTrace): the newest press of PRESET_<key> a person made
// at or after since. Not known while the trace is not running, or before it
// has read any preset key press on this speaker: a speaker whose trace does
// not report its preset keys must not have every press read as "nobody
// pressed". The trace line can trail the frame by about a second on the
// Portable (keyTraceWait), so a trace that shows no press yet is waited for,
// up to wait. Only asked when the answer changes what a press does (the key
// of the folder that plays), so no other recall waits for it.
func folderKeyPress(tr presetPressTrace, key int, wait time.Duration) func(since time.Time) (time.Time, bool, bool) {
	if tr == nil {
		return nil
	}
	name := "PRESET_" + strconv.Itoa(key)
	return func(since time.Time) (time.Time, bool, bool) {
		if !tr.Healthy() || !tr.PresetKeysTraced() {
			return time.Time{}, false, false
		}
		deadline := time.Now().Add(wait)
		for {
			if at, ok := tr.LastPhysicalPressSince(name, since); ok {
				return at, true, true
			}
			if !time.Now().Before(deadline) {
				return time.Time{}, false, true
			}
			time.Sleep(folderKeyTracePoll)
		}
	}
}

// waitForKeyTrace waits, bounded by keyTraceWait, for the key trace to name a
// transport key behind the bare frame that just settled. False means the
// trace is not healthy, or it named nothing (or a preset key) in time.
func (h *presetWsHandler) waitForKeyTrace() bool {
	if h.keyTrace == nil || !h.keyTrace.Healthy() {
		return false
	}
	deadline := time.Now().Add(keyTraceWait)
	for {
		if h.keyTraceExplainsBareFrame() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}
