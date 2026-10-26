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
