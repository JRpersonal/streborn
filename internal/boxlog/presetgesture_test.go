package boxlog

import (
	"testing"
	"time"
)

// Message bodies verbatim from the kitchen ST10 (rhino) probe of 2026-10-10;
// the syslog prefix follows the shape of the 2026-09-06 captures.
const (
	lineRhinoPresetHold  = `Oct 10 12:59:34 rhino local0.info BoseApp[1943]: [(002164):HSM:INFO]SysCtlr: HandleMessage(EVT_KEY_PRESET_PRESS_AND_HOLD) >> On > StdOpOn`
	lineRhinoPresetTap   = `Oct 10 13:00:23 rhino local0.info BoseApp[1943]: [(002164):HSM:INFO]SysCtlr: HandleMessage(EVT_KEY_PRESET_PRESS) >> On > StdOpOn > StdOp`
	lineRhinoDisplaySet  = `Oct 10 12:59:34 rhino local0.info BoseApp[1943]: [(002164):HSM:INFO]DisplayController: HandleMessage(EVT_PRESET_SET_OR_SELECT_STATUS) >> OnShelby > OnPlayable > Main > Top`
	lineRhinoWebKeyPress = `Oct 10 12:59:32 rhino local0.info BoseApp[1943]: [(002170):WebClientTO:INFO]ProcessKey: key = 12 state = 0`
	lineRhinoWebKeyUnh   = `Oct 10 12:59:32 rhino local0.err BoseApp[1943]: [(002170):WebClientTO:ERROR]ProcessKey: Unhandled key 12`
)

func TestParsePresetGestureLine(t *testing.T) {
	now := time.Now()
	if g, ok := ParsePresetGestureLine(lineRhinoPresetHold, now); !ok || !g.Hold {
		t.Fatalf("hold line: got %+v ok=%v", g, ok)
	}
	if g, ok := ParsePresetGestureLine(lineRhinoPresetTap, now); !ok || g.Hold {
		t.Fatalf("tap line: got %+v ok=%v", g, ok)
	}
	for _, l := range []string{lineRhinoDisplaySet, lineRhinoWebKeyPress, lineNoise, ""} {
		if _, ok := ParsePresetGestureLine(l, now); ok {
			t.Fatalf("not a verdict: %q", l)
		}
	}
}

func TestParseWebKeyLine(t *testing.T) {
	ev, ok := ParseWebKeyLine(lineRhinoWebKeyPress, time.Now())
	if !ok || ev.Name != "PRESET_1" || ev.State != StatePressed || ev.Producer != ProducerGabbo || ev.Origin != OriginWeb {
		t.Fatalf("got %+v ok=%v", ev, ok)
	}
	if ev.Producer.Physical() {
		t.Fatal("a key sent over the API is never physical")
	}
	if _, ok := ParseWebKeyLine(lineRhinoWebKeyUnh, time.Now()); ok {
		t.Fatal("the Unhandled line is not a key event")
	}
}

// TestReaderRoutesGestureAndWebKeys: the verdict and the API key go to their
// own hooks, and neither reaches the key handler or the key history, which
// answer "what did a person press".
func TestReaderRoutesGestureAndWebKeys(t *testing.T) {
	var keys []KeyEvent
	r := New(nil, nil, func(ev KeyEvent) { keys = append(keys, ev) })
	var gestures []PresetGesture
	var web []KeyEvent
	r.SetPresetGestureHandler(func(g PresetGesture) { gestures = append(gestures, g) })
	r.SetWebKeyHandler(func(ev KeyEvent) { web = append(web, ev) })
	now := time.Now()
	r.InjectLine(lineRhinoWebKeyPress, now)
	r.InjectLine(lineRhinoPresetHold, now)
	r.InjectLine(lineRhinoPresetTap, now)
	if len(gestures) != 2 || !gestures[0].Hold || gestures[1].Hold {
		t.Fatalf("gestures = %+v", gestures)
	}
	if len(web) != 1 || web[0].Name != "PRESET_1" {
		t.Fatalf("web keys = %+v", web)
	}
	if len(keys) != 0 || r.KeyEventWithin(time.Minute) {
		t.Fatalf("the key handler and history must stay clean, got %+v", keys)
	}
}
