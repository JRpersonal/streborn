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
