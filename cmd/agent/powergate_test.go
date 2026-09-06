package main

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxlog"
)

// newPowerTestHandler returns a handler whose two standby doors count their
// deliveries.
func newPowerTestHandler(strSource func() bool) (h *presetWsHandler, exits, entries *atomic.Int32) {
	exits, entries = new(atomic.Int32), new(atomic.Int32)
	h = &presetWsHandler{
		logger:            slog.Default(),
		onStandbyExit:     func() { exits.Add(1) },
		onEnterStandby:    func() { entries.Add(1) },
		strSourceRecently: strSource,
	}
	return h, exits, entries
}

// waitCount polls an async counter briefly (the wake door runs its action in
// a goroutine).
func waitCount(t *testing.T, c *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for c.Load() < want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	// Settle so a wrongly duplicated delivery has had time to land too.
	time.Sleep(30 * time.Millisecond)
	if got := c.Load(); got != want {
		t.Fatalf("want %d deliveries, got %d", want, got)
	}
}

func TestPowerGateSuppressesDuplicateWake(t *testing.T) {
	presetResyncAsk.Store(false)
	presetResyncLast.Store(0)
	// Bus first, ring a moment later: one delivery.
	h, exits, _ := newPowerTestHandler(nil)
	h.OnStandbyExit(context.TODO())
	h.OnBoxPowerEvent(boxlog.PowerEvent{Kind: boxlog.PowerWake, Source: boxlog.PowerSourceHSM, At: time.Now(), Class: boxlog.ClassWake})
	waitCount(t, exits, 1)
	snap := h.powerGate.snapshot()
	if snap["lastWakeSource"] != "gabbo" || snap["syslogDuplicates"] != uint64(1) {
		t.Fatalf("snapshot after bus-first wake: %+v", snap)
	}

	// Ring first, bus a moment later: still one delivery, credited to syslog.
	h, exits, _ = newPowerTestHandler(nil)
	h.OnBoxPowerEvent(boxlog.PowerEvent{Kind: boxlog.PowerWake, Source: boxlog.PowerSourceScmmond, At: time.Now(), Class: boxlog.ClassPowerEvent})
	h.OnStandbyExit(context.TODO())
	waitCount(t, exits, 1)
	snap = h.powerGate.snapshot()
	if snap["lastWakeSource"] != "syslog" || snap["gabboDuplicates"] != uint64(1) || snap["syslogDelivered"] != uint64(1) {
		t.Fatalf("snapshot after ring-first wake: %+v", snap)
	}
}

func TestPowerGateDeliversLoneSyslogWake(t *testing.T) {
	presetResyncAsk.Store(false)
	presetResyncLast.Store(0)
	h, exits, _ := newPowerTestHandler(nil)
	h.OnBoxPowerEvent(boxlog.PowerEvent{Kind: boxlog.PowerWake, Source: boxlog.PowerSourceHSM, At: time.Now(), Class: boxlog.ClassWake})
	waitCount(t, exits, 1)
	if !presetResyncAsk.Load() {
		t.Fatal("a syslog wake must schedule the key re-sync exactly as a bus standby exit does")
	}
	snap := h.powerGate.snapshot()
	if snap["lastWakeSource"] != "syslog" || snap["lastWakeSeen"].(map[string]string)["gabbo"] != "" {
		t.Fatalf("snapshot after lone syslog wake: %+v", snap)
	}
}

func TestPowerGateWindowExpires(t *testing.T) {
	h := &presetWsHandler{logger: slog.Default()}
	now := time.Now()
	if ok, _, _ := h.powerGate.admit(powerSignalWake, originGabbo, now); !ok {
		t.Fatal("first report must pass")
	}
	if ok, prev, _ := h.powerGate.admit(powerSignalWake, originSyslog, now.Add(powerSignalWindow-time.Millisecond)); ok || prev != originGabbo {
		t.Fatalf("report inside the window must be a duplicate of gabbo, ok=%v prev=%q", ok, prev)
	}
	// Same-origin repeats pass (the bus handlers keep their own debounce).
	if ok, _, _ := h.powerGate.admit(powerSignalWake, originGabbo, now.Add(time.Second)); !ok {
		t.Fatal("a same-origin repeat must not be gated")
	}
	if ok, _, _ := h.powerGate.admit(powerSignalWake, originSyslog, now.Add(time.Second+powerSignalWindow)); !ok {
		t.Fatal("report at the window edge is a new transition")
	}
}

func TestPowerGateStandbyFollowsDispatcherGate(t *testing.T) {
	// STR's source was not active: the ring's standby is noted, not acted on.
	h, _, entries := newPowerTestHandler(func() bool { return false })
	h.OnBoxPowerEvent(boxlog.PowerEvent{Kind: boxlog.PowerStandby, Source: boxlog.PowerSourceHSM, At: time.Now(), Class: boxlog.ClassStandby})
	waitCount(t, entries, 0)
	snap := h.powerGate.snapshot()
	if snap["lastStandbySource"] != "" || snap["lastStandbySeen"].(map[string]string)["syslog"] == "" {
		t.Fatalf("ungated standby must be seen but not delivered: %+v", snap)
	}

	// STR's source was active: delivered once, and the bus frame that
	// follows is the duplicate.
	h, _, entries = newPowerTestHandler(func() bool { return true })
	h.OnBoxPowerEvent(boxlog.PowerEvent{Kind: boxlog.PowerStandby, Source: boxlog.PowerSourceScmmond, At: time.Now(), Class: boxlog.ClassPowerSleep})
	h.OnEnterStandby(context.TODO())
	waitCount(t, entries, 1)
	snap = h.powerGate.snapshot()
	if snap["lastStandbySource"] != "syslog" || snap["gabboDuplicates"] != uint64(1) {
		t.Fatalf("snapshot after ring-first standby: %+v", snap)
	}
}
