// #823: "the stream jumps back about 20 to 30 seconds and then stays in that
// loop." An Icecast edge hands every new connection a prebuffer before it
// settles to real time, and STR forwarded that burst on RECONNECTS too, so the
// listener heard the last half minute again, over and over.

package streamproxy

import (
	"bytes"
	"io"
	"testing"
	"time"
)

// pacedReader delivers burst bytes instantly and then paces the rest at bps,
// which is what the reporter's station does: 388 KB in the first few seconds of
// every connection, then 96 kbps for as long as the connection lives.
type pacedReader struct {
	burst []byte
	rest  []byte
	bps   int
	sent  int
	start time.Time
}

func (p *pacedReader) Read(b []byte) (int, error) {
	if p.start.IsZero() {
		p.start = time.Now()
	}
	if p.sent < len(p.burst) {
		n := copy(b, p.burst[p.sent:])
		p.sent += n
		return n, nil
	}
	i := p.sent - len(p.burst)
	if i >= len(p.rest) {
		return 0, io.EOF
	}
	// Hand out at most what real time has produced since the burst ended.
	allowed := int(float64(p.bps) * time.Since(p.start).Seconds())
	if allowed <= p.sent-len(p.burst) {
		time.Sleep(5 * time.Millisecond)
		return 0, nil
	}
	n := copy(b, p.rest[i:])
	p.sent += n
	return n, nil
}

// pattern makes each byte position identifiable, so a test can say exactly
// which stretch of the stream came out the other end. A hash of the absolute
// position rather than a short cycle: with a repeating pattern a search for a
// kept stretch matches near the start of the stream too, and the test would
// pass or fail on an accident of periodicity.
func pattern(off, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(uint32(off+i) * 2654435761 >> 24)
	}
	return out
}

// The core of the bug: on a reconnect the box must be handed roughly the hole
// it missed, not the whole burst. 33 seconds of burst against a 5 second hole
// used to mean 28 seconds of audio the listener had already heard.
func TestTrimBurstKeepsOnlyTheHole(t *testing.T) {
	const bps = 12000 // 96 kbps, the reporter's station
	burst := pattern(0, 33*bps)
	rest := pattern(33*bps, 5*bps)
	src := &pacedReader{burst: burst, rest: rest, bps: bps}

	keep := 5 * bps // a five-second hole
	out, dropped, _ := trimBurst(src, bps, keep)

	if dropped < int64(20*bps) {
		t.Fatalf("only %d bytes dropped; a 33 s burst against a 5 s hole must discard most of it", dropped)
	}
	got := make([]byte, keep)
	if _, err := io.ReadFull(out, got); err != nil {
		t.Fatalf("reading the kept audio: %v", err)
	}
	// What survives must be the NEWEST audio, not the oldest: keeping the front
	// of the burst would be the bug with extra steps. The exact offset depends
	// on how much live audio arrived while the rate settled, so assert the
	// position rather than a fixed slice.
	all := append(append([]byte{}, burst...), rest...)
	at := bytes.Index(all, got)
	if at < 0 {
		t.Fatal("the kept audio is not a contiguous stretch of the stream")
	}
	if oldest := len(burst) - keep; at < oldest {
		t.Fatalf("the box was handed audio starting at byte %d; anything before %d is a stretch it had already heard", at, oldest)
	}
}

// A station that does not burst must be left completely alone: its bytes are
// already live, and eating any of them would be a forward skip.
func TestTrimBurstLeavesARealTimeStreamAlone(t *testing.T) {
	const bps = 12000
	// No burst at all, everything paced at the real rate.
	src := &pacedReader{burst: nil, rest: pattern(0, 4*bps), bps: bps}
	out, dropped, _ := trimBurst(src, bps, 5*bps)
	if dropped != 0 {
		t.Fatalf("dropped %d bytes of a stream that was already at real time", dropped)
	}
	all, _ := io.ReadAll(out)
	if !bytes.Equal(all, pattern(0, 4*bps)) {
		t.Fatalf("a real-time stream came out altered: got %d bytes", len(all))
	}
}

// Without a known bitrate there is no way to tell a burst from a healthy
// stream, so the safe answer is today's behaviour: touch nothing.
func TestTrimBurstWithoutABitrateIsANoOp(t *testing.T) {
	want := pattern(0, 40000)
	out, dropped, _ := trimBurst(bytes.NewReader(want), 0, 10000)
	if dropped != 0 {
		t.Fatalf("dropped %d bytes with no bitrate to reason from", dropped)
	}
	all, _ := io.ReadAll(out)
	if !bytes.Equal(all, want) {
		t.Fatal("the stream was altered with no bitrate to reason from")
	}
}

// A hole of zero still has to yield a working reader: the copy loop must not
// see a short read or a nil.
func TestTrimBurstWithNoHoleStillStreams(t *testing.T) {
	const bps = 12000
	src := &pacedReader{burst: pattern(0, 10*bps), rest: pattern(10*bps, bps), bps: bps}
	out, _, _ := trimBurst(src, bps, 0)
	buf := make([]byte, 1024)
	if _, err := io.ReadFull(out, buf); err != nil {
		t.Fatalf("no audio came out of a zero-hole trim: %v", err)
	}
}

// An upstream that dies during the drain must still surface its error to the
// copy loop, after whatever it did manage to hand over.
func TestTrimBurstPassesTheUpstreamErrorOn(t *testing.T) {
	const bps = 12000
	src := bytes.NewReader(pattern(0, 2000)) // ends in EOF immediately
	out, _, _ := trimBurst(src, bps, 4000)
	all, err := io.ReadAll(out)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(all) != 2000 {
		t.Fatalf("got %d bytes back from a 2000-byte upstream", len(all))
	}
}

func TestByteRingKeepsTheNewestBytes(t *testing.T) {
	r := newByteRing(10)
	r.write(pattern(0, 4))
	if got := r.bytes(); !bytes.Equal(got, pattern(0, 4)) {
		t.Fatalf("partial ring: got %v", got)
	}
	// Write past the wrap in several pieces: the ring must still read out in
	// stream order, oldest kept byte first.
	r.write(pattern(4, 5))
	r.write(pattern(9, 6))
	if got := r.bytes(); !bytes.Equal(got, pattern(5, 10)) {
		t.Fatalf("wrapped ring: got %v, want %v", got, pattern(5, 10))
	}
	// A single write larger than the ring keeps only its tail.
	r2 := newByteRing(4)
	r2.write(pattern(0, 100))
	if got := r2.bytes(); !bytes.Equal(got, pattern(96, 4)) {
		t.Fatalf("oversized write: got %v, want %v", got, pattern(96, 4))
	}
}

// The retry budget must be a failure STREAK. As a lifetime cap it ran out on a
// station that was still playing: in the #823 bundle the handler gave up after
// sixty reconnects and twenty-five minutes, several of those connections having
// each delivered more than a megabyte of clean audio, and the speaker recorded
// SOURCE_DISCONNECTED. The listener heard "and then it suddenly stopped
// playing completely".
func TestAProductiveConnectionResetsTheFailureStreak(t *testing.T) {
	s := New(nil, silentLogger())
	s.noteConnResult(2<<20, 90*time.Second)
	if !s.connWasProductive() {
		t.Fatal("a 90 second connection carrying 2 MB read as unproductive; a long stream would still run out of retries")
	}
	// A connection that opened and died at once is not productive, whatever it
	// managed to write.
	s.noteConnResult(2<<20, time.Second)
	if s.connWasProductive() {
		t.Fatal("a one-second connection counted as productive")
	}
	s.noteConnResult(1024, 90*time.Second)
	if s.connWasProductive() {
		t.Fatal("90 seconds carrying 1 KB counted as productive")
	}
	// The reset the loops do before every attempt, so an early failure that
	// never reaches the copy loop reads as delivering nothing.
	s.noteConnResult(0, 0)
	if s.connWasProductive() {
		t.Fatal("a zeroed result counted as productive")
	}
}
