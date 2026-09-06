package main

import (
	"context"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxlog"
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

var (
	keyLogMu   sync.Mutex
	keyLogLast = map[string]time.Time{}
)

// OnKeyEvent receives every key state change the speaker itself decoded (see
// internal/boxlog). It logs the event and, for a physical press of one of the
// transport keys, fires the per-key webhook. Presets, AUX and power keep their
// existing paths (the box's own selection/source/power frames), so nothing
// fires twice; volume and the rest are logged only.
func (h *presetWsHandler) OnKeyEvent(ev boxlog.KeyEvent) {
	h.logKeyEvent(ev)
	if !ev.Pressed() || !ev.Producer.Physical() {
		return
	}
	id := webhooks.KeyTriggerID(ev.Name)
	if id == "" || h.webhooks == nil {
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
