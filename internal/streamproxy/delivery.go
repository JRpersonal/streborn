package streamproxy

// Whether the station currently being served has ever produced a byte.
//
// Package-level, like LastSlotMiss beside it and for the same reason: the agent
// runs one proxy, and the caller that needs this is the native-preset latch in
// cmd/agent, which has no handle on the Server.
//
// It exists because a speaker that abandons a station it just started is
// normally evidence about the speaker, and that is what the latch counts. It is
// NOT evidence about the speaker when nothing ever arrived for it to play: then
// the box gave up waiting on a fetch that never answered, and moving its presets
// to the slower form cannot help, because the failure is upstream of both forms.
//
// Measured on a SoundTouch 20 on 2026-09-27: about fifty attempts, not one
// upstream response, and the latch read the resulting drops as "the firmware
// rejects native stations" and permanently downgraded four slots.

import (
	"sync"
	"time"
)

var delivery struct {
	sync.Mutex
	url       string
	any       bool
	startedAt time.Time
}

// noteDeliveryStart records that a station was handed to the box, with nothing
// delivered for it yet.
func noteDeliveryStart(url string) {
	if url == "" {
		return
	}
	delivery.Lock()
	defer delivery.Unlock()
	if url != delivery.url {
		delivery.url = url
		delivery.any = false
		delivery.startedAt = time.Now()
		return
	}
	if delivery.startedAt.IsZero() {
		delivery.startedAt = time.Now()
	}
}

// noteDelivered records that bytes reached the box for url.
func noteDelivered(url string) {
	delivery.Lock()
	defer delivery.Unlock()
	if url != "" && url != delivery.url {
		delivery.url = url
		delivery.startedAt = time.Now()
	}
	delivery.any = true
}

// NothingDeliveredYet reports whether a station is being served for which not
// one byte has arrived, and for how long that has been the case.
//
// ok is false when no station is in play at all, so a caller cannot mistake
// "nothing is happening" for "the fetch is failing".
func NothingDeliveredYet() (since time.Duration, ok bool) {
	delivery.Lock()
	defer delivery.Unlock()
	if delivery.url == "" || delivery.startedAt.IsZero() || delivery.any {
		return 0, false
	}
	return time.Since(delivery.startedAt), true
}

// ResetDeliveryForTest clears the record between tests.
func ResetDeliveryForTest() {
	delivery.Lock()
	defer delivery.Unlock()
	delivery.url = ""
	delivery.any = false
	delivery.startedAt = time.Time{}
}
