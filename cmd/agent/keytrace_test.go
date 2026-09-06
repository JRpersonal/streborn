package main

import (
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxlog"
)

// TestKeyTraceExplainsBareFrame: a transport key named by the trace explains
// the bare gabbo frame (heuristic stands down); a preset key does not (the
// #342 dead-key path must keep running); an app-sent key does not either.
func TestKeyTraceExplainsBareFrame(t *testing.T) {
	h := &presetWsHandler{}
	if h.keyTraceExplainsBareFrame() {
		t.Fatal("nil reader must not explain anything")
	}
	push := func(name string, prod boxlog.Producer) {
		r := boxlog.New(nil, nil, nil)
		ev := boxlog.KeyEvent{Key: boxlog.KeyNumber(name), Name: name, Producer: prod, State: boxlog.StatePressed, Origin: boxlog.OriginTrace, At: time.Now()}
		r.Inject(ev)
		h.keyTrace = r
	}
	push("THUMBS_UP", boxlog.ProducerIRRemote)
	if !h.keyTraceExplainsBareFrame() {
		t.Fatal("remote thumbs up must explain the bare frame")
	}
	push("PRESET_3", boxlog.ProducerIRRemote)
	if h.keyTraceExplainsBareFrame() {
		t.Fatal("a preset key must leave the #342 path active")
	}
	push("NEXT_TRACK", boxlog.ProducerGabbo)
	if h.keyTraceExplainsBareFrame() {
		t.Fatal("an app-sent key is not a physical press")
	}
}

// TestBoxFailureAttrs: the firmware's own playback failure reason is appended
// to the recall-exhausted warning only when it arrived inside the window.
func TestBoxFailureAttrs(t *testing.T) {
	h := &presetWsHandler{}
	if h.boxFailureAttrs() != nil {
		t.Fatal("nil reader must add nothing")
	}
	r := boxlog.New(nil, nil, nil)
	h.keyTrace = r
	if h.boxFailureAttrs() != nil {
		t.Fatal("no failure yet must add nothing")
	}
	const line = `Sep  6 18:02:12 taigan local0.err APServer[1877]: [(002101):AudioIF:ERROR]SERVER ERROR: Server state has terminal error = BAD_URL and m_nMaxRetryAttempt =3`
	r.InjectLine(line, time.Now().Add(-2*boxReasonWindow))
	if h.boxFailureAttrs() != nil {
		t.Fatal("a stale reason must add nothing")
	}
	r.InjectLine(line, time.Now())
	attrs := h.boxFailureAttrs()
	if len(attrs) != 4 || attrs[0] != "boxReason" || attrs[1] != string(boxlog.ClassPlayServerError) {
		t.Fatalf("attrs %v", attrs)
	}
}
