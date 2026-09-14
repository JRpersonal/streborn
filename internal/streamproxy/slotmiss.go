package streamproxy

import (
	"sync"
	"time"
)

// The last time the proxy was asked for a slot it cannot serve.
//
// It is recorded because the failure is invisible where it does damage. A box
// that fetches /stream/<slot> and gets a 404 leaves the station it had just
// been given, and the agent's native-preset watchdog reads that as "this
// speaker cannot keep a native station" and eventually puts every slot on the
// slower UPnP form. Three grouped SoundTouch 10s on 2026-09-13 spent four
// presses that way on a slot only the follower had, each one refused by the
// master's own proxy, and the speaker was blamed for all four.
//
// A slot miss is our answer, not the speaker's fault, so the watchdog needs to
// be able to tell that one of ours came first. Package-level because there is
// one proxy per agent and the watchdog lives in another package with no handle
// on the server.
var lastMiss struct {
	sync.Mutex
	slot int
	at   time.Time
}

// noteSlotMiss records that the proxy refused a fetch for slot, having nothing
// playable there.
func noteSlotMiss(slot int) {
	lastMiss.Lock()
	lastMiss.slot = slot
	lastMiss.at = time.Now()
	lastMiss.Unlock()
}

// LastSlotMiss reports the most recent slot the proxy could not serve and how
// long ago it was refused. ok is false when this agent has never refused one.
func LastSlotMiss() (slot int, ago time.Duration, ok bool) {
	lastMiss.Lock()
	defer lastMiss.Unlock()
	if lastMiss.at.IsZero() {
		return 0, 0, false
	}
	return lastMiss.slot, time.Since(lastMiss.at), true
}
