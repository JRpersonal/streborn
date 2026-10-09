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
// The trim keeps only the part of the burst the box has not heard yet. It
// finds that point by content when it can (the bytes the box last received
// appear in the burst) and estimates it from the hole when it cannot.

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
	// be discarding audio nobody ever hears. trimBurstMaxWait is the ceiling;
	// the caller passes the actual wait from drainBudget, which is usually
	// shorter.
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

	// boxSilenceBudget is how long the box may go without a single byte
	// before STR has to have handed it audio again. The #823 SoundTouch 30
	// gave up (play_underrun, then ERROR_BAD_URL) after a 21.5 s hole plus an
	// 8.5 s drain, so its limit sits at about 30 s; 28 s keeps a margin.
	boxSilenceBudget = 28 * time.Second
	// drainBudgetMin is the shortest drain the budget ever allows. Without at
	// least a few rate windows the trim cannot tell a burst from the live edge.
	drainBudgetMin = time.Second

	// spliceTailSize is how much of what the box was last handed is kept to
	// find the resume point in a reconnect's burst. 64 KB is about five
	// seconds at 96 kbps: the needle plus the context that confirms it, at a
	// fixed cost of 64 KB per running stream.
	spliceTailSize = 64 << 10
	// spliceNeedleSize is the stretch actually searched for: the last 4 KB the
	// box received, a third of a second of 96 kbps audio. Compressed audio that
	// long does not recur by accident.
	spliceNeedleSize = 4 << 10
	// spliceUniformProbe is how much of the needle's opening is checked for a
	// repeat inside the needle itself (see newSpliceFinder).
	spliceUniformProbe = 256
)

// drainBudget is how long trimBurst may hold the box's bytes back while it
// waits for the burst to settle, given that the box has already been silent
// for hole. The fixed 20 s wait let a 21.5 s hole grow into half a minute of
// silence twice in the #823 log, and the speaker gave up both times. Hole plus
// drain now stays inside boxSilenceBudget, with at least drainBudgetMin so the
// rate can be judged at all.
func drainBudget(hole time.Duration) time.Duration {
	d := boxSilenceBudget - hole
	if d < drainBudgetMin {
		d = drainBudgetMin
	}
	if d > trimBurstMaxWait {
		d = trimBurstMaxWait
	}
	return d
}

// burstTrim describes what trimBurst did, for the reconnect log line.
type burstTrim struct {
	dropped int64         // burst bytes discarded
	kept    int           // bytes handed to the box ahead of the live stream
	drain   time.Duration // how long the box's bytes were held back
	// spliced is true when the resume point was found by content: the box
	// continues on the exact byte after the last one it received, with no
	// repeat and no skip. False means the time-based estimate was used.
	spliced bool
}

// trimBurst reads ahead of the box while the upstream is delivering FASTER than
// real time, and returns a reader that yields the part of the burst the box
// still needs and then the live stream, so the caller's copy loop does not
// change.
//
// The resume point is found two ways. First by CONTENT: tail is the newest
// bytes the box actually received before the reconnect. When the burst
// contains them, the box resumes on the very next byte, whatever the old
// connection's lag or lead was when it stalled. Splicing by time alone
// (keep = hole * bitrate) assumed the old connection was exactly at the live
// edge when it died, and in the #823 log it never was: the edge had stalled
// for up to 15 s and then delivered its backlog in a rush, so the estimate
// turned reconnects into a jump back or a skip forward.
//
// When the tail is not found (no tail yet, a different edge with different ad
// insertion, a tail too uniform to identify, or a box further behind than the
// burst reaches), the time-based estimate applies: keep only the newest keep
// bytes once the rate has settled to real time. Nothing live is lost by
// construction there: the ring keeps the NEWEST bytes, so whatever arrives
// while the rate is being judged is part of what the box gets.
//
// The stop condition for that fallback is a delivery RATE over a window, not a
// gap between reads. A gap rule would be wrong here for a reason the
// reporter's log shows directly: the bursts are chunky, one of them pauses for
// two seconds three seconds in, and a "stop when a read takes longer than X"
// rule would stop dead in the middle of it and keep most of the burst.
//
// bps is bytes per second of real time and must be > 0; the caller skips the
// trim entirely when the bitrate is unknown, because without it there is no way
// to tell a burst from a healthy stream and the safe answer is today's
// behaviour. maxWait bounds how long the box's bytes are held back (see
// drainBudget). src must be clean audio: the caller strips ICY metadata first,
// so the bytes compared here are the bytes the box would get.
func trimBurst(src io.Reader, bps, keep int, tail []byte, maxWait time.Duration) (io.Reader, burstTrim) {
	if bps <= 0 {
		return src, burstTrim{}
	}
	if maxWait <= 0 || maxWait > trimBurstMaxWait {
		maxWait = trimBurstMaxWait
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
	finder := newSpliceFinder(tail)
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
			if rest, ok := finder.feed(buf[:n]); ok {
				// Everything after the match is audio the box has not heard.
				// Stop here: the rest of the burst is exactly what a box that
				// has been silent for seconds needs, as fast as it comes.
				next := src
				if err != nil {
					next = errReader{err}
				}
				return io.MultiReader(bytes.NewReader(rest), next), burstTrim{
					dropped: drained - int64(len(rest)),
					kept:    len(rest),
					drain:   time.Since(start),
					spliced: true,
				}
			}
		}
		if err != nil {
			// EOF or a broken upstream: hand back what we have and let the
			// copy loop see the same error it would have seen.
			kept := ring.bytes()
			return io.MultiReader(bytes.NewReader(kept), errReader{err}),
				burstTrim{dropped: drained - int64(len(kept)), kept: len(kept), drain: time.Since(start)}
		}
		if el := time.Since(winStart); el >= trimBurstWindow {
			live := float64(bps) * el.Seconds() * trimBurstAheadFactor
			if float64(winBytes) <= live {
				break // the upstream is at real time: this is the live edge
			}
			winStart = time.Now()
			winBytes = 0
		}
		if drained >= trimBurstMaxBytes || time.Since(start) >= maxWait {
			break
		}
	}
	kept := ring.bytes()
	return io.MultiReader(bytes.NewReader(kept), src),
		burstTrim{dropped: drained - int64(len(kept)), kept: len(kept), drain: time.Since(start)}
}

// spliceFinder searches a byte stream for the point right after tail's last
// byte, in bounded memory: it carries at most about len(tail) bytes between
// reads and scans each byte for the needle once.
type spliceFinder struct {
	tail   []byte // what the box last received, oldest first
	needle []byte // the last spliceNeedleSize bytes of tail
	win    []byte // recent stream bytes still in reach of a match
	// scanned is how far into win the needle's start has been searched, so a
	// long burst costs one pass, not one pass per read.
	scanned int
}

// newSpliceFinder returns a finder for tail, or a disabled one when tail is
// too short or too uniform to locate reliably. Uniform is the real risk:
// encoded digital silence is the same frame over and over, and a needle made
// of it would match the first silent frame of the burst, which is a repeat or
// a skip of arbitrary length. A needle whose own opening recurs inside it is
// treated as unidentifiable, and the time-based trim is used instead.
func newSpliceFinder(tail []byte) *spliceFinder {
	f := &spliceFinder{}
	if len(tail) < spliceNeedleSize {
		return f
	}
	needle := tail[len(tail)-spliceNeedleSize:]
	if bytes.Contains(needle[1:], needle[:spliceUniformProbe]) {
		return f
	}
	f.tail = tail
	f.needle = needle
	return f
}

// feed appends p to the search window and, once the needle (confirmed by as
// much preceding context as the window holds) is in it, returns a copy of the
// bytes that follow the match.
func (f *spliceFinder) feed(p []byte) ([]byte, bool) {
	if f.needle == nil {
		return nil, false
	}
	f.win = append(f.win, p...)
	nl := len(f.needle)
	for f.scanned+nl <= len(f.win) {
		i := bytes.Index(f.win[f.scanned:], f.needle)
		if i < 0 {
			// A match may still start in the last nl-1 bytes and end in the
			// next read, so those stay unscanned.
			f.scanned = len(f.win) - nl + 1
			break
		}
		at := f.scanned + i
		if f.contextMatches(at) {
			rest := make([]byte, len(f.win)-at-nl)
			copy(rest, f.win[at+nl:])
			return rest, true
		}
		f.scanned = at + 1
	}
	// Bound the window: keep the unscanned bytes plus enough before them for
	// the context check of a match that starts there.
	keepFrom := f.scanned - (len(f.tail) - nl)
	if keepFrom > 0 {
		f.win = append(f.win[:0], f.win[keepFrom:]...)
		f.scanned -= keepFrom
	}
	return nil, false
}

// contextMatches confirms a needle hit at win[at:] by comparing the bytes
// before it with the bytes before the needle in tail, as far back as both
// reach.
func (f *spliceFinder) contextMatches(at int) bool {
	before := len(f.tail) - len(f.needle)
	n := min(at, before)
	return bytes.Equal(f.win[at-n:at], f.tail[before-n:before])
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
