// Package boxlog reads the speaker's own trace out of the firmware's syslog
// ring buffer and turns the lines that matter into typed events.
//
// The firmware (BoseApp) logs through its DPrint facility into busybox
// syslogd, which on every SoundTouch chassis runs with a RAM-only circular
// buffer (`syslogd -C512`). `logread -f` streams that buffer; nothing is ever
// written to NAND, so following it costs one small child process and a pipe.
//
// The first consumer is the remote/console key trace. The gabbo WebSocket only
// ever reports a bare <userActivityUpdate/> for the remote's lower keys, so
// STR could not tell Back, Forward, Thumbs down and Thumbs up apart (Discussion
// #827). BoseApp itself decodes the IR code and, once its `IrDevice` and
// `ConsoleButtons` facilities are raised to DEBUG over the TAP port, logs
//
//	[(tid):IrDevice:DEBUG]IR Key event: Key()=5, State()=0, Producer()=2
//	[(tid):ConsoleButtons:DEBUG]CONSOLE Key event: Key()=12, State()=1, Producer()=1
//
// for every press (State 0) and release (State 1). Measured live on a Portable
// (taigan) and cross-checked against the KEY_VAL_* enum in the BoseApp string
// table, 2026-09-06. On the sm2 chassis (rhino, SoundTouch 10) those two
// facilities stay silent, but the firmware's analytics client logs at its
// default INFO level
//
//	[(tid):StatsDataCaptureClient:INFO]buildJson(), like-pressed, THUMBS_UP, ir-remote
//
// once per press, which carries the same key identity without a release.
package boxlog

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Producer is who pressed the key, per the firmware's KEY_PRODUCER_* enum.
type Producer int

const (
	ProducerGabbo       Producer = 0 // a network client: the app, /key on :8090
	ProducerConsole     Producer = 1 // the buttons on top of the speaker
	ProducerIRRemote    Producer = 2 // the infrared remote
	ProducerLightswitch Producer = 3
	ProducerETAP        Producer = 4
	ProducerBoselink    Producer = 5
	ProducerUnknown     Producer = -1
)

// String returns the short label used in logs and the debug section.
func (p Producer) String() string {
	switch p {
	case ProducerGabbo:
		return "app"
	case ProducerConsole:
		return "console"
	case ProducerIRRemote:
		return "remote"
	case ProducerLightswitch:
		return "lightswitch"
	case ProducerETAP:
		return "etap"
	case ProducerBoselink:
		return "boselink"
	}
	return "unknown"
}

// Physical reports whether a human pressed something on the speaker or its
// remote, as opposed to a network client sending a key.
func (p Producer) Physical() bool {
	return p == ProducerConsole || p == ProducerIRRemote || p == ProducerLightswitch || p == ProducerBoselink
}

// State is the key state per the firmware's KEY_STATE_* enum.
type State int

const (
	StatePressed  State = 0
	StateReleased State = 1
	StateRepeat   State = 2
	StatePnH      State = 3 // press-and-hold reached
	StateXPnH     State = 4 // extended press-and-hold
	StateTimeout  State = 5
)

// String returns the short label used in logs.
func (s State) String() string {
	switch s {
	case StatePressed:
		return "press"
	case StateReleased:
		return "release"
	case StateRepeat:
		return "repeat"
	case StatePnH:
		return "hold"
	case StateXPnH:
		return "long-hold"
	case StateTimeout:
		return "timeout"
	}
	return "state" + strconv.Itoa(int(s))
}

// keyNames is the firmware's KEY_VAL_* enum in declaration order. The indices
// were verified on hardware: Back=3, Forward=4, Thumbs up=5, Thumbs down=6,
// Volume up=10, Preset 1=12, Preset 2=13, AUX=18.
var keyNames = []string{
	"PLAY", "PAUSE", "STOP", "PREV_TRACK", "NEXT_TRACK", "THUMBS_UP",
	"THUMBS_DOWN", "BOOKMARK", "POWER", "MUTE", "VOLUME_UP", "VOLUME_DOWN",
	"PRESET_1", "PRESET_2", "PRESET_3", "PRESET_4", "PRESET_5", "PRESET_6",
	"AUX_INPUT", "SHUFFLE_OFF", "SHUFFLE_ON", "REPEAT_OFF", "REPEAT_ONE",
	"REPEAT_ALL", "PLAY_PAUSE", "ADD_FAVORITE", "REMOVE_FAVORITE", "BLUETOOTH",
}

// KeyName returns the firmware's name for a key number, or "KEY_<n>" for a
// number outside the known enum so a new firmware never hides a key.
func KeyName(key int) string {
	if key >= 0 && key < len(keyNames) {
		return keyNames[key]
	}
	return "KEY_" + strconv.Itoa(key)
}

// KeyNumber returns the enum number for a firmware key name, or -1.
func KeyNumber(name string) int {
	for i, n := range keyNames {
		if n == name {
			return i
		}
	}
	return -1
}

// Origin says which trace line an event came from.
type Origin string

const (
	// OriginTrace is the IrDevice/ConsoleButtons DEBUG line (press and release).
	OriginTrace Origin = "trace"
	// OriginStats is the analytics client's INFO line (press only).
	OriginStats Origin = "stats"
)

// KeyEvent is one decoded key state change on the speaker.
type KeyEvent struct {
	Key      int      `json:"key"`
	Name     string   `json:"name"`
	Producer Producer `json:"producer"`
	State    State    `json:"state"`
	Origin   Origin   `json:"origin"`
	// At is when the line was read, not the syslog timestamp: the ring's
	// timestamps have one-second resolution and the line arrives within
	// milliseconds of the press anyway.
	At time.Time `json:"at"`
}

// Pressed reports whether this is the initial press of a physical key.
func (e KeyEvent) Pressed() bool { return e.State == StatePressed }

var (
	traceRe = regexp.MustCompile(`(?:IR|CONSOLE) Key event: Key\(\)=(\d+), State\(\)=(\d+), Producer\(\)=(\d+)`)
	statsRe = regexp.MustCompile(`buildJson\(\), [a-z-]+-pressed, ([A-Z_0-9]+), ([a-z-]+)`)
)

// ParseKeyLine decodes one syslog line into a KeyEvent. ok is false for every
// line that is not a key trace, which is nearly all of them, so the cheap
// substring checks run before any regexp.
func ParseKeyLine(line string, now time.Time) (ev KeyEvent, ok bool) {
	if strings.Contains(line, "Key event: Key()=") {
		m := traceRe.FindStringSubmatch(line)
		if m == nil {
			return ev, false
		}
		key, _ := strconv.Atoi(m[1])
		state, _ := strconv.Atoi(m[2])
		prod, _ := strconv.Atoi(m[3])
		return KeyEvent{
			Key: key, Name: KeyName(key), Producer: Producer(prod), State: State(state),
			Origin: OriginTrace, At: now,
		}, true
	}
	if strings.Contains(line, "-pressed, ") {
		m := statsRe.FindStringSubmatch(line)
		if m == nil {
			return ev, false
		}
		return KeyEvent{
			Key: KeyNumber(m[1]), Name: m[1], Producer: statsProducer(m[2]), State: StatePressed,
			Origin: OriginStats, At: now,
		}, true
	}
	return ev, false
}

// statsProducer maps the analytics client's producer word onto the enum.
func statsProducer(s string) Producer {
	switch s {
	case "ir-remote":
		return ProducerIRRemote
	case "console":
		return ProducerConsole
	case "gabbo", "app":
		return ProducerGabbo
	case "lightswitch", "lightswitch-remote":
		return ProducerLightswitch
	}
	return ProducerUnknown
}
