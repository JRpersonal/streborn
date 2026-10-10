package boxlog

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The firmware's own verdict on a preset key: a tap or a press-and-hold.
//
// The key lines (keys.go) say THAT a preset key went down, and on the sm2
// chassis only that: the analytics line is press-only, so a tap and a hold
// look identical there. The firmware's system controller, however, logs at
// its default INFO level which of the two it made of the press:
//
//	[(tid):HSM:INFO]SysCtlr: HandleMessage(EVT_KEY_PRESET_PRESS) >> On > StdOpOn > StdOp
//	[(tid):HSM:INFO]SysCtlr: HandleMessage(EVT_KEY_PRESET_PRESS_AND_HOLD) >> On > StdOpOn
//
// Measured live on an ST10 (rhino, 27.0.6) on 2026-10-10 with keys sent over
// :8090/key while a library file played as a UPnP push: a tap logs
// EVT_KEY_PRESET_PRESS at the release; a hold logs
// EVT_KEY_PRESET_PRESS_AND_HOLD once the key has been down between 1.5 s and
// 1.75 s (a 1.5 s press still activated the key, a 1.75 s press did not),
// whether or not the key holds a preset, and nothing more at the release.
// Neither line names the key; the press that preceded it does.
//
// The same probe showed the network keys' own line, which the firmware writes
// for every key that arrives over /key (the app, STR itself) and never for a
// physical one:
//
//	[(tid):WebClientTO:INFO]ProcessKey: key = 12 state = 0

// PresetGesture is one firmware verdict on a preset key press.
type PresetGesture struct {
	// Hold is true for a press-and-hold, false for a tap (a select).
	Hold bool
	At   time.Time
}

// PresetGestureHandler receives each verdict. It runs on the reader's
// goroutine, so it must not block.
type PresetGestureHandler func(PresetGesture)

// OriginWeb marks a key that arrived over the speaker's HTTP API (:8090/key),
// read from the firmware's WebClientTO line.
const OriginWeb Origin = "web"

var webKeyRe = regexp.MustCompile(`ProcessKey: key = (\d+) state = (\d+)`)

// ParsePresetGestureLine decodes the system controller's tap/hold verdict.
func ParsePresetGestureLine(line string, now time.Time) (PresetGesture, bool) {
	if !strings.Contains(line, "HandleMessage(EVT_KEY_PRESET_PRESS") {
		return PresetGesture{}, false
	}
	switch {
	case strings.Contains(line, "HandleMessage(EVT_KEY_PRESET_PRESS_AND_HOLD)"):
		return PresetGesture{Hold: true, At: now}, true
	case strings.Contains(line, "HandleMessage(EVT_KEY_PRESET_PRESS)"):
		return PresetGesture{Hold: false, At: now}, true
	}
	return PresetGesture{}, false
}

// ParseWebKeyLine decodes the firmware's line for a key sent over /key. The
// producer is always the network: a physical key never writes this line.
func ParseWebKeyLine(line string, now time.Time) (KeyEvent, bool) {
	if !strings.Contains(line, "ProcessKey: key = ") {
		return KeyEvent{}, false
	}
	m := webKeyRe.FindStringSubmatch(line)
	if m == nil {
		return KeyEvent{}, false
	}
	key, _ := strconv.Atoi(m[1])
	state, _ := strconv.Atoi(m[2])
	return KeyEvent{
		Key: key, Name: KeyName(key), Producer: ProducerGabbo, State: State(state),
		Origin: OriginWeb, At: now,
	}, true
}

// SetPresetGestureHandler installs the tap/hold hook. Set before Run; nil
// disables it.
func (r *Reader) SetPresetGestureHandler(h PresetGestureHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gestureHandler = h
}

// SetWebKeyHandler installs the hook for keys sent over the speaker's HTTP
// API. They are kept out of the key handler and the key history on purpose:
// those answer "what did a person press", and a key STR sends itself must
// never mask or impersonate one. Set before Run; nil disables it.
func (r *Reader) SetWebKeyHandler(h Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.webKeyHandler = h
}

// handleGestureLines feeds the two hooks above; false when the line was
// neither, so the caller goes on to the key parse.
func (r *Reader) handleGestureLines(line string, now time.Time) bool {
	if g, ok := ParsePresetGestureLine(line, now); ok {
		r.mu.Lock()
		h := r.gestureHandler
		r.mu.Unlock()
		if h != nil {
			h(g)
		}
		return true
	}
	if ev, ok := ParseWebKeyLine(line, now); ok {
		r.mu.Lock()
		h := r.webKeyHandler
		r.mu.Unlock()
		if h != nil {
			h(ev)
		}
		return true
	}
	return false
}
