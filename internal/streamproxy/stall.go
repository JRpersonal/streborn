// stall.go: how long the upstream stall watchdog waits, and the state a
// reconnect carries over from the connection it replaces (the box's estimated
// buffer and the last bytes the box received).
//
// #823 (SoundTouch 30, streamtheworld AAC at 96 kbps): inside single
// connections the edge went quiet for 2 to 15.7 s at a time and then delivered
// the backlog in a burst, so every one of those stalls recovered without loss.
// A fixed 15 s watchdog killed the ones that ran a little longer, each kill
// cost a reconnect, and each reconnect was a chance to splice the stream at
// the wrong place. The box had about half a minute of audio buffered the whole
// time, so it could afford to wait longer; a box that is nearly empty cannot.
// The watchdog therefore waits as long as the box's buffer allows, between the
// old 15 s and a hard ceiling that leaves room for the reconnect itself.

package streamproxy

import (
	"sync"
	"time"
)

const (
	// upstreamStallBase is the shortest the watchdog ever waits, and what it
	// waits when the box's buffer cannot be estimated (unknown bitrate). It is
	// the value that held from #823's first round until this change.
	upstreamStallBase = 15 * time.Second
	// upstreamStallMax is the longest it ever waits. A reconnect then takes up
	// to about 6.5 s before the first byte (the #823 log: holes of 17.5 to
	// 21.5 s behind 15 s stalls), and the trim may hold the burst for at least
	// drainBudgetMin, so 22 s still lands the first new byte inside the box's
	// ~30 s silence limit.
	upstreamStallMax = 22 * time.Second
	// stallReconnectReserve is the audio the box must still have buffered
	// when the watchdog fires: the reconnect overhead plus the minimum drain.
	stallReconnectReserve = 8 * time.Second
	// boxBufferCap is the most audio the box is assumed to hold. The #823
	// SoundTouch 30 swallowed about 33 s of burst in three to five seconds;
	// 30 s is that minus the share sitting in the TCP buffers.
	boxBufferCap = 30 * time.Second
)

// stallLimit is how long the watchdog lets a read wait for the upstream.
// buffered is the audio the box was estimated to hold when the wait began.
// The watchdog fires when that buffer would be down to stallReconnectReserve,
// but never before base (a short buffer is no reason to be more eager than the
// proven threshold) and never after max.
func stallLimit(base, max, buffered time.Duration) time.Duration {
	if max < base {
		max = base
	}
	d := buffered - stallReconnectReserve
	if d < base {
		return base
	}
	if d > max {
		return max
	}
	return d
}

// boxBuffer estimates how many seconds of audio the box holds: it fills with
// the audio duration of every byte written to the box and drains at real time,
// clamped to [0, boxBufferCap]. The clamps keep a slightly wrong bitrate from
// drifting the estimate over hours: a box that pushes back on writes is full,
// a box that has been starved is empty. It lives on the Server so a reconnect
// starts from what the previous connection left in the box.
type boxBuffer struct {
	mu    sync.Mutex
	level time.Duration // estimate at `at`
	at    time.Time
}

// reset empties the estimate, for a fresh stream.
func (b *boxBuffer) reset() {
	b.mu.Lock()
	b.level = 0
	b.at = time.Time{}
	b.mu.Unlock()
}

// add records n bytes written to the box at now, at bps bytes per second.
func (b *boxBuffer) add(n, bps int, now time.Time) {
	if n <= 0 || bps <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	lvl := b.levelLocked(now) + time.Duration(float64(n)/float64(bps)*float64(time.Second))
	if lvl > boxBufferCap {
		lvl = boxBufferCap
	}
	b.level = lvl
	b.at = now
}

// levelAt is the estimate at t (not earlier than the last add).
func (b *boxBuffer) levelAt(t time.Time) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.levelLocked(t)
}

func (b *boxBuffer) levelLocked(t time.Time) time.Duration {
	if b.at.IsZero() {
		return 0
	}
	lvl := b.level
	if t.After(b.at) {
		lvl -= t.Sub(b.at)
	}
	if lvl < 0 {
		lvl = 0
	}
	return lvl
}

// spliceTail holds the newest bytes the box received on the current stream,
// for trimBurst to find the resume point in a reconnect's burst. Fixed size
// (spliceTailSize), allocated once per Server.
type spliceTail struct {
	mu      sync.Mutex
	ring    *byteRing
	station string
}

// reset starts a fresh tail for station. An empty station disables recording
// until the next reset (the box is de-interleaving ICY itself, so the bytes it
// got are not the clean audio a burst is compared against).
func (t *spliceTail) reset(station string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ring == nil {
		t.ring = newByteRing(spliceTailSize)
	}
	t.ring.pos = 0
	t.ring.full = false
	t.station = station
}

// record appends p, the bytes just written to the box, if they belong to the
// station the tail is tracking.
func (t *spliceTail) record(station string, p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ring == nil || station == "" || station != t.station {
		return
	}
	t.ring.write(p)
}

// snapshot returns a copy of the tail for station, or nil when it tracks
// something else.
func (t *spliceTail) snapshot(station string) []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ring == nil || station == "" || station != t.station {
		return nil
	}
	return t.ring.bytes()
}
