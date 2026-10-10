// #823 round two: the reconnect trim estimated the resume point from the hole
// and the bitrate, which is only right when the old connection was exactly at
// the live edge when it died. The edge in the reporter's log stalled for up to
// 15 s inside connections, so it never was, and the listener heard the stream
// jump back or skip. These tests pin the content-based splice, its fallback,
// and the time budgets that keep the box from giving up during a reconnect.

package streamproxy

import (
	"bytes"
	"fmt"
	"io"
	"testing"
	"time"
)

// chunkReader hands out data in fixed-size pieces, so a needle can straddle
// two reads the way it does on a real socket.
type chunkReader struct {
	data  []byte
	chunk int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	n := min(min(c.chunk, len(p)), len(c.data))
	copy(p, c.data[:n])
	c.data = c.data[n:]
	return n, nil
}

// boxTail is what the copy loop would have recorded after handing the box
// stream bytes [0, upTo): the newest spliceTailSize of them.
func boxTail(upTo int) []byte {
	r := newByteRing(spliceTailSize)
	r.write(pattern(0, upTo))
	return r.bytes()
}

// The old connection stalled while the box was at byte boxAt; the new
// connection's burst starts 20 s earlier than that. Whatever the hole says, the
// box must continue on byte boxAt exactly: no repeat, no skip.
func TestTrimBurstResumesExactlyAfterWhatTheBoxGot(t *testing.T) {
	const bps = 12000
	for _, chunk := range []int{16 << 10, 3000, 4095, 1} {
		t.Run(fmt.Sprintf("chunk=%d", chunk), func(t *testing.T) {
			boxAt := 600 * bps
			burstFrom := boxAt - 20*bps
			src := &chunkReader{data: pattern(burstFrom, 34*bps), chunk: chunk}
			// A hole of 3 s would keep 3 s by time, a 17 s repeat here.
			out, res := trimBurst(src, bps, 3*bps, boxTail(boxAt), trimBurstMaxWait)
			if !res.spliced {
				t.Fatal("the box's last bytes are in the burst but the trim fell back to the time estimate")
			}
			got := make([]byte, 5*bps)
			if _, err := io.ReadFull(out, got); err != nil {
				t.Fatalf("reading after the splice: %v", err)
			}
			if !bytes.Equal(got, pattern(boxAt, len(got))) {
				t.Fatal("the box was not resumed on the byte right after the last one it received")
			}
			if want := int64(20 * bps); res.dropped != want {
				t.Fatalf("dropped %d bytes, want exactly the %d the box already had", res.dropped, want)
			}
		})
	}
}

// The other direction: the old connection was AHEAD of what the hole implies
// (the edge's backlog had already arrived), and the time estimate would skip.
// A burst that only reaches 2 s back from the box's position still splices.
func TestTrimBurstSpliceNearTheBurstStart(t *testing.T) {
	const bps = 12000
	boxAt := 300 * bps
	src := &chunkReader{data: pattern(boxAt-2*bps, 30*bps), chunk: 16 << 10}
	out, res := trimBurst(src, bps, 25*bps, boxTail(boxAt), trimBurstMaxWait)
	if !res.spliced {
		t.Fatal("expected a content splice")
	}
	got := make([]byte, bps)
	if _, err := io.ReadFull(out, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pattern(boxAt, bps)) {
		t.Fatal("resumed at the wrong byte")
	}
}

// When the burst does not contain what the box last received (another edge
// with different ad insertion, or a box further behind than the burst reaches)
// the trim must behave exactly as before: time-based, newest bytes kept.
func TestTrimBurstFallsBackToTimeWithoutOverlap(t *testing.T) {
	const bps = 12000
	burst := pattern(0, 33*bps)
	rest := pattern(33*bps, 5*bps)
	src := &pacedReader{burst: burst, rest: rest, bps: bps}
	unrelated := pattern(100_000_000, spliceTailSize)
	keep := 5 * bps
	out, res := trimBurst(src, bps, keep, unrelated, trimBurstMaxWait)
	if res.spliced {
		t.Fatal("spliced on content that is not in the burst")
	}
	if res.dropped < int64(20*bps) {
		t.Fatalf("only %d bytes dropped by the time fallback", res.dropped)
	}
	got := make([]byte, keep)
	if _, err := io.ReadFull(out, got); err != nil {
		t.Fatal(err)
	}
	all := append(append([]byte{}, burst...), rest...)
	if at := bytes.Index(all, got); at < len(burst)-keep {
		t.Fatalf("time fallback handed the box audio from byte %d", at)
	}
}

// The upstream interleaves ICY metadata at offsets that differ from the old
// connection's, so matching has to happen on the stripped audio. The caller
// strips before trimming; this pins that the two compose.
func TestTrimBurstSplicesThroughICYMetadata(t *testing.T) {
	const bps = 12000
	const metaint = 8192
	boxAt := 400 * bps
	audio := pattern(boxAt-15*bps, 30*bps)
	var wire bytes.Buffer
	for i := 0; i < len(audio); i += metaint {
		end := min(i+metaint, len(audio))
		wire.Write(audio[i:end])
		if end == len(audio) {
			break
		}
		if (i/metaint)%3 == 0 {
			meta := []byte("StreamTitle='Artist - Title';")
			blocks := (len(meta) + 15) / 16
			wire.WriteByte(byte(blocks))
			wire.Write(meta)
			wire.Write(make([]byte, blocks*16-len(meta)))
		} else {
			wire.WriteByte(0)
		}
	}
	src := newICYReader(&chunkReader{data: wire.Bytes(), chunk: 5000}, metaint, nil)
	out, res := trimBurst(src, bps, 3*bps, boxTail(boxAt), trimBurstMaxWait)
	if !res.spliced {
		t.Fatal("no content splice through stripped ICY metadata")
	}
	got := make([]byte, 4*bps)
	if _, err := io.ReadFull(out, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pattern(boxAt, len(got))) {
		t.Fatal("resumed at the wrong byte, or metadata leaked into the audio")
	}
}

// A needle hit whose preceding bytes do not match the tail is a coincidence,
// not the resume point, and must be passed over for the real one.
func TestTrimBurstIgnoresANeedleHitWithTheWrongContext(t *testing.T) {
	const bps = 12000
	boxAt := 200 * bps
	tail := boxTail(boxAt)
	needle := tail[len(tail)-spliceNeedleSize:]
	var burst []byte
	burst = append(burst, pattern(900_000_000, 40_000)...) // unrelated audio
	burst = append(burst, needle...)                       // same needle, wrong context
	burst = append(burst, pattern(800_000_000, 20_000)...)
	burst = append(burst, pattern(boxAt-10*bps, 20*bps)...) // the real overlap
	out, res := trimBurst(&chunkReader{data: burst, chunk: 7000}, bps, bps, tail, trimBurstMaxWait)
	if !res.spliced {
		t.Fatal("expected the real overlap to be found")
	}
	got := make([]byte, bps)
	if _, err := io.ReadFull(out, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pattern(boxAt, bps)) {
		t.Fatal("spliced on the decoy instead of the real overlap")
	}
}

// Encoded silence repeats the same frame, so a needle made of it matches in
// many places. Such a tail must not be used for a content splice.
func TestSpliceFinderRefusesAUniformTail(t *testing.T) {
	frame := pattern(42, 418) // one repeated "silent frame"
	tail := bytes.Repeat(frame, spliceTailSize/len(frame)+1)
	if f := newSpliceFinder(tail); f.needle != nil {
		t.Fatal("a periodic tail was accepted as a splice needle")
	}
	if f := newSpliceFinder(pattern(0, spliceNeedleSize-1)); f.needle != nil {
		t.Fatal("a tail shorter than the needle was accepted")
	}
	if f := newSpliceFinder(pattern(0, spliceTailSize)); f.needle == nil {
		t.Fatal("an ordinary tail was refused")
	}
}

// The search window must stay bounded however long the burst runs without a
// match: a 4 MB burst on a 128 MB speaker must not be held in memory.
func TestSpliceFinderWindowStaysBounded(t *testing.T) {
	f := newSpliceFinder(pattern(0, spliceTailSize))
	for off := 1 << 30; off < 1<<30+4<<20; off += 16 << 10 {
		if _, ok := f.feed(pattern(off, 16<<10)); ok {
			t.Fatal("matched unrelated data")
		}
		if len(f.win) > spliceTailSize+16<<10 {
			t.Fatalf("search window grew to %d bytes", len(f.win))
		}
	}
}

// Hole plus drain must stay inside the box's silence budget: twice in the #823
// log a 21.5 s hole plus an 8.5 s drain made the speaker give up.
func TestDrainBudgetKeepsHolePlusDrainUnderTheBoxLimit(t *testing.T) {
	cases := []struct{ hole, want time.Duration }{
		{0, trimBurstMaxWait},
		{5 * time.Second, trimBurstMaxWait},
		{17500 * time.Millisecond, 10500 * time.Millisecond},
		{21500 * time.Millisecond, 6500 * time.Millisecond},
		{27500 * time.Millisecond, drainBudgetMin},
		{40 * time.Second, drainBudgetMin},
	}
	for _, c := range cases {
		if got := drainBudget(c.hole); got != c.want {
			t.Errorf("drainBudget(%s) = %s, want %s", c.hole, got, c.want)
		}
	}
	for hole := 8 * time.Second; hole <= boxSilenceBudget-drainBudgetMin; hole += 500 * time.Millisecond {
		if total := hole + drainBudget(hole); total > boxSilenceBudget {
			t.Errorf("hole %s + drain = %s, over the %s budget", hole, total, boxSilenceBudget)
		}
	}
}

// The drain budget is honoured: an upstream that bursts forever is cut off at
// maxWait, not at the 20 s ceiling.
func TestTrimBurstHonoursTheDrainBudget(t *testing.T) {
	const bps = 12000
	src := &pacedReader{burst: pattern(0, 3<<20), rest: nil, bps: 1}
	start := time.Now()
	slow := &slowReader{r: src, delay: 20 * time.Millisecond}
	_, res := trimBurst(slow, bps, bps, nil, 600*time.Millisecond)
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("drain took %s with a 600 ms budget", el)
	}
	if res.spliced {
		t.Fatal("no tail, no splice")
	}
}

// slowReader delays every read, so a burst that is still faster than real time
// spans the drain budget.
type slowReader struct {
	r     io.Reader
	delay time.Duration
}

func (s *slowReader) Read(p []byte) (int, error) {
	time.Sleep(s.delay)
	return s.r.Read(p[:min(len(p), 4096)])
}

// The watchdog waits as long as the box's buffer allows, between the old 15 s
// and 22 s, so the first new byte still lands inside the box's ~30 s limit.
func TestStallLimitFollowsTheBoxBuffer(t *testing.T) {
	cases := []struct{ buffered, want time.Duration }{
		{0, upstreamStallBase},
		{10 * time.Second, upstreamStallBase},
		{26 * time.Second, 18 * time.Second},
		{boxBufferCap, upstreamStallMax},
		{time.Hour, upstreamStallMax},
	}
	for _, c := range cases {
		if got := stallLimit(upstreamStallBase, upstreamStallMax, c.buffered); got != c.want {
			t.Errorf("stallLimit(buffered %s) = %s, want %s", c.buffered, got, c.want)
		}
	}
	// A shrunken base (the backpressure test) with no buffer estimate stays at
	// the base, and a max below the base never lowers it.
	if got := stallLimit(300*time.Millisecond, upstreamStallMax, 0); got != 300*time.Millisecond {
		t.Errorf("shrunken base: got %s", got)
	}
	if got := stallLimit(15*time.Second, time.Second, time.Hour); got != 15*time.Second {
		t.Errorf("max below base: got %s", got)
	}
	// Watchdog plus the worst reconnect seen plus the minimum drain must fit
	// the box's silence limit.
	if total := upstreamStallMax + 6500*time.Millisecond + drainBudgetMin; total >= 30*time.Second {
		t.Errorf("worst case %s reaches the box's ~30 s limit", total)
	}
}

func TestBoxBufferFillsDrainsAndClamps(t *testing.T) {
	var b boxBuffer
	t0 := time.Unix(1_000_000, 0)
	if lvl := b.levelAt(t0); lvl != 0 {
		t.Fatalf("empty estimate reads %s", lvl)
	}
	const bps = 12000
	b.add(33*bps, bps, t0) // the #823 burst: 33 s in one go
	if lvl := b.levelAt(t0); lvl != boxBufferCap {
		t.Fatalf("after a 33 s burst: %s, want the %s cap", lvl, boxBufferCap)
	}
	if lvl := b.levelAt(t0.Add(12 * time.Second)); lvl != 18*time.Second {
		t.Fatalf("12 s later: %s, want 18s", lvl)
	}
	b.add(2*bps, bps, t0.Add(12*time.Second))
	if lvl := b.levelAt(t0.Add(12 * time.Second)); lvl != 20*time.Second {
		t.Fatalf("after 2 s more audio: %s, want 20s", lvl)
	}
	if lvl := b.levelAt(t0.Add(time.Minute)); lvl != 0 {
		t.Fatalf("a starved box reads %s, want 0", lvl)
	}
	b.add(bps, 0, t0) // unknown bitrate: no change, no panic
	b.reset()
	if lvl := b.levelAt(t0); lvl != 0 {
		t.Fatalf("after reset: %s", lvl)
	}
}

func TestSpliceTailTracksOneStation(t *testing.T) {
	var tl spliceTail
	tl.record("a", []byte("ignored before any reset"))
	if got := tl.snapshot("a"); got != nil {
		t.Fatal("a tail existed before the stream started")
	}
	tl.reset("a")
	tl.record("a", pattern(0, 100_000))
	if got := tl.snapshot("a"); !bytes.Equal(got, pattern(100_000-spliceTailSize, spliceTailSize)) {
		t.Fatal("the tail is not the newest bytes the box received")
	}
	if tl.snapshot("b") != nil {
		t.Fatal("another station's reconnect was handed this station's tail")
	}
	tl.record("b", pattern(5, 10))
	tl.reset("")
	tl.record("", pattern(0, 10))
	if tl.snapshot("") != nil || tl.snapshot("a") != nil {
		t.Fatal("a disabled tail still recorded")
	}
}
