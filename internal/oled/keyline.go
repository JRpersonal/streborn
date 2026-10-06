package oled

import (
	"bytes"
	"strconv"
)

// KeyPress is one remote key state change: Key in BoseApp's KEY_VAL order,
// State as BoseApp logs it (0 press, 1 release, 2 repeat while held, 3 and 4
// press-and-hold stages, 5 timeout).
type KeyPress struct {
	Key   int
	State int
}

// Key states.
const (
	KeyPressed  = 0
	KeyReleased = 1
	KeyRepeat   = 2
)

var keyMarker = []byte("IR Key event: Key()=")

// parseKeyLine reads key and state out of BoseApp's syslog line.
func parseKeyLine(b []byte) (KeyPress, bool) {
	i := bytes.Index(b, keyMarker)
	if i < 0 {
		return KeyPress{}, false
	}
	num := func(r []byte) (int, bool) {
		j := 0
		for j < len(r) && r[j] >= '0' && r[j] <= '9' {
			j++
		}
		n, err := strconv.Atoi(string(r[:j]))
		return n, err == nil
	}
	rest := b[i+len(keyMarker):]
	key, ok := num(rest)
	if !ok {
		return KeyPress{}, false
	}
	k := bytes.Index(rest, []byte("State()="))
	if k < 0 {
		return KeyPress{}, false
	}
	state, ok := num(rest[k+len("State()="):])
	return KeyPress{Key: key, State: state}, ok
}
