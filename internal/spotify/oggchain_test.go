// Tests for the Ogg chain seam (oggchain.go): the byte helpers, the trigger
// (bytes and memory gate), the deferred seam line, the effectiveness latch
// and the NAND knob. The drain-level tests that push synthetic and real
// Vorbis tracks through the real loop live in drain_test.go; the harness
// they share (page builders, libogg-style layout, chain parser, fake sink)
// is here.

package spotify

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// ---- harness -------------------------------------------------------------

// buildPage assembles one Ogg page with a real checksum from explicit
// fields. Unlike makeOggPage it takes the lacing table, so packets can span
// pages and end on a 255.
func buildPage(serial, seq uint32, flags byte, granule int64, lacing, body []byte) []byte {
	p := make([]byte, 0, 27+len(lacing)+len(body))
	p = append(p, 'O', 'g', 'g', 'S', 0, flags)
	p = binary.LittleEndian.AppendUint64(p, uint64(granule))
	p = binary.LittleEndian.AppendUint32(p, serial)
	p = binary.LittleEndian.AppendUint32(p, seq)
	p = append(p, 0, 0, 0, 0)
	p = append(p, byte(len(lacing)))
	p = append(p, lacing...)
	p = append(p, body...)
	oggReseal(p)
	return p
}

// lace returns the lacing values for a packet of n bytes (RFC 3533: 255-runs
// and a final value below 255, a trailing 0 when n is a multiple of 255).
func lace(n int) []byte {
	var l []byte
	for n >= 255 {
		l = append(l, 255)
		n -= 255
	}
	return append(l, byte(n))
}

// layoutPackets lays packets onto pages: at most 255 segments per page, a
// page closes once its body reaches budget. With span=false it closes at
// the next packet boundary (a current libogg, which never spans a packet
// unless the segment table is full); with span=true it closes right where
// the budget is hit, splitting the packet across pages with the
// continuation flag on the next one (an old libogg, the hostile framing).
// A page's granule is that of the last packet that completes on it, -1
// when none does. eos flags the final page.
func layoutPackets(serial, firstSeq uint32, packets [][]byte, granules []int64, budget int, span, eos bool) [][]byte {
	var pages [][]byte
	seq := firstSeq
	var segs, body []byte
	lastGran, completed, continued := int64(-1), false, false
	flush := func(last bool) {
		if len(segs) == 0 {
			return
		}
		var flags byte
		if continued {
			flags |= 0x01
		}
		if last && eos {
			flags |= 0x04
		}
		g := lastGran
		if !completed {
			g = -1
		}
		pages = append(pages, buildPage(serial, seq, flags, g, segs, body))
		seq++
		segs, body, completed = nil, nil, false
	}
	for i, pkt := range packets {
		if !span && len(body) >= budget {
			flush(false)
			continued = false
		}
		off := 0
		ls := lace(len(pkt))
		for j, l := range ls {
			if len(segs) == 255 || (span && len(body) >= budget && len(segs) > 0) {
				flush(false)
				continued = j > 0
			}
			segs = append(segs, l)
			body = append(body, pkt[off:off+int(l)]...)
			off += int(l)
			if j == len(ls)-1 {
				lastGran, completed = granules[i], true
			}
		}
	}
	flush(true)
	return pages
}

// synthVorbisTrack builds a track in the shape go-librespot's passthrough
// emits: a 58-byte BOS page with a Vorbis identification header, a comment
// page, a setup page, then nAudio packets of 700..1400 bytes (one of them a
// multiple of 255) laid out like a current libogg (pages end at packet
// boundaries) with granules advancing 128/576/1024 per packet. The final
// page carries EOS when eos is set.
func synthVorbisTrack(serial uint32, nAudio int, eos bool) [][]byte {
	return synthTrack(serial, nAudio, false, eos)
}

// synthVorbisTrackSpanning is the same track laid out by an old libogg:
// packets split across pages wherever the page fills, so most pages end on
// a 255 and start with the continuation flag.
func synthVorbisTrackSpanning(serial uint32, nAudio int, eos bool) [][]byte {
	return synthTrack(serial, nAudio, true, eos)
}

func synthTrack(serial uint32, nAudio int, span, eos bool) [][]byte {
	ident := make([]byte, 30)
	copy(ident, "\x01vorbis")
	ident[11] = 2 // channels
	binary.LittleEndian.PutUint32(ident[12:16], 44100)
	ident[28] = 0xB8 // blocksizes 256 / 2048
	ident[29] = 1    // framing
	comment := append([]byte("\x03vorbis"), []byte("\x06\x00\x00\x00STRtst\x00\x00\x00\x00\x01")...)
	setup := append([]byte("\x05vorbis"), bytes.Repeat([]byte{0x5a}, 900)...)
	pages := [][]byte{
		buildPage(serial, 0, 0x02, 0, lace(len(ident)), ident),
		buildPage(serial, 1, 0, 0, lace(len(comment)), comment),
		buildPage(serial, 2, 0, 0, lace(len(setup)), setup),
	}
	var packets [][]byte
	var grans []int64
	var g int64
	steps := []int64{128, 576, 1024}
	for i := 0; i < nAudio; i++ {
		n := 700 + (i*131)%701
		if i == nAudio/2 {
			n = 1020 // 4*255: lacing ends with a 0
		}
		pkt := make([]byte, n)
		for k := range pkt {
			pkt[k] = byte(i*7 + k*13)
		}
		pkt[0] &^= 0x01 // audio packet type
		g += steps[i%3]
		packets = append(packets, pkt)
		grans = append(grans, g)
	}
	return append(pages, layoutPackets(serial, 3, packets, grans, 4096, span, eos)...)
}

func concatPages(pages [][]byte) []byte {
	return bytes.Join(pages, nil)
}

type parsedPage struct {
	serial, seq uint32
	flags       byte
	gran        int64
	lacing      []byte
	body        []byte
	raw         []byte
}

func parsePage(p []byte) parsedPage {
	n := int(p[26])
	return parsedPage{
		serial: binary.LittleEndian.Uint32(p[14:18]),
		seq:    binary.LittleEndian.Uint32(p[18:22]),
		flags:  p[5],
		gran:   int64(binary.LittleEndian.Uint64(p[6:14])),
		lacing: p[27 : 27+n],
		body:   p[27+n:],
		raw:    p,
	}
}

// logicalStream is one run of pages sharing a serial, with the packets
// reassembled across pages (continuation honoured).
type logicalStream struct {
	serial  uint32
	pages   []parsedPage
	packets [][]byte
}

// parseChain runs the real page reader over stream (a page with a bad
// checksum is dropped exactly as on the box) and groups the pages into
// logical streams by serial.
func parseChain(t *testing.T, stream []byte) []logicalStream {
	t.Helper()
	r := newOggPageReader(bytes.NewReader(stream), nil)
	var out []logicalStream
	var partial []byte
	for {
		p, err := r.ReadPage()
		if err != nil {
			break
		}
		pp := parsePage(p)
		if len(out) == 0 || out[len(out)-1].serial != pp.serial {
			out = append(out, logicalStream{serial: pp.serial})
			partial = nil
		}
		cur := &out[len(out)-1]
		cur.pages = append(cur.pages, pp)
		off := 0
		for _, l := range pp.lacing {
			partial = append(partial, pp.body[off:off+int(l)]...)
			off += int(l)
			if l < 255 {
				cur.packets = append(cur.packets, partial)
				partial = nil
			}
		}
	}
	return out
}

func assertContiguousSeq(t *testing.T, s logicalStream, from uint32) {
	t.Helper()
	for i, p := range s.pages {
		if want := from + uint32(i); p.seq != want {
			t.Fatalf("stream %08x page %d: seq %d, want %d (a hole makes libogg flag a loss and GStreamer re-prime)", s.serial, i, p.seq, want)
		}
	}
}

// headerPageCount is the drain's own rule: the leading pages up to (not
// including) the first page with a granule above zero.
func headerPageCount(pages [][]byte) int {
	for i, p := range pages {
		if i > 0 && oggGranule(p) > 0 {
			return i
		}
	}
	return len(pages)
}

// fakeSink stands in for the box's HTTP connection: it records every write
// as one chunk (the chunk boundaries are part of the contract) and the
// concatenated bytes.
type fakeSink struct {
	buf    bytes.Buffer
	chunks [][]byte
}

func (f *fakeSink) Write(p []byte) (int, error) {
	f.chunks = append(f.chunks, append([]byte(nil), p...))
	return f.buf.Write(p)
}

func (f *fakeSink) Flush() {}

// newTestChain returns a chain cutter with the given period in bytes and no
// other gate: minimum link 0, memory unknown (memAvail nil unless given),
// fallback = period, so a seam fires exactly when a legal page crosses
// period. The knob path points into a temp dir (absent unless a test
// writes it).
func newTestChain(t *testing.T, period int64, memAvail func() int64) *oggChain {
	t.Helper()
	c := newOggChain(memAvail, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.overridePath = filepath.Join(t.TempDir(), "spotify-chain-mb")
	c.defaultPeriod = period
	c.minBytes, c.probeBytes, c.fallbackBytes = 0, 0, period
	c.loadOverride()
	return c
}

// newDrainHarness wires a Manager with a fake sink and the real drain step.
// Pacing is off; the engine-hot window is held open so the no-consumer path
// can never try to pause an engine that does not exist.
func newDrainHarness(t *testing.T, chain *oggChain, flushBytes int) (*Manager, *oggDrain, *fakeSink) {
	t.Helper()
	m := newStallTestManager()
	m.engineHotUntil = time.Now().Add(time.Hour)
	fs := &fakeSink{}
	m.sink = fs
	chain.publish = func(s chainSnapshot) {
		m.mu.Lock()
		m.chainSnap = s
		m.mu.Unlock()
	}
	d := m.newOggDrain(flushBytes, 0, chain)
	d.paused = true
	return m, d, fs
}

// holdSeams stops further seams: the ceiling is raised out of reach both
// live and for the re-read at the next link open / track boundary.
func holdSeams(c *oggChain) {
	c.period, c.defaultPeriod, c.fallbackBytes = 1<<40, 1<<40, 1<<40
}

// feed pushes pages through the drain, each as a fresh allocation the way
// ReadPage hands them over (the drain patches pages in place).
func feed(d *oggDrain, pages [][]byte) {
	for _, p := range pages {
		d.page(context.Background(), append([]byte(nil), p...))
	}
}

// recHandler records slog output for assertions on the seam lines.
type recHandler struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (h *recHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.recs = append(h.recs, r.Clone())
	h.mu.Unlock()
	return nil
}
func (h *recHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recHandler) WithGroup(string) slog.Handler      { return h }

func (h *recHandler) withMsg(sub string) []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []slog.Record
	for _, r := range h.recs {
		if bytes.Contains([]byte(r.Message), []byte(sub)) {
			out = append(out, r)
		}
	}
	return out
}

func recAttr(r slog.Record, key string) (slog.Value, bool) {
	var v slog.Value
	found := false
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			v, found = a.Value, true
			return false
		}
		return true
	})
	return v, found
}

func recInt(t *testing.T, r slog.Record, key string) int64 {
	t.Helper()
	v, ok := recAttr(r, key)
	if !ok {
		t.Fatalf("seam line lacks %q: %v", key, r.Message)
	}
	return v.Int64()
}

// legalAudioPage is a page a seam may end on: completes its last packet
// (final lacing < 255), granule above zero, neither BOS nor EOS.
func legalAudioPage(serial uint32, seq uint32, gran int64, n int) []byte {
	body := make([]byte, n)
	for i := range body {
		body[i] = byte(i)
	}
	return buildPage(serial, seq, 0, gran, lace(n), body)
}

// ---- byte helpers --------------------------------------------------------

func TestRewriteOggPageIDs(t *testing.T) {
	orig := makeOggPage(0x00, 4242, []byte("audio-body"))
	p := append([]byte(nil), orig...)
	rewriteOggPageIDs(p, 0xdeadbeef, 77)
	if got := binary.LittleEndian.Uint32(p[14:18]); got != 0xdeadbeef {
		t.Fatalf("serial = %08x, want deadbeef", got)
	}
	if got := binary.LittleEndian.Uint32(p[18:22]); got != 77 {
		t.Fatalf("seq = %d, want 77", got)
	}
	if got := binary.LittleEndian.Uint32(p[22:26]); got != oggPageCRC(p) {
		t.Fatalf("checksum not resealed")
	}
	// Everything else is untouched: flags, granule, segment table, body.
	if !bytes.Equal(p[:14], orig[:14]) || !bytes.Equal(p[26:], orig[26:]) {
		t.Fatalf("rewrite touched bytes outside serial/seq/crc")
	}
	// The real reader accepts the resealed page.
	r := newOggPageReader(bytes.NewReader(p), nil)
	if got, err := r.ReadPage(); err != nil || !bytes.Equal(got, p) {
		t.Fatalf("resealed page rejected by the reader: %v", err)
	}
}

func TestOggLastLacingAndPageLen(t *testing.T) {
	empty := buildPage(1, 0, 0, -1, nil, nil)
	if oggLastLacing(empty) != 0 || oggPageLen(empty) != 27 {
		t.Fatalf("0-segment page: lacing %d len %d, want 0/27", oggLastLacing(empty), oggPageLen(empty))
	}
	one := buildPage(1, 1, 0, 10, []byte{7}, []byte("1234567"))
	if oggLastLacing(one) != 7 || oggPageLen(one) != 27+1+7 {
		t.Fatalf("1-segment page: lacing %d len %d", oggLastLacing(one), oggPageLen(one))
	}
	spanning := buildPage(1, 2, 0, -1, []byte{100, 255, 255}, make([]byte, 610))
	if oggLastLacing(spanning) != 255 {
		t.Fatalf("255-terminated page: lacing %d, want 255", oggLastLacing(spanning))
	}
	if oggPageLen(spanning) != len(spanning) {
		t.Fatalf("spanning page len %d, want %d", oggPageLen(spanning), len(spanning))
	}
	// oggPageLen walks a concatenation page by page.
	all := concatPages([][]byte{empty, one, spanning})
	off, n := 0, 0
	for off < len(all) {
		l := oggPageLen(all[off:])
		if l <= 0 {
			t.Fatalf("walk stuck at %d", off)
		}
		off += l
		n++
	}
	if n != 3 || off != len(all) {
		t.Fatalf("walked %d pages to %d, want 3 pages to %d", n, off, len(all))
	}
	if oggLastLacing([]byte("short")) != 0 || oggPageLen([]byte("short")) != 0 {
		t.Fatal("short input must read as 0")
	}
}

func TestNewSerialAvoids(t *testing.T) {
	for i := 0; i < 2000; i++ {
		s := newSerial(0x11111111, 0x22222222)
		if s == 0 || s == 0x11111111 || s == 0x22222222 {
			t.Fatalf("newSerial returned a forbidden value %08x", s)
		}
	}
}

// ---- trigger -------------------------------------------------------------

// pageFeeder emits legal audio pages of a fixed size whose granules advance
// at a real stream rate, so a link's byte count and the audio seconds it
// carries stand in the relation the box sees (perSec 20000 = 160 kbps, the
// rate at which chainMinBytes is ~105 s). Sequence and granule run on across
// links: both are per track, and the granule is what the chain's time floor
// reads.
type pageFeeder struct {
	serial    uint32
	pageBytes int
	perSec    int64
	seq       uint32
	gran      int64
	sent      int64
}

func newPageFeeder(serial uint32, pageBytes int, perSec int64) *pageFeeder {
	return &pageFeeder{serial: serial, pageBytes: pageBytes, perSec: perSec, seq: 2}
}

func (f *pageFeeder) next() []byte {
	f.sent += int64(f.pageBytes)
	f.gran = f.sent * vorbisRate / f.perSec
	f.seq++
	return legalAudioPage(f.serial, f.seq, f.gran, f.pageBytes-27-len(lace(f.pageBytes-40)))
}

// pump stamps pages through c until it cuts or the current link reaches
// limit bytes. Returns the link size at the seam (-1 when none) and the
// reason.
func (f *pageFeeder) pump(c *oggChain, limit int64, now time.Time) (int64, string) {
	for c.since < limit {
		p := f.next()
		if c.cutHere(p, true, now) {
			c.stamp(p)
			return c.since, c.cut.reason
		}
		c.stamp(p)
	}
	return -1, ""
}

// prodChain is a chain with the production thresholds and a 10 MiB ceiling,
// its knob path pointing at an absent file in a temp dir.
func prodChain(t *testing.T, mem func() int64) *oggChain {
	t.Helper()
	c := newOggChain(mem, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.overridePath = filepath.Join(t.TempDir(), "absent")
	c.defaultPeriod = 10 << 20
	c.loadOverride()
	return c
}

// The trigger with the production thresholds: a healthy box never seams a
// normal song, the ceiling caps a long track, and an unreadable meminfo falls
// back to a fixed period. Memory is probed at most once per chainProbeBytes.
func TestChainMemoryGate(t *testing.T) {
	const pageBytes = 3200
	now := time.Now()
	run := func(t *testing.T, mem func() int64, limit int64) (seamAt int64, reason string) {
		t.Helper()
		return newPageFeeder(0x1234, pageBytes, 20000).pump(prodChain(t, mem), limit, now)
	}

	// Healthy box (20 MB free): nothing through 10 MiB minus a page, then the
	// ceiling.
	probes := 0
	c := prodChain(t, func() int64 { probes++; return 20000 })
	f := newPageFeeder(0x1234, pageBytes, 20000)
	if at, why := f.pump(c, (10<<20)-pageBytes, now); at >= 0 {
		t.Fatalf("healthy box seamed at %d KB (%s) before the ceiling", at/1024, why)
	}
	if probes > (8<<20)/chainProbeBytes+2 {
		t.Fatalf("%d memory probes over 8 MiB, want at most one per %d KB", probes, chainProbeBytes/1024)
	}
	if probes == 0 {
		t.Fatal("no memory probe at all past the minimum link")
	}
	at, why := f.pump(c, 11<<20, now)
	if at < 10<<20 || why != "period" {
		t.Fatalf("ceiling: seam at %d KB (%s), want >= 10 MiB / period", at/1024, why)
	}

	// Memory unreadable (-1): the fallback period.
	at, why = run(t, func() int64 { return -1 }, 10<<20)
	if at < chainFallbackBytes || at > chainFallbackBytes+pageBytes || why != "fallback" {
		t.Fatalf("unreadable meminfo: seam at %d KB (%s), want %d KB / fallback", at/1024, why, chainFallbackBytes/1024)
	}
	// No reader at all (tests, dev hosts): the same fallback.
	at, why = run(t, nil, 10<<20)
	if at < chainFallbackBytes || at > chainFallbackBytes+pageBytes || why != "fallback" {
		t.Fatalf("nil reader: seam at %d KB (%s), want %d KB / fallback", at/1024, why, chainFallbackBytes/1024)
	}
	// Ceiling with plenty of memory.
	at, why = run(t, func() int64 { return 30000 }, 11<<20)
	if at < 10<<20 || why != "period" {
		t.Fatalf("30 MB free: seam at %d KB (%s), want the 10 MiB ceiling", at/1024, why)
	}
}

// The memory reason is an attribution, not a level (review 2026-09-07). A box
// sitting low for an unrelated reason must never be seamed early; a box whose
// MemAvailable falls with the audio of this link must be.
func TestChainMemoryGateNeedsFallOnThisLink(t *testing.T) {
	const pageBytes = 3200
	now := time.Now()

	// The regression: 8 MB free the whole time, flat, nothing to do with the
	// stream (the BoseApp leak family lives here for hours). The old absolute
	// gate seamed every link at the 2 MiB minimum; the only seam allowed now
	// is the ceiling.
	flat := prodChain(t, func() int64 { return 8000 })
	at, why := newPageFeeder(0x1234, pageBytes, 20000).pump(flat, 11<<20, now)
	if at < 10<<20 || why != "period" {
		t.Fatalf("a box low for an unrelated reason seamed at %d KB (%s), want only the 10 MiB ceiling",
			at/1024, why)
	}

	// The field signature, steep: MemAvailable falling 4 bytes per byte of
	// audio. The baseline is taken at the minimum link, so the seam lands one
	// probe interval later, not at the ceiling.
	var steep *oggChain
	steepMem := func() int64 { return 21192 - steep.since*4/1024 }
	steep = prodChain(t, steepMem)
	at, why = newPageFeeder(0x1234, pageBytes, 20000).pump(steep, 11<<20, now)
	if why != "memory" {
		t.Fatalf("a leaking box seamed at %d KB for %q, want memory", at/1024, why)
	}
	if at > chainMinBytes+chainProbeBytes+8*pageBytes {
		t.Fatalf("leaking box seamed at %d KB, want within one probe interval of the %d KB minimum",
			at/1024, chainMinBytes/1024)
	}

	// The field signature at the measured field rate (1.25 bytes per byte,
	// 2026-09-07): still a memory seam, and still far below the ceiling.
	var leak *oggChain
	leakMem := func() int64 { return 14000 - leak.since*5/4/1024 }
	leak = prodChain(t, leakMem)
	at, why = newPageFeeder(0x1234, pageBytes, 20000).pump(leak, 11<<20, now)
	if why != "memory" || at > 4<<20 {
		t.Fatalf("field-rate leak seamed at %d KB (%s), want a memory seam below 4 MiB", at/1024, why)
	}
}

// The time floor: a memory reading may not end a link that carries less than
// chainMemMinSec of audio, so a high bitrate cannot turn the byte minimum
// into a seam every ~52 s.
func TestChainMemoryGateTimeFloor(t *testing.T) {
	const pageBytes = 3200
	const perSec = 5000 // 40 kbps: chainMemMinSec of audio is ~293 KB
	now := time.Now()
	var c *oggChain
	mem := func() int64 { return 20000 - c.since*40/1024 }
	c = newOggChain(mem, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.overridePath = filepath.Join(t.TempDir(), "absent")
	c.defaultPeriod = 64 << 20
	c.minBytes, c.probeBytes = 64<<10, 16<<10
	c.loadOverride()
	f := newPageFeeder(0x5678, pageBytes, perSec)

	// 56 s of audio: low, falling hard, well past the minimum link, and still
	// no seam.
	if at, why := f.pump(c, 280<<10, now); at >= 0 {
		t.Fatalf("seamed at %d KB / %d s of audio (%s), want nothing before %d s",
			at/1024, f.gran/vorbisRate, why, chainMemMinSec)
	}
	// ... and the floor is demonstrably the only thing that held it: the level
	// and the fall both cleared long ago.
	if c.avail < 0 || c.avail >= c.lowWaterKB {
		t.Fatalf("memAvail = %d KB, not below the %d KB low water: the test proves nothing", c.avail, c.lowWaterKB)
	}
	if fell := c.availAtStart - c.avail; fell < (280<<10)/chainMemFallDiv {
		t.Fatalf("fell only %d KB over the link: the leak signature is missing", fell)
	}

	at, why := f.pump(c, 512<<10, now)
	if why != "memory" {
		t.Fatalf("no memory seam once the floor is behind the link: %d KB (%s)", at/1024, why)
	}
	if c.cut.linkSec < chainMemMinSec {
		t.Fatalf("seam at %d s of audio, want at least %d", c.cut.linkSec, chainMemMinSec)
	}
	if c.cut.linkSec > chainMemMinSec+5 {
		t.Fatalf("seam at %d s of audio, want it right at the %d s floor", c.cut.linkSec, chainMemMinSec)
	}
}

// The structural preconditions, one by one.
func TestChainCutHerePreconditions(t *testing.T) {
	c := newTestChain(t, 1, nil) // any legal page crosses the period
	now := time.Now()
	legal := legalAudioPage(1, 5, 1000, 300)
	if c.cutHere(append([]byte(nil), legal...), false, now) {
		t.Fatal("no header set captured yet: must not cut")
	}
	bos := buildPage(1, 0, 0x02, 0, []byte{30}, make([]byte, 30))
	if c.cutHere(bos, true, now) {
		t.Fatal("a BOS page must never be P")
	}
	eos := buildPage(1, 9, 0x04, 5000, []byte{10}, make([]byte, 10))
	if c.cutHere(eos, true, now) {
		t.Fatal("a page already carrying EOS must never be P")
	}
	noPacket := buildPage(1, 6, 0, -1, []byte{255, 255}, make([]byte, 510))
	if c.cutHere(noPacket, true, now) {
		t.Fatal("a granule -1 page completes no packet: must not cut")
	}
	spanning := buildPage(1, 7, 0, 2000, []byte{100, 255, 255}, make([]byte, 610))
	if c.cutHere(spanning, true, now) {
		t.Fatal("a page whose last packet continues on the next (final lacing 255) must not be P")
	}
	if !c.cutHere(append([]byte(nil), legal...), true, now) {
		t.Fatal("a legal page crossing the period must cut")
	}
	if c.cutHere(legalAudioPage(1, 6, 2000, 300), true, now) {
		t.Fatal("no second cut while the link is still pending")
	}
	c.openLink(concatPages(synthVorbisTrack(1, 0, false)))
	c.latched = true
	if c.cutHere(legalAudioPage(1, 7, 3000, 300), true, now) {
		t.Fatal("latched: must not cut")
	}
	c.latched = false
	c.period = 0
	if c.cutHere(legalAudioPage(1, 8, 4000, 300), true, now) {
		t.Fatal("period 0 (off): must not cut")
	}
}

// ---- seam line -----------------------------------------------------------

// One line per seam, with the memory reading before AND after the cut,
// written once the box has had its 20 s, or earlier when a real BOS, a
// detach or the engine exit gets there first.
func TestChainProbeLine(t *testing.T) {
	h := &recHandler{}
	avail := int64(8000)
	c := newOggChain(func() int64 { return avail }, slog.New(h))
	c.overridePath = filepath.Join(t.TempDir(), "spotify-chain-mb")
	c.defaultPeriod = 1 << 30
	c.minBytes, c.probeBytes = 0, 0
	c.loadOverride()
	c.engineRSS = func() int64 { return 31000 }
	hdr := concatPages(synthVorbisTrack(0xabcd, 0, false))
	t0 := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	const seamMsg = "chain seam, long track split"

	seq, gran := uint32(2), int64(0)
	// memCut takes a memory-reason cut the way the gate demands one: a first
	// page records the link's starting MemAvailable while memory is still
	// high, then MemAvailable drops (the leak signature) and a second page,
	// 100 s of audio into the link, carries the cut.
	memCut := func(t *testing.T) {
		t.Helper()
		avail = 30000
		seq++
		gran += 10 * vorbisRate
		arm := legalAudioPage(0xabcd, seq, gran, 500)
		if c.cutHere(arm, true, t0) {
			t.Fatal("the arming page must not cut: memory has not fallen over this link yet")
		}
		c.stamp(arm)
		avail = 8000
		seq++
		gran += 90 * vorbisRate
		p := legalAudioPage(0xabcd, seq, gran, 3000)
		if !c.cutHere(p, true, t0) {
			t.Fatal("memory gate: 30000 -> 8000 KB over a 100 s link must cut")
		}
		c.stamp(p)
	}

	memCut(t)
	linkBytes := c.since
	c.openLink(hdr)
	c.probe(t0.Add(5*time.Second), "")
	if n := len(h.withMsg(seamMsg)); n != 0 {
		t.Fatalf("seam line written after 5 s (%d lines), must wait for the box's free", n)
	}
	avail = 20000
	c.probe(t0.Add(25*time.Second), "")
	lines := h.withMsg(seamMsg)
	if len(lines) != 1 {
		t.Fatalf("%d seam lines after 25 s, want exactly 1", len(lines))
	}
	l := lines[0]
	if l.Level != slog.LevelInfo {
		t.Fatalf("seam line level %v, want INFO (the bundle must carry it)", l.Level)
	}
	if got := recInt(t, l, "memBeforeKB"); got != 8000 {
		t.Errorf("memBeforeKB = %d, want 8000", got)
	}
	if got := recInt(t, l, "memAfterKB"); got != 20000 {
		t.Errorf("memAfterKB = %d, want 20000", got)
	}
	if got := recInt(t, l, "freedKB"); got != 12000 {
		t.Errorf("freedKB = %d, want 12000", got)
	}
	if got := recInt(t, l, "probeSec"); got != 25 {
		t.Errorf("probeSec = %d, want 25", got)
	}
	if got := recInt(t, l, "linkKB"); got != linkBytes/1024 {
		t.Errorf("linkKB = %d, want %d", got, linkBytes/1024)
	}
	if got := recInt(t, l, "granuleSec"); got != 100 {
		t.Errorf("granuleSec = %d, want 100", got)
	}
	if got := recInt(t, l, "engineRSSKB"); got != 31000 {
		t.Errorf("engineRSSKB = %d, want 31000", got)
	}
	// The evidence a field bundle needs to judge a memory-reason seam: what
	// the link started from, how far MemAvailable fell over it, how much audio
	// it carried, and the ratio the gate compared against 0.50.
	if got := recInt(t, l, "availAtLinkStartKB"); got != 30000 {
		t.Errorf("availAtLinkStartKB = %d, want 30000", got)
	}
	if got := recInt(t, l, "fellKB"); got != 22000 {
		t.Errorf("fellKB = %d, want 22000 (30000 -> 8000 over the link)", got)
	}
	if got := recInt(t, l, "linkSec"); got != 100 {
		t.Errorf("linkSec = %d, want 100", got)
	}
	if v, ok := recAttr(l, "fellPerByte"); !ok || v.String() == "" {
		t.Error("the seam line must carry the fell-per-byte ratio the memory reason was judged on")
	}
	if v, _ := recAttr(l, "reason"); v.String() != "memory" {
		t.Errorf("reason = %q, want memory", v.String())
	}
	if v, _ := recAttr(l, "oldSerial"); v.String() != "0000abcd" {
		t.Errorf("oldSerial = %q, want 0000abcd", v.String())
	}
	if v, _ := recAttr(l, "newSerial"); v.String() == "00000000" || v.String() == "0000abcd" {
		t.Errorf("newSerial = %q, want a fresh serial", v.String())
	}
	if _, forced := recAttr(l, "forcedBy"); forced {
		t.Error("an unforced line must not carry forcedBy")
	}
	c.probe(t0.Add(time.Minute), "")
	if n := len(h.withMsg(seamMsg)); n != 1 {
		t.Fatalf("a second probe wrote another line (%d total): one line per seam", n)
	}

	// Forced early by a real BOS, a detach and the engine exit: the line comes
	// at once and says why and how long it waited.
	for i, force := range []string{"bos", "detach", "engine-exit"} {
		memCut(t)
		c.openLink(hdr)
		c.probe(t0.Add(time.Second), force)
		got := h.withMsg(seamMsg)
		if len(got) != 2+i {
			t.Fatalf("%s: %d seam lines, want %d", force, len(got), 2+i)
		}
		last := got[len(got)-1]
		if v, ok := recAttr(last, "forcedBy"); !ok || v.String() != force {
			t.Errorf("forced line lacks forcedBy=%s: %v", force, v)
		}
		if got := recInt(t, last, "probeSec"); got != 1 {
			t.Errorf("%s: probeSec = %d, want 1", force, got)
		}
	}

	// A cut that a real BOS overtook before the link opened still gets its
	// line, marked as deferred, with newSerial 0.
	memCut(t)
	c.reset(t0.Add(2*time.Second), 2)
	got := h.withMsg(seamMsg)
	last := got[len(got)-1]
	if v, ok := recAttr(last, "deferredToBOS"); !ok || !v.Bool() {
		t.Error("a cut overtaken by a real BOS must be marked deferredToBOS")
	}
	if v, _ := recAttr(last, "newSerial"); v.String() != "00000000" {
		t.Errorf("deferred cut newSerial = %q, want 00000000", v.String())
	}
	if c.open || c.linkPending {
		t.Error("reset must drop the pending link")
	}

	// Info vs Debug: under a standing override (a sweep), the second effective
	// seam of a track goes to Debug so a 1 MiB period cannot fill the NAND ring.
	if err := os.WriteFile(c.overridePath, []byte("3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c.reset(t0, 3)
	if !c.overridden || c.period != 3<<20 {
		t.Fatalf("override not picked up at reset: overridden=%v period=%d", c.overridden, c.period)
	}
	before := len(h.withMsg(seamMsg))
	levels := []slog.Level{}
	for i := 0; i < 2; i++ {
		memCut(t)
		c.openLink(hdr)
		avail = 20000 // freed far more than 10 % of the link
		c.probe(t0.Add(25*time.Second), "")
		got := h.withMsg(seamMsg)
		if len(got) != before+1+i {
			t.Fatalf("sweep seam %d: %d lines", i, len(got))
		}
		levels = append(levels, got[len(got)-1].Level)
	}
	if levels[0] != slog.LevelInfo || levels[1] != slog.LevelDebug {
		t.Fatalf("override sweep levels = %v, want [INFO DEBUG]", levels)
	}
}

// Three consecutive seams that free under 10 % of their link warn once and
// stop seaming until the next real track boundary.
func TestChainEffectivenessLatch(t *testing.T) {
	h := &recHandler{}
	c := newOggChain(func() int64 { return 20000 }, slog.New(h)) // never moves: nothing is freed
	c.overridePath = filepath.Join(t.TempDir(), "absent")
	c.defaultPeriod = 1
	c.minBytes, c.probeBytes = 0, 0
	c.loadOverride()
	hdr := concatPages(synthVorbisTrack(0x77, 0, false))
	t0 := time.Now()
	const latchMsg = "chain seams are not freeing memory"
	for i := 0; i < chainIneffective; i++ {
		if c.latched {
			t.Fatalf("latched after %d seams, want %d", i, chainIneffective)
		}
		q := legalAudioPage(0x77, uint32(3+i), int64(1000*(i+1)), 3000)
		if !c.cutHere(q, true, t0) {
			t.Fatalf("seam %d refused", i)
		}
		c.stamp(q)
		c.openLink(hdr)
		c.probe(t0.Add(25*time.Second), "")
	}
	if !c.latched {
		t.Fatal("not latched after three ineffective seams")
	}
	warns := h.withMsg(latchMsg)
	if len(warns) != 1 || warns[0].Level != slog.LevelWarn {
		t.Fatalf("latch warning: %d lines (level %v), want exactly one WARN", len(warns), warns[0].Level)
	}
	if c.cutHere(legalAudioPage(0x77, 9, 9000, 3000), true, t0) {
		t.Fatal("latched chain still cuts")
	}
	// A forced (early) line must not count: the box had no time to react.
	c.reset(t0, 2)
	if c.latched || c.badRun != 0 {
		t.Fatal("reset must clear the latch and the run")
	}
	for i := 0; i < chainIneffective+1; i++ {
		q := legalAudioPage(0x77, uint32(3+i), int64(1000*(i+1)), 3000)
		if !c.cutHere(q, true, t0) {
			t.Fatalf("after reset: seam %d refused", i)
		}
		c.stamp(q)
		c.openLink(hdr)
		c.probe(t0.Add(time.Second), "detach")
	}
	if c.latched {
		t.Fatal("forced early lines must not feed the latch")
	}
	// An effective seam in the middle resets the run: bad, bad, good, bad,
	// bad never reaches three in a row.
	avail := int64(20000)
	c2 := newOggChain(func() int64 { return avail }, slog.New(h))
	c2.overridePath = filepath.Join(t.TempDir(), "absent")
	c2.defaultPeriod = 1
	c2.minBytes, c2.probeBytes = 0, 0
	c2.loadOverride()
	for i := 0; i < 5; i++ {
		avail = 20000
		q := legalAudioPage(0x77, uint32(3+i), int64(1000*(i+1)), 3000)
		if !c2.cutHere(q, true, t0) {
			t.Fatalf("c2 seam %d refused", i)
		}
		c2.stamp(q)
		c2.openLink(hdr)
		if i == 2 {
			avail = 25000 // this one freed plenty
		}
		c2.probe(t0.Add(25*time.Second), "")
	}
	if c2.latched {
		t.Fatal("an effective seam in the run must reset the count")
	}
	if c2.badRun != 2 {
		t.Fatalf("badRun = %d after bad, bad, good, bad, bad; want 2", c2.badRun)
	}
}

// The NAND knob: absent = default, "0" = off, 1..64 = MiB, garbage =
// default; re-read at a real track boundary; logged once per change.
func TestChainOverrideParse(t *testing.T) {
	h := &recHandler{}
	c := newOggChain(nil, slog.New(h))
	c.overridePath = filepath.Join(t.TempDir(), "spotify-chain-mb")
	c.defaultPeriod = chainPeriodBytes
	c.loadOverride()
	if c.period != chainPeriodBytes || c.overridden {
		t.Fatalf("absent file: period %d overridden %v", c.period, c.overridden)
	}
	cases := []struct {
		in         string
		period     int64
		overridden bool
	}{
		{"0", 0, true},
		{"3", 3 << 20, true},
		{" 64\n", 64 << 20, true},
		{"65", chainPeriodBytes, false},
		{"abc", chainPeriodBytes, false},
		{"", chainPeriodBytes, false},
		{"-1", chainPeriodBytes, false},
	}
	for _, cs := range cases {
		if err := os.WriteFile(c.overridePath, []byte(cs.in), 0o644); err != nil {
			t.Fatal(err)
		}
		c.loadOverride()
		if c.period != cs.period || c.overridden != cs.overridden {
			t.Errorf("%q: period %d overridden %v, want %d/%v", cs.in, c.period, c.overridden, cs.period, cs.overridden)
		}
	}
	// Re-read at reset.
	if err := os.WriteFile(c.overridePath, []byte("5"), 0o644); err != nil {
		t.Fatal(err)
	}
	c.reset(time.Now(), 1)
	if c.period != 5<<20 || !c.overridden {
		t.Fatalf("reset did not re-read the knob: period %d", c.period)
	}
	// Logged once per change, not once per read.
	n := len(h.withMsg("chain seam period overridden"))
	c.reset(time.Now(), 2)
	c.reset(time.Now(), 3)
	if got := len(h.withMsg("chain seam period overridden")); got != n {
		t.Fatalf("an unchanged override logged again (%d -> %d lines)", n, got)
	}
	removed := len(h.withMsg("override removed"))
	if err := os.Remove(c.overridePath); err != nil {
		t.Fatal(err)
	}
	c.reset(time.Now(), 4)
	if c.period != chainPeriodBytes || c.overridden {
		t.Fatalf("removed file: period %d overridden %v", c.period, c.overridden)
	}
	if got := len(h.withMsg("override removed")); got != removed+1 {
		t.Fatalf("override removal logged %d more times, want 1", got-removed)
	}
	c.reset(time.Now(), 5)
	if got := len(h.withMsg("override removed")); got != removed+1 {
		t.Fatal("a still-absent file logged the removal again")
	}
}

// The snapshot the debug section reads reflects every state change.
func TestChainSnapshot(t *testing.T) {
	var published chainSnapshot
	avail := int64(9000)
	c := newTestChain(t, 1, func() int64 { return avail })
	c.publish = func(s chainSnapshot) { published = s }
	c.emit()
	if !published.Enabled || published.SeamsTotal != 0 || published.Latched {
		t.Fatalf("initial snapshot: %+v", published)
	}
	t0 := time.Now()
	q := legalAudioPage(5, 3, 44100, 2000)
	c.cutHere(q, true, t0)
	c.stamp(q)
	c.openLink(concatPages(synthVorbisTrack(5, 0, false)))
	if published.SeamsTotal != 1 || published.LastReason != "period" || published.LiveSerial == "" || published.LastMemBeforeKB != 9000 {
		t.Fatalf("after openLink: %+v", published)
	}
	avail = 21000
	c.probe(t0.Add(time.Minute), "")
	if published.LastMemAfterKB != 21000 || published.LastFreedKB != 12000 {
		t.Fatalf("after probe: %+v", published)
	}
	snap := fmt.Sprintf("%+v", c.snapshot())
	if snap != fmt.Sprintf("%+v", published) {
		t.Fatalf("snapshot() %s != published %+v", snap, published)
	}
}
