// burst.go: trimming the upstream's burst-on-connect when STR RECONNECTS
// mid-stream, so the listener does not hear the last half minute again.
//
// The bug it fixes (#823, SoundTouch 30, reported "the stream jumps back about
// 20 to 30 seconds and then stays in that loop"): an Icecast/Triton edge hands
// every new connection a large prebuffer before it settles to real time. On the
// FIRST connect that burst is exactly right, it is the box's prebuffer. On a
// RECONNECT it is audio the listener has already heard, and STR forwarded it
// verbatim.
//
// From the reporter's own log, 13 independent connections to the same station
// delivered 364-400 KB (mean ~388 KB) in their first three to eight seconds and
// then settled to 11.4-12.3 KB/s, the nominal 96 kbps of the stream. That is
// 32 to 34 seconds of audio on every reconnect, against a median hole of 14
// seconds. The difference, about 19 seconds, is what he heard repeat. It looped
// because the reconnects came 7 to 90 seconds apart, so the next burst landed
// before the box had played out the last one and the audio position barely
// advanced.
//
// The trim keeps only as much of the burst as the hole actually needs.

package streamproxy

import (
	"bytes"
	"io"
	"time"
)

const (
	// trimBurstMaxBytes and trimBurstMaxWait bound the work. A station that
	// does not burst hits neither: the rate test below settles on its first
	// window. They exist for the pathological case of an upstream that keeps
	// delivering faster than real time indefinitely, where draining on would
	// be discarding audio nobody ever hears.
	trimBurstMaxBytes = 4 << 20
	trimBurstMaxWait  = 20 * time.Second
	// trimBurstKeepCap is the most audio the ring will hold, whatever the hole
	// says. A hole of minutes is not something a prebuffer should try to cover,
	// and this bounds the transient memory a reconnect costs on a speaker with
	// 120 MB of RAM.
	trimBurstKeepCap = 512 << 10
	// trimBurstWindow is how long a delivery window is measured over before the
	// rate is judged. Short enough that a station which does not burst is held
	// up only briefly, long enough that one chunky read does not read as a
	// burst.
	trimBurstWindow = 250 * time.Millisecond
	// trimBurstAheadFactor is how much faster than real time a window has to
	// arrive to still count as burst rather than live. Anything at or below it
	// is the live edge.
	trimBurstAheadFactor = 1.5
)

// trimBurst reads ahead of the box while the upstream is delivering FASTER than
// real time, keeping only the newest keep bytes and discarding the rest. It
// returns a reader that yields those kept bytes and then the live stream, so
// the caller's copy loop does not change.
//
// Nothing live is lost by construction: the ring keeps the NEWEST bytes, so
// whatever arrives while the rate is being judged is part of what the box gets.
// The only cost on a station that does not burst is one short window of delay
// on a reconnect that has already been silent for seconds.
//
// The stop condition is a delivery RATE over a window, not a gap between reads.
// A gap rule would be wrong here for a reason the reporter's log shows
// directly: the bursts are chunky, one of them pauses for two seconds three
// seconds in, and a "stop when a read takes longer than X" rule would stop dead
// in the middle of it and keep most of the burst.
//
// bps is bytes per second of real time and must be > 0; the caller skips the
// trim entirely when the bitrate is unknown, because without it there is no way
// to tell a burst from a healthy stream and the safe answer is today's
// behaviour. dropped is what was discarded, for the log.
func trimBurst(src io.Reader, bps int, keep int) (io.Reader, int64, time.Duration) {
	if bps <= 0 {
		return src, 0, 0
	}
	// The box needs a prebuffer even when the hole was tiny, so keep never
	// falls below a second of audio. A reconnect that handed the box nothing
	// would trade a rewind for a stutter.
	if keep < bps {
		keep = bps
	}
	if keep > trimBurstKeepCap {
		keep = trimBurstKeepCap
	}
	start := time.Now()
	ring := newByteRing(keep)
	buf := make([]byte, 16*1024)
	var drained int64
	winStart := start
	var winBytes int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			drained += int64(n)
			winBytes += int64(n)
			ring.write(buf[:n])
		}
		if err != nil {
			// EOF or a broken upstream: hand back what we have and let the
			// copy loop see the same error it would have seen.
			return io.MultiReader(bytes.NewReader(ring.bytes()), errReader{err}), drained - int64(ring.len()), time.Since(start)
		}
		if el := time.Since(winStart); el >= trimBurstWindow {
			live := float64(bps) * el.Seconds() * trimBurstAheadFactor
			if float64(winBytes) <= live {
				break // the upstream is at real time: this is the live edge
			}
			winStart = time.Now()
			winBytes = 0
		}
		if drained >= trimBurstMaxBytes || time.Since(start) >= trimBurstMaxWait {
			break
		}
	}
	return io.MultiReader(bytes.NewReader(ring.bytes()), src), drained - int64(ring.len()), time.Since(start)
}

// errReader replays one pending error after the underlying reader is drained,
// so a burst that ended in EOF still reaches the copy loop as EOF.
type errReader struct{ err error }

func (e errReader) Read(p []byte) (int, error) { return 0, e.err }

// byteRing keeps the most recent n bytes written to it and nothing else. Fixed
// allocation: one buffer of n bytes for the life of the trim, so a reconnect
// costs at most trimBurstKeepCap of transient memory on a speaker with 120 MB
// of RAM.
type byteRing struct {
	buf  []byte
	pos  int
	full bool
}

func newByteRing(n int) *byteRing {
	if n < 0 {
		n = 0
	}
	return &byteRing{buf: make([]byte, n)}
}

func (r *byteRing) write(p []byte) {
	if len(r.buf) == 0 {
		return
	}
	// Only the tail can survive: anything before it is overwritten anyway.
	if len(p) >= len(r.buf) {
		copy(r.buf, p[len(p)-len(r.buf):])
		r.pos = 0
		r.full = true
		return
	}
	n := copy(r.buf[r.pos:], p)
	if n < len(p) {
		copy(r.buf, p[n:])
		r.pos = len(p) - n
		r.full = true
	} else {
		r.pos += n
		if r.pos == len(r.buf) {
			r.pos = 0
			r.full = true
		}
	}
}

func (r *byteRing) len() int {
	if r.full {
		return len(r.buf)
	}
	return r.pos
}

// bytes returns the kept bytes in stream order, oldest first.
func (r *byteRing) bytes() []byte {
	if !r.full {
		out := make([]byte, r.pos)
		copy(out, r.buf[:r.pos])
		return out
	}
	out := make([]byte, len(r.buf))
	n := copy(out, r.buf[r.pos:])
	copy(out[n:], r.buf[:r.pos])
	return out
}
