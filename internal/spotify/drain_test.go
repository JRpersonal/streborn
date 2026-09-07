// Tests for the chain seam through the REAL drain step (drain.go): synthetic
// tracks and a real libvorbis file go in page by page, the fake sink records
// what the box would receive, and the output is parsed with the same page
// reader the drain uses on the box. The harness lives in oggchain_test.go.

package spotify

import (
	"bytes"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func sumLen(pages [][]byte) int64 {
	var n int64
	for _, p := range pages {
		n += int64(len(p))
	}
	return n
}

func rawOf(pages []parsedPage) []byte {
	var out []byte
	for _, p := range pages {
		out = append(out, p.raw...)
	}
	return out
}

// headerPacketCount is how many packets the header pages carry (3 for any
// Vorbis stream: identification, comment, setup).
func headerPacketCount(t *testing.T, headerPages [][]byte) int {
	t.Helper()
	s := parseChain(t, concatPages(headerPages))
	if len(s) != 1 {
		t.Fatalf("header pages parse into %d streams", len(s))
	}
	return len(s[0].packets)
}

// audioGranules lists the granules of the audio pages of a chain in order,
// skipping the re-emitted header pages of every link after the first.
func audioGranules(streams []logicalStream, h int) []int64 {
	var out []int64
	for i, s := range streams {
		pages := s.pages
		if i > 0 {
			pages = pages[h:]
		}
		for _, p := range pages {
			if p.gran > 0 || p.gran == -1 {
				out = append(out, p.gran)
			}
		}
	}
	return out
}

func inputGranules(pages [][]byte, h int) []int64 {
	var out []int64
	for _, p := range pages[h:] {
		g := oggGranule(p)
		if g > 0 || g == -1 {
			out = append(out, g)
		}
	}
	return out
}

func equalGranules(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A seam on the wire is exactly the shape of a natural track end followed
// by a late join: the old link ends on an EOS page that differs from the
// input only in its flag byte and checksum, the new link opens with the
// track's own header pages under a fresh serial (seq 0..h-1, granule 0),
// and every later page carries that serial with a contiguous sequence,
// its original granule and body untouched. The EOS page closes an HTTP
// chunk; the late-joiner header set is the emitted link.
func TestDrainSeamReproducesTrackEndShape(t *testing.T) {
	const s1 = uint32(0x11111111)
	track := synthVorbisTrack(s1, 40, false)
	h := headerPageCount(track)
	period := sumLen(track[:h+3]) // crosses on the third audio page
	chain := newTestChain(t, period, nil)
	m, d, fs := newDrainHarness(t, chain, 1<<30)
	feed(d, track[:h+3])
	if !chain.linkPending {
		t.Fatal("the third audio page did not become P")
	}
	holdSeams(chain) // exactly one seam in this track
	feed(d, track[h+3:])
	if !bytes.Equal(d.hdr, concatPages(track[:h])) {
		t.Fatal("the drain's own header set must keep the original serial")
	}
	next := synthVorbisTrack(0x22222222, 1, false)
	feed(d, next[:1]) // the next track's BOS flushes the tail

	streams := parseChain(t, fs.buf.Bytes())
	if len(streams) < 2 {
		t.Fatalf("%d logical streams on the wire, want 2", len(streams))
	}
	a, b := streams[0], streams[1]
	if a.serial != s1 {
		t.Fatalf("first stream serial %08x, want the track's own %08x", a.serial, s1)
	}
	cutIdx := len(a.pages) - 1
	if cutIdx < h+2 {
		t.Fatalf("seam on page %d, want the third audio page (%d) or a later legal one", cutIdx, h+2)
	}
	for i := 0; i < cutIdx; i++ {
		if !bytes.Equal(a.pages[i].raw, track[i]) {
			t.Fatalf("page %d before the seam is not byte-identical to the input", i)
		}
	}
	wantP := append([]byte(nil), track[cutIdx]...)
	wantP[5] |= 0x04
	oggReseal(wantP)
	if !bytes.Equal(a.pages[cutIdx].raw, wantP) {
		t.Fatalf("P differs from the input beyond the EOS flag and the checksum")
	}
	if oggLastLacing(wantP) == 255 {
		t.Fatal("P must complete its last packet")
	}

	if b.serial == s1 || b.serial == 0 {
		t.Fatalf("link 2 serial %08x must be fresh", b.serial)
	}
	if len(b.pages) < h+1 {
		t.Fatalf("link 2 has %d pages, want headers plus audio", len(b.pages))
	}
	for i := 0; i < h; i++ {
		p, o := b.pages[i], parsePage(track[i])
		if p.seq != uint32(i) || p.gran != 0 || p.flags != o.flags ||
			!bytes.Equal(p.lacing, o.lacing) || !bytes.Equal(p.body, o.body) {
			t.Fatalf("link 2 header page %d: seq %d gran %d flags %#x, want a verbatim copy of the track's header page", i, p.seq, p.gran, p.flags)
		}
	}
	if len(b.pages[0].raw) != 58 || b.pages[0].flags&0x02 == 0 {
		t.Fatalf("link 2 does not open with the 58-byte BOS identification page (len %d flags %#x)", len(b.pages[0].raw), b.pages[0].flags)
	}
	assertContiguousSeq(t, b, 0)
	if got, want := len(b.pages)-h, len(track)-cutIdx-1; got != want {
		t.Fatalf("link 2 carries %d audio pages, want %d (everything after P)", got, want)
	}
	for i := h; i < len(b.pages); i++ {
		p, o := b.pages[i], parsePage(track[cutIdx+1+i-h])
		if p.gran != o.gran || p.flags != o.flags || !bytes.Equal(p.lacing, o.lacing) || !bytes.Equal(p.body, o.body) {
			t.Fatalf("link 2 audio page %d: granule/flags/body differ from the input page %d", i, cutIdx+1+i-h)
		}
	}
	if b.pages[h].flags&0x01 != 0 {
		t.Fatal("the first audio page of link 2 continues a packet; the cut was mid-packet")
	}

	if len(fs.chunks) < 2 {
		t.Fatalf("%d chunks on the wire, want the seam to close one and open another", len(fs.chunks))
	}
	if !bytes.HasSuffix(fs.chunks[0], wantP) {
		t.Fatal("the chunk before the new link does not end with the EOS page")
	}
	if !bytes.HasPrefix(fs.chunks[1], b.pages[0].raw) {
		t.Fatal("the new link's headers do not open the next chunk")
	}

	m.mu.Lock()
	hp := append([]byte(nil), m.headerPages...)
	seams := m.sinkSeams
	m.mu.Unlock()
	if !bytes.Equal(hp, rawOf(b.pages[:h])) {
		t.Fatal("the late-joiner header set is not the emitted link header set")
	}
	if seams != 1 {
		t.Fatalf("sinkSeams = %d, want 1", seams)
	}
}

// The packets on the wire are the input packets, in order, byte for byte:
// a naive cut duplicates nothing and drops nothing at the packet level. On
// both framings: pages ending at packet boundaries (a current libogg) and
// packets spanning pages (an old one, where a legal cut point is rare and
// the seam has to slide).
func TestDrainSeamPacketContinuity(t *testing.T) {
	layouts := []struct {
		name  string
		track [][]byte
		span  bool
	}{
		{"boundary", synthVorbisTrack(0x31, 300, true), false},
		{"spanning", synthVorbisTrackSpanning(0x32, 300, true), true},
	}
	for _, lay := range layouts {
		for _, period := range []int64{8 << 10, 16 << 10, 64 << 10} {
			track := lay.track
			h := headerPageCount(track)
			hp := headerPacketCount(t, track[:h])
			orig := parseChain(t, concatPages(track))
			chain := newTestChain(t, period, nil)
			m, d, fs := newDrainHarness(t, chain, 1)
			feed(d, track)
			streams := parseChain(t, fs.buf.Bytes())
			var got [][]byte
			for i, s := range streams {
				pk := s.packets
				if i > 0 {
					pk = pk[hp:] // the re-emitted header packets
				}
				got = append(got, pk...)
			}
			if len(got) != len(orig[0].packets) {
				t.Fatalf("%s, period %d KiB: %d packets on the wire, want %d", lay.name, period>>10, len(got), len(orig[0].packets))
			}
			for i := range got {
				if !bytes.Equal(got[i], orig[0].packets[i]) {
					t.Fatalf("%s, period %d KiB: packet %d differs", lay.name, period>>10, i)
				}
			}
			seams := int64(len(streams) - 1)
			total := sumLen(track)
			if seams < 1 || seams > total/period {
				t.Fatalf("%s, period %d KiB over %d KiB: %d seams", lay.name, period>>10, total>>10, seams)
			}
			// A link is the fewest whole pages that reach the period, so it
			// runs over by at most one page (budget plus one packet).
			if !lay.span && seams < total/(period+4096+1400)-1 {
				t.Fatalf("%s: every page is a legal cut point, yet only %d seams for %d periods", lay.name, seams, total/period)
			}
			if m.sinkSeams != int(seams) {
				t.Fatalf("%s: sinkSeams = %d, want %d", lay.name, m.sinkSeams, seams)
			}
			// On the spanning layout every link still opens on a fresh packet.
			for i, s := range streams[1:] {
				if s.pages[h].flags&0x01 != 0 {
					t.Fatalf("%s: link %d opens on a continued page", lay.name, i+2)
				}
			}
		}
	}
}

// The period crossing lands on pages that cannot be P: one whose last packet
// continues on the next page, one completing no packet at all, one already
// carrying EOS. The seam slides to the next legal page, or does not happen.
func TestDrainSeamNeverCutsMidPacket(t *testing.T) {
	const s = uint32(0x41)
	body := func(n int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(n + i)
		}
		return b
	}
	hdr := synthVorbisTrack(s, 0, false)
	a1 := buildPage(s, 3, 0, 1000, []byte{200}, body(200))
	a2 := buildPage(s, 4, 0, 2000, []byte{100, 255, 255}, body(610)) // completes one packet, the last spans
	a3 := buildPage(s, 5, 0x01, 3000, []byte{255, 40}, body(295))    // completes the spanning packet
	a4 := buildPage(s, 6, 0, -1, []byte{255, 255}, body(510))        // completes nothing
	a5 := buildPage(s, 7, 0x01, 4000, []byte{255, 10}, body(265))
	a6 := buildPage(s, 8, 0, 5000, []byte{50}, body(50))
	a7 := buildPage(s, 9, 0x04, 6000, []byte{60}, body(60)) // the track's real end
	pages := append(append([][]byte{}, hdr...), a1, a2, a3, a4, a5, a6, a7)
	h := len(hdr)

	run := func(t *testing.T, period int64) []logicalStream {
		t.Helper()
		chain := newTestChain(t, period, nil)
		_, d, fs := newDrainHarness(t, chain, 1)
		feed(d, pages)
		return parseChain(t, fs.buf.Bytes())
	}
	lastOf := func(s logicalStream) parsedPage { return s.pages[len(s.pages)-1] }

	// Crossing on a2 (final lacing 255): the seam slides to a3.
	streams := run(t, sumLen(pages[:h+2]))
	if len(streams) != 2 {
		t.Fatalf("case 255-run: %d streams, want 2", len(streams))
	}
	if p := lastOf(streams[0]); p.flags&0x04 == 0 || p.seq != 5 || p.gran != 3000 {
		t.Fatalf("case 255-run: P is seq %d gran %d flags %#x, want a3 with EOS", p.seq, p.gran, p.flags)
	}
	for i, p := range streams[1].pages[:h] {
		if p.flags&0x01 != 0 || p.seq != uint32(i) {
			t.Fatalf("case 255-run: link header page %d flags %#x seq %d", i, p.flags, p.seq)
		}
	}
	if q := streams[1].pages[h]; q.flags&0x01 != 0 || q.gran != -1 {
		t.Fatalf("case 255-run: Q is gran %d flags %#x, want a4 with the continuation flag clear", q.gran, q.flags)
	}
	assertContiguousSeq(t, streams[1], 0)

	// Crossing on a4 (granule -1): the seam slides to a5.
	streams = run(t, sumLen(pages[:h+4]))
	if len(streams) != 2 {
		t.Fatalf("case granule -1: %d streams, want 2", len(streams))
	}
	if p := lastOf(streams[0]); p.flags&0x04 == 0 || p.gran != 4000 {
		t.Fatalf("case granule -1: P is gran %d flags %#x, want a5 with EOS", p.gran, p.flags)
	}
	if q := streams[1].pages[h]; q.gran != 5000 || q.flags != 0 {
		t.Fatalf("case granule -1: Q is gran %d flags %#x, want a6", q.gran, q.flags)
	}

	// Crossing on a7 (already EOS, the last page): no seam at all.
	streams = run(t, sumLen(pages))
	if len(streams) != 1 {
		t.Fatalf("case already-EOS: %d streams, want 1", len(streams))
	}
	if !bytes.Equal(rawOf(streams[0].pages), concatPages(pages)) {
		t.Fatal("case already-EOS: output is not byte-identical to the input")
	}
}

// The cut lands on the last page of a track and the very next page is the
// next track's BOS: P goes out with EOS, NO header-only link is emitted, the
// new track passes through unstamped under its own serial.
func TestDrainSeamDeferredToRealBOS(t *testing.T) {
	track1 := synthVorbisTrack(0x51, 6, false) // no EOS: the last page is a legal P
	track2 := synthVorbisTrack(0x52, 2, false)
	period := sumLen(track1) // crosses exactly on the last page of track 1
	chain := newTestChain(t, period, nil)
	m, d, fs := newDrainHarness(t, chain, 1)
	feed(d, track1)
	if !chain.linkPending {
		t.Fatal("after P the link must be pending")
	}
	feed(d, track2)

	streams := parseChain(t, fs.buf.Bytes())
	if len(streams) != 2 {
		t.Fatalf("%d streams, want 2 (no header-only link may reach the box)", len(streams))
	}
	a, b := streams[0], streams[1]
	if last := a.pages[len(a.pages)-1]; last.flags&0x04 == 0 || a.serial != 0x51 {
		t.Fatalf("track 1 does not end on an EOS page under its own serial")
	}
	if b.serial != 0x52 {
		t.Fatalf("track 2 serial %08x, want its own 0x52", b.serial)
	}
	for i, p := range b.pages {
		if !bytes.Equal(p.raw, track2[i]) {
			t.Fatalf("track 2 page %d is not byte-identical: it must pass through unstamped", i)
		}
	}
	if chain.linkPending || chain.open {
		t.Fatal("the real BOS must drop the pending link and stop stamping")
	}
	if chain.since != sumLen(track2) {
		t.Fatalf("link byte count %d after the BOS, want %d (restarted with track 2)", chain.since, sumLen(track2))
	}
	m.mu.Lock()
	hp := append([]byte(nil), m.headerPages...)
	seams := m.sinkSeams
	m.mu.Unlock()
	h := headerPageCount(track2)
	if !bytes.Equal(hp, concatPages(track2[:h])) {
		t.Fatal("the late-joiner header set must be track 2's own after its first audio page")
	}
	if seams != 0 {
		t.Fatalf("sinkSeams = %d, want 0 (the link never opened)", seams)
	}
}

// Two seams inside one track: the second P carries the first link's serial,
// the third link gets a third serial, granules stay untouched throughout,
// and the track's real EOS page ends the last link.
func TestDrainSeamTwoSeamsOneTrack(t *testing.T) {
	track := synthVorbisTrack(0x61, 40, true)
	h := headerPageCount(track)
	chain := newTestChain(t, sumLen(track)/3, nil)
	m, d, fs := newDrainHarness(t, chain, 1)
	feed(d, track)

	streams := parseChain(t, fs.buf.Bytes())
	if len(streams) < 3 {
		t.Fatalf("%d streams, want at least 3", len(streams))
	}
	s0, s1, s2 := streams[0], streams[1], streams[2]
	if s0.serial != 0x61 || s1.serial == s0.serial || s2.serial == s1.serial || s2.serial == s0.serial {
		t.Fatalf("serials %08x %08x %08x are not pairwise distinct with the track's own first", s0.serial, s1.serial, s2.serial)
	}
	if p := s1.pages[len(s1.pages)-1]; p.flags&0x04 == 0 || p.serial != s1.serial {
		t.Fatalf("the second P must carry EOS under the first link's serial")
	}
	for i, s := range streams[1:] {
		for j := 0; j < h; j++ {
			if s.pages[j].seq != uint32(j) || s.pages[j].gran != 0 {
				t.Fatalf("link %d header page %d: seq %d gran %d", i+2, j, s.pages[j].seq, s.pages[j].gran)
			}
		}
		assertContiguousSeq(t, s, 0)
	}
	if !equalGranules(audioGranules(streams, h), inputGranules(track, h)) {
		t.Fatal("granule positions were rewritten somewhere")
	}
	last := streams[len(streams)-1]
	if p := last.pages[len(last.pages)-1]; p.flags&0x04 == 0 {
		t.Fatal("the track's real EOS page lost its flag")
	}
	if m.sinkSeams != len(streams)-1 {
		t.Fatalf("sinkSeams = %d, want %d", m.sinkSeams, len(streams)-1)
	}
}

// Pages dropped by an armed skip cut never reach the seam code: not counted,
// not stamped, no seam; the following real BOS resets the chain.
func TestDrainSeamSkipCutDrops(t *testing.T) {
	track := synthVorbisTrack(0x71, 6, false)
	chain := newTestChain(t, sumLen(track)+1, nil) // track 1 alone stays under the period
	m, d, fs := newDrainHarness(t, chain, 1)
	feed(d, track)
	sinceBefore := chain.since
	m.NoteSkip()
	extra := synthVorbisTrack(0x71, 6, false)[headerPageCount(track):] // stale audio after the skip
	feed(d, extra)
	if chain.since != sinceBefore {
		t.Fatalf("dropped pages counted toward the link: %d -> %d", sinceBefore, chain.since)
	}
	if chain.linkPending || m.sinkSeams != 0 {
		t.Fatal("dropped pages produced a seam")
	}
	if !bytes.Equal(fs.buf.Bytes(), concatPages(track)) {
		t.Fatal("dropped pages reached the wire")
	}
	bos := synthVorbisTrack(0x72, 1, false)[0]
	feed(d, [][]byte{bos})
	if m.skipCutArmed() {
		t.Fatal("the boundary must disarm the skip cut")
	}
	if chain.since != int64(len(bos)) || chain.open {
		t.Fatalf("the real BOS must reset the link: since %d open %v", chain.since, chain.open)
	}
}

// A box that (re)attaches in the middle of a link gets the link's header
// set first: together with the live pages that is one decodable logical
// stream, exactly the late-join shape the box handles today.
func TestDrainSeamLateJoinerHeaders(t *testing.T) {
	track := synthVorbisTrack(0x81, 60, true)
	h := headerPageCount(track)
	if len(track) < h+12 {
		t.Fatalf("track has only %d pages", len(track))
	}
	chain := newTestChain(t, sumLen(track[:h+4]), nil)
	m, d, _ := newDrainHarness(t, chain, 1)
	feed(d, track[:h+5]) // past the seam (P is the fourth audio page), the link is open
	if m.sinkSeams != 1 {
		t.Fatalf("sinkSeams = %d, want 1 before the join", m.sinkSeams)
	}
	holdSeams(chain)
	feed(d, track[h+5:h+8]) // a few pages into the link
	fs2 := &fakeSink{}
	m.mu.Lock()
	hdr := append([]byte(nil), m.headerPages...)
	m.sink = fs2
	m.mu.Unlock()
	feed(d, track[h+8:])

	streams := parseChain(t, append(hdr, fs2.buf.Bytes()...))
	if len(streams) != 1 {
		t.Fatalf("the joiner sees %d logical streams, want 1", len(streams))
	}
	s := streams[0]
	if s.serial == 0x81 || s.serial == 0 {
		t.Fatalf("joiner stream serial %08x, want the live link's", s.serial)
	}
	for i := 0; i < h; i++ {
		if s.pages[i].seq != uint32(i) || s.pages[i].gran != 0 {
			t.Fatalf("joiner header page %d: seq %d gran %d", i, s.pages[i].seq, s.pages[i].gran)
		}
	}
	live := s.pages[h:]
	if len(live) == 0 || live[0].seq < uint32(h) {
		t.Fatalf("joiner live pages: %d, first seq %d", len(live), live[0].seq)
	}
	for i := 1; i < len(live); i++ {
		if live[i].seq != live[i-1].seq+1 {
			t.Fatalf("live page %d: seq %d after %d", i, live[i].seq, live[i-1].seq)
		}
	}
	if p := live[len(live)-1]; p.flags&0x04 == 0 {
		t.Fatal("the track's real EOS page lost its flag")
	}
	hp := headerPacketCount(t, track[:h])
	if len(s.packets) <= hp {
		t.Fatal("joiner decodes no audio packets")
	}
}

// The kill switch: with the knob at 0 the drain is byte-identical
// passthrough and the header cache behaves exactly as before.
func TestDrainChainOffIsByteIdentical(t *testing.T) {
	chain := newTestChain(t, 16<<10, nil)
	if err := os.WriteFile(chain.overridePath, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	chain.loadOverride()
	if chain.period != 0 || !chain.overridden {
		t.Fatalf("knob 0 not honoured: period %d", chain.period)
	}
	track1 := synthVorbisTrack(0x91, 80, true)
	track2 := synthVorbisTrack(0x92, 20, true)
	m, d, fs := newDrainHarness(t, chain, 1)
	feed(d, track1)
	feed(d, track2)
	want := append(concatPages(track1), concatPages(track2)...)
	if !bytes.Equal(fs.buf.Bytes(), want) {
		t.Fatal("output differs from the input with the chain off")
	}
	m.mu.Lock()
	hp := append([]byte(nil), m.headerPages...)
	seams, snap := m.sinkSeams, m.chainSnap
	m.mu.Unlock()
	if !bytes.Equal(hp, concatPages(track2[:headerPageCount(track2)])) {
		t.Fatal("header cache is not track 2's own set")
	}
	if seams != 0 || snap.SeamsTotal != 0 || snap.Enabled {
		t.Fatalf("chain off: seams %d snapshot %+v", seams, snap)
	}
}

// The rewritten stream reads back through the box-side page reader with
// every page intact: input pages plus h header pages per seam, and no
// checksum resync at all.
func TestDrainOutputStaysReadable(t *testing.T) {
	for _, track := range [][][]byte{synthVorbisTrack(0xa1, 200, true), synthVorbisTrackSpanning(0xa2, 200, true)} {
		h := headerPageCount(track)
		chain := newTestChain(t, 20<<10, nil)
		m, d, fs := newDrainHarness(t, chain, 1)
		feed(d, track)
		rec := &recHandler{}
		r := newOggPageReader(bytes.NewReader(fs.buf.Bytes()), slog.New(rec))
		n := 0
		for {
			if _, err := r.ReadPage(); err != nil {
				break
			}
			n++
		}
		if m.sinkSeams < 2 {
			t.Fatalf("sinkSeams = %d, want a few", m.sinkSeams)
		}
		if want := len(track) + h*m.sinkSeams; n != want {
			t.Fatalf("%d pages read back, want %d (input plus %d headers per seam)", n, want, h)
		}
		if len(rec.recs) != 0 {
			t.Fatalf("the reader resynced %d times: a page failed its checksum", len(rec.recs))
		}
	}
}

// ---- real libvorbis framing ---------------------------------------------

func loadFixturePages(t *testing.T) [][]byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "pink-5s.ogg"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	r := newOggPageReader(bytes.NewReader(b), nil)
	var pages [][]byte
	for {
		p, err := r.ReadPage()
		if err != nil {
			break
		}
		pages = append(pages, p)
	}
	if len(pages) < 8 {
		t.Fatalf("fixture has only %d pages", len(pages))
	}
	return pages
}

// Real lacing, real packet splits, the encoder's own header layout: the
// structural contract holds on a libvorbis file, not only on the synthetic
// tracks.
func TestChainRealVorbisFraming(t *testing.T) {
	pages := loadFixturePages(t)
	h := headerPageCount(pages)
	hp := headerPacketCount(t, pages[:h])
	orig := parseChain(t, concatPages(pages))
	if len(orig) != 1 {
		t.Fatalf("fixture parses into %d streams", len(orig))
	}
	chain := newTestChain(t, 12<<10, nil)
	m, d, fs := newDrainHarness(t, chain, 1)
	feed(d, pages)
	streams := parseChain(t, fs.buf.Bytes())
	seams := len(streams) - 1
	if seams < 2 {
		t.Fatalf("%d seams over a %d KiB file, want at least 2", seams, sumLen(pages)>>10)
	}
	if streams[0].serial != orig[0].serial {
		t.Fatal("the first link lost the file's own serial")
	}
	if p := streams[0].pages[len(streams[0].pages)-1]; p.flags&0x04 == 0 {
		t.Fatal("the first link does not end with EOS")
	}
	var got [][]byte
	for i, s := range streams {
		if i > 0 {
			for j := 0; j < h; j++ {
				o := parsePage(pages[j])
				if s.pages[j].seq != uint32(j) || s.pages[j].gran != 0 || !bytes.Equal(s.pages[j].body, o.body) {
					t.Fatalf("link %d header page %d is not the file's own header page", i+1, j)
				}
			}
			if s.pages[h].flags&0x01 != 0 {
				t.Fatalf("link %d opens on a continued page", i+1)
			}
			if s.serial == orig[0].serial || s.serial == streams[i-1].serial {
				t.Fatalf("link %d serial %08x is not fresh", i+1, s.serial)
			}
		}
		assertContiguousSeq(t, s, 0)
		pk := s.packets
		if i > 0 {
			pk = pk[hp:]
		}
		got = append(got, pk...)
	}
	if len(got) != len(orig[0].packets) {
		t.Fatalf("%d packets on the wire, want %d", len(got), len(orig[0].packets))
	}
	for i := range got {
		if !bytes.Equal(got[i], orig[0].packets[i]) {
			t.Fatalf("packet %d differs", i)
		}
	}
	if !equalGranules(audioGranules(streams, h), inputGranules(pages, h)) {
		t.Fatal("granule positions were rewritten")
	}
	if m.sinkSeams != seams {
		t.Fatalf("sinkSeams = %d, want %d", m.sinkSeams, seams)
	}
}

// The naive cut's cost in PCM, measured with the reference decoder: each
// seam loses the overlap between the last packet of one link and the first
// of the next, a quarter block on either side, so between bs0/2 and bs1/2
// samples. Byte identity would be the primer packet's acceptance test.
// Skipped without oggdec (vorbis-tools); CI installs it.
func TestChainRealVorbisPCM(t *testing.T) {
	oggdec, err := exec.LookPath("oggdec")
	if err != nil {
		t.Skip("oggdec (vorbis-tools) not on PATH")
	}
	pages := loadFixturePages(t)
	ident := parsePage(pages[0]).body
	if len(ident) < 30 || string(ident[:7]) != "\x01vorbis" {
		t.Fatal("fixture does not open with a Vorbis identification header")
	}
	channels := int64(ident[11])
	bs0 := int64(1) << (ident[28] & 0x0f)
	bs1 := int64(1) << (ident[28] >> 4)

	chain := newTestChain(t, 8<<10, nil)
	m, d, fs := newDrainHarness(t, chain, 1)
	feed(d, pages)
	seams := int64(m.sinkSeams)
	if seams < 2 {
		t.Fatalf("%d seams, want at least 2", seams)
	}
	dir := t.TempDir()
	origPath := filepath.Join(dir, "orig.ogg")
	splitPath := filepath.Join(dir, "split.ogg")
	if err := os.WriteFile(origPath, concatPages(pages), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(splitPath, fs.buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	decode := func(in string) int64 {
		out := in + ".raw"
		cmd := exec.Command(oggdec, "-Q", "-R", "-b", "16", "-e", "0", "-s", "1", "-o", out, in)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("oggdec %s: %v\n%s", in, err, b)
		}
		fi, err := os.Stat(out)
		if err != nil {
			t.Fatal(err)
		}
		return fi.Size() / 2 / channels
	}
	origSamples, splitSamples := decode(origPath), decode(splitPath)
	loss := origSamples - splitSamples
	lo, hi := seams*bs0/2, seams*bs1/2
	if loss < lo || loss > hi {
		t.Fatalf("naive cut lost %d samples over %d seams (%d -> %d), want between %d and %d (a quarter block each side per seam, blocks %d/%d)",
			loss, seams, origSamples, splitSamples, lo, hi, bs0, bs1)
	}
}
