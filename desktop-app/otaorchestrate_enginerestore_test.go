package main

import (
	"errors"
	"testing"
	"time"
)

// oldVersionEngineDropped is the old agent still running after an update that
// did not complete, its engine dropped for that update until an engine call
// has landed (and, with dropped=false, an engine that was simply never there).
func oldVersionEngineDropped(fb *otaFakeBox, dropped bool) func(time.Time) (map[string]string, error) {
	return func(time.Time) (map[string]string, error) {
		v := oldVersion()
		fb.mu.Lock()
		calls := len(fb.engineCalls)
		fb.mu.Unlock()
		if fb.pushTime().IsZero() || calls > 0 {
			return v, nil
		}
		v["goLibrespot"] = "missing"
		if dropped {
			v["goLibrespotDroppedForUpdate"] = "true"
		}
		return v, nil
	}
}

// The 2026-10-25 field case: the upload dies, the speaker keeps its old
// agent, but its engine is gone. The run puts the engine back and still
// reports the update as not done.
func TestIncompleteUpdateRestoresDroppedEngine(t *testing.T) {
	for _, c := range []struct {
		name    string
		pushErr error
		verdict string
		outcome string
	}{
		{"clean reject", errors.New("status 400: read: connection reset by peer"), "", outcomeFailed},
		{"timed out, not landed", errors.New(`Post "http://192.0.2.1:8888/api/agent/update": context deadline exceeded`), "not-landed", outcomeTimeout},
	} {
		t.Run(c.name, func(t *testing.T) {
			fb := newOtaFakeBox()
			fb.pushErr = c.pushErr
			if c.verdict != "" {
				fb.verdicts = []string{c.verdict}
			}
			fb.versionAt = oldVersionEngineDropped(fb, true)
			fb.engineReplies = []engineReply{{res: "ok"}}
			out, _ := runSingle(t, fb)
			if out.Outcome != c.outcome {
				t.Fatalf("the update did not complete and must say so, got %+v", out)
			}
			if len(fb.engineCalls) != 1 {
				t.Fatalf("want exactly one engine delivery, got %d", len(fb.engineCalls))
			}
			if !out.EngineDelivered {
				t.Error("the restored engine is reported")
			}
			if !fb.hasOutcome("engine back on the speaker") {
				t.Errorf("the restore is journaled, got %v", fb.outcomes)
			}
			if got := fb.phaseIDs(); got[len(got)-1] != c.outcome {
				t.Errorf("the terminal phase stays %s, phases %v", c.outcome, got)
			}
		})
	}
}

// Only the speaker's own "dropped for an update" counts. An engine that was
// never installed, and the outcomes where no delivery can land, are left alone.
func TestIncompleteUpdateLeavesEngineAloneWithoutTheMarker(t *testing.T) {
	t.Run("never installed", func(t *testing.T) {
		fb := newOtaFakeBox()
		fb.pushErr = errors.New("status 500: write failed")
		fb.versionAt = oldVersionEngineDropped(fb, false)
		if _, err := runSingle(t, fb); err == nil {
			t.Fatal("want the push failure")
		}
		if len(fb.engineCalls) != 0 {
			t.Errorf("no marker, no delivery; got %d", len(fb.engineCalls))
		}
	})
	t.Run("agent gone", func(t *testing.T) {
		fb := newOtaFakeBox()
		fb.pushErr = errors.New("context deadline exceeded")
		fb.verdicts = []string{"agent-gone"}
		fb.versionAt = oldVersionEngineDropped(fb, true)
		out, _ := runSingle(t, fb)
		if out.Outcome != outcomeAgentGone || len(fb.engineCalls) != 0 {
			t.Errorf("agent-gone gets no delivery, got %+v with %d engine calls", out, len(fb.engineCalls))
		}
	})
}

// A cut stream is retried once; a full speaker is not retried at all.
func TestIncompleteUpdateEngineRestoreRetryBounds(t *testing.T) {
	t.Run("one retry", func(t *testing.T) {
		fb := newOtaFakeBox()
		fb.pushErr = errors.New("status 400: read: connection reset by peer")
		fb.versionAt = func(time.Time) (map[string]string, error) {
			v := oldVersion()
			v["goLibrespot"] = "missing"
			v["goLibrespotDroppedForUpdate"] = "true"
			return v, nil
		}
		fb.engineReplies = []engineReply{{err: errors.New("broken pipe")}}
		out, _ := runSingle(t, fb)
		if len(fb.engineCalls) != engineRestoreAttempts {
			t.Errorf("want %d attempts, got %d", engineRestoreAttempts, len(fb.engineCalls))
		}
		if out.EngineDelivered || !fb.hasOutcome("engine re-delivery after the incomplete update did not succeed") {
			t.Errorf("a failed restore is journaled and not reported as delivered: %+v %v", out, fb.outcomes)
		}
	})
	t.Run("no room", func(t *testing.T) {
		fb := newOtaFakeBox()
		fb.pushErr = errors.New("status 400: read: connection reset by peer")
		fb.versionAt = oldVersionEngineDropped(fb, true)
		fb.engineReplies = []engineReply{{err: errors.New("status 507: insufficient NAND space")}}
		out, _ := runSingle(t, fb)
		if len(fb.engineCalls) != 1 || !out.EngineTooFull {
			t.Errorf("a full speaker is not retried and is reported, got %d calls, %+v", len(fb.engineCalls), out)
		}
	})
}

// droppedUntilEngineCalls is the old agent with its engine dropped until the
// n-th engine call has been made.
func droppedUntilEngineCalls(fb *otaFakeBox, n int) func(time.Time) (map[string]string, error) {
	return func(time.Time) (map[string]string, error) {
		v := oldVersion()
		fb.mu.Lock()
		calls := len(fb.engineCalls)
		fb.mu.Unlock()
		if calls >= n {
			return v, nil
		}
		v["goLibrespot"] = "missing"
		v["goLibrespotDroppedForUpdate"] = "true"
		return v, nil
	}
}

// The desktop's stall watchdog cancels a push over a dead link while the
// speaker still sits in its read and holds the update latch. Its 409
// update-in-flight refusals send nothing, so they must not use up the two
// attempts: the restore waits the latch out and then delivers.
func TestIncompleteUpdateEngineRestoreWaitsOutTheUpdateLatch(t *testing.T) {
	fb := newOtaFakeBox()
	fb.pushErr = errors.New("context canceled")
	fb.versionAt = droppedUntilEngineCalls(fb, 5)
	fb.engineReplies = []engineReply{
		{res: EngineDeferredUpdateInFlight}, {res: EngineDeferredUpdateInFlight},
		{res: EngineDeferredUpdateInFlight}, {res: EngineDeferredUpdateInFlight},
		{res: "ok"},
	}
	out, _ := runSingle(t, fb)
	if out.Outcome != outcomeFailed {
		t.Fatalf("the update did not complete, got %+v", out)
	}
	if len(fb.engineCalls) != 5 {
		t.Fatalf("four refusals then one delivery, got %d engine calls", len(fb.engineCalls))
	}
	if !out.EngineDelivered || !fb.hasOutcome("engine back on the speaker") {
		t.Errorf("the restore lands once the latch clears: %+v %v", out, fb.outcomes)
	}
	if !fb.hasOutcome("engine re-delivery: the speaker still reports its update in flight") {
		t.Errorf("the latch wait is journaled, got %v", fb.outcomes)
	}
	// The refusals are spaced out, not fired back to back.
	if gap := fb.engineCalls[1].Sub(fb.engineCalls[0]); gap < otaProductionPolicy.backoffBase {
		t.Errorf("refusals retried %s apart, want at least the backoff", gap)
	}
}

// A latch that never clears ends the restore at the window, journaled, with
// the update's own outcome untouched.
func TestIncompleteUpdateEngineRestoreLatchWaitIsBounded(t *testing.T) {
	fb := newOtaFakeBox()
	fb.pushErr = errors.New("context canceled")
	fb.versionAt = droppedUntilEngineCalls(fb, 1<<30)
	fb.engineReplies = []engineReply{{res: EngineDeferredUpdateInFlight}}
	out, _ := runSingle(t, fb)
	if out.Outcome != outcomeFailed || out.EngineDelivered {
		t.Fatalf("got %+v", out)
	}
	if len(fb.engineCalls) <= engineRestoreAttempts {
		t.Errorf("refusals do not count as attempts, got only %d calls", len(fb.engineCalls))
	}
	if span := fb.engineCalls[len(fb.engineCalls)-1].Sub(fb.engineCalls[0]); span > otaProductionPolicy.engineWindow+otaProductionPolicy.backoffCap {
		t.Errorf("latch wait ran %s, past the window", span)
	}
	if !fb.hasOutcome("engine re-delivery after the incomplete update did not succeed: the speaker kept reporting") {
		t.Errorf("the give-up is journaled, got %v", fb.outcomes)
	}
}

func TestEngineSent(t *testing.T) {
	for res, want := range map[string]bool{
		"ok": true, "engine-staged-reboot-failed": true,
		"current": false, EngineDeferredUpdateInFlight: false,
	} {
		if got := engineSent(res); got != want {
			t.Errorf("engineSent(%q) = %v, want %v", res, got, want)
		}
	}
}

// In a batch the restore waits for the other speakers' agent pushes. Queued
// in the shared gate ahead of them, its ~16 MB over the failed link held
// every remaining speaker's update behind it.
func TestFleetEngineRestoreYieldsToTheBatchPushes(t *testing.T) {
	ff := newFleetFake()
	ff.add("ok", "present")
	ff.pushDelay = 120 * time.Millisecond // a long reply wait after the wire, so jumping the queue shows
	bad := ff.add("bad", "missing")
	bad.version["goLibrespotDroppedForUpdate"] = "true"
	bad.pushErr = errors.New("status 400: read: connection reset by peer")
	s := ff.seams()
	// "ok" starts late, so "bad" pushes and fails first and its restore is
	// due while "ok" still has its agent push to make.
	read := s.readVersion
	var okDelayed bool
	s.readVersion = func(host string, port int) (map[string]string, error) {
		ff.mu.Lock()
		first := host == "ok" && !okDelayed
		if first {
			okDelayed = true
		}
		ff.mu.Unlock()
		if first {
			time.Sleep(30 * time.Millisecond)
		}
		return read(host, port)
	}
	var engineAt time.Time
	ensure := s.ensureEngine
	s.ensureEngine = func(host string, port int) (string, error) {
		if host == "bad" {
			ff.mu.Lock()
			if engineAt.IsZero() {
				engineAt = time.Now()
			}
			ff.mu.Unlock()
		}
		return ensure(host, port)
	}
	out, err := orchestrateFleet(s, fleetPolicy, []FleetTarget{{Host: "ok", Port: 1}, {Host: "bad", Port: 1}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var okEnd time.Time
	var badStart time.Time
	for _, p := range ff.pushLog {
		switch p.host {
		case "ok":
			okEnd = p.end
		case "bad":
			badStart = p.start
		}
	}
	if okEnd.IsZero() || engineAt.IsZero() {
		t.Fatalf("want an ok push and a bad restore, log %+v, rows %+v", ff.pushLog, out.Rows)
	}
	if !badStart.Before(okEnd) {
		t.Skip("scheduling put the bad push after the ok one; nothing to yield to")
	}
	if engineAt.Before(okEnd) {
		t.Errorf("the restore went out %s before the other speaker's agent push had finished", okEnd.Sub(engineAt))
	}
	for _, r := range out.Rows {
		if r.Host == "bad" && (r.Outcome != outcomeFailed || !r.EngineDelivered) {
			t.Errorf("bad row: %+v", r)
		}
		if r.Host == "ok" && r.Outcome != outcomeDone {
			t.Errorf("ok row: %+v", r)
		}
	}
}
