// oggchain.go: artificial Ogg chain seams for long Spotify tracks.
//
// The Bose firmware retains memory in proportion to the audio it receives
// within ONE logical Ogg stream and frees it only when a new logical stream
// begins: a BOS page with a new serial. A normal playlist hands it a new
// stream every three to nine minutes, so MemAvailable stays flat for hours;
// a single 60-minute track is one logical stream for the whole hour,
// MemAvailable falls about 1.25 bytes per byte of audio, and the memory guard
// reboots the box after twenty to thirty minutes (field report 2026-09-07,
// SoundTouch 20 sm2, FW 27.0.6: 29.9 MB free -> 3.8 MB in 1092 s of one
// track, while 46 min / 45 MB of ordinary songs, one of them a single 15-min
// HTTP attach, moved nothing). An HTTP re-attach, a pause or a standby frees
// nothing; Bluetooth does not leak.
//
// So the drain cuts a long track into a chain of logical streams itself: the
// current page is flagged end-of-stream, the track's own three header pages
// are re-emitted under a fresh serial, and every following page is re-stamped
// with that serial and a contiguous page sequence, granule positions
// untouched. That is the intersection of three shapes the box already
// decodes today: a natural track end (EOS, then a BOS with a new serial), an
// app skip (BOS with a new serial, no EOS) and a late join (granule-0 headers
// followed by a large absolute granule). The decoder re-initialises from the
// identical headers and the firmware frees what it held for the old link.
//
// Cost: a naive cut loses the overlap between the last packet of the old link
// and the first of the new one (a quarter block each, a few milliseconds),
// heard as at most a tick every ten minutes of a long track. A primer packet
// that would make the cut sample-exact is deliberately not built until a
// hardware A/B says which decoder class the box runs: openLink is the hook.
//
// Trigger (only when the switch below turns the mechanism on): never inside
// the first chainMinBytes of a link, at the latest at the configured period, and earlier only when the memory can be attributed to
// THIS link: MemAvailable below chainLowWaterKB AND fallen by at least half a
// byte per byte of the link since the link's first probe (the field rate is
// 1.25) AND at least chainMemMinSec of audio in the link. Ordinary songs on a
// healthy box therefore never see a seam, a box that merely sits low for an
// unrelated reason only ever sees the chainPeriodBytes ceiling, and a leaking
// box seams well before it reaches the memory guard.
//
// Switch: /mnt/nv/streborn/spotify-chain-mb, absent = OFF (byte-identical
// passthrough, the default since the hardware measurement of 2026-09-08
// recorded at chainPeriodBytes), 0 = off as well, 1..64 = the ceiling in
// MiB, which turns the mechanism on. Re-read at engine start, at every real
// track boundary and after every seam, never polled, so a change lands at
// the next boundary without a restart.

package spotify

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// chainPeriodBytes is the hard ceiling per link (~10.5 min at 160 kbps,
	// ~5 min at 320): an ordinary song never reaches it. Retains ~15 MB on a
	// box that leaks at the field rate.
	//
	// OFF by default since 2026-09-08. The mechanism was built on the
	// hypothesis that the speaker's firmware releases its stream buffer at a
	// logical-stream boundary, which the field data supported: memory fell
	// only inside a single long track and stayed flat across a playlist. A
	// measurement on a live Portable disproved it. Three seams were cut
	// cleanly a megabyte apart, playback continued, and the box freed
	// nothing (freedKB -928, +80, -556, the effectiveness latch engaged),
	// while the Spotify ENGINE's resident memory climbed 19.9 -> 22.4 MB
	// over the same three links. The retention is go-librespot's chunk
	// reader keeping every fetched chunk of the current track, which is why
	// a track change frees it. The seam is therefore not the fix and does
	// not run; the code and the knob stay because another firmware class
	// may behave the way the hypothesis assumed, and the seam log line
	// (memAvail before and after) is what would show it.
	chainPeriodDefault = 12 << 20
	chainPeriodBytes   = 0
	// chainMinBytes: never seam a link shorter than this (~100 s at 160
	// kbps). It is the floor under every reason and the point at which the
	// memory baseline (availAtStart) is taken; a box that is low for an
	// unrelated reason is kept off it by the attribution gate below, not by
	// this minimum.
	chainMinBytes = 2 << 20
	// chainLowWaterKB is the memory gate: 2x memGuardThresholdKB, 3x
	// memGuardCriticalKB (cmd/agent/sysmon.go). At the field leak rate
	// (~1.4 MB/min) that leaves ~4 min to the guard's warn line against a
	// worst-case seam latency of ~25 s probe + 2 s flush + 10 s lead. Normal
	// playlists never go below ~24 MB on the ST20 and the Portable, so the
	// gate is dormant there.
	chainLowWaterKB = 12 * 1024
	// chainMemFallDiv is the attribution half of the memory gate: since the
	// link's first probe MemAvailable must have fallen by linkBytes/2048 KB,
	// i.e. by at least 0.5 bytes per byte of THIS link, before its level may
	// end it. The field leak runs at 1.25 and clears that with room; a box
	// that is simply low for another reason never does (see cutHere).
	chainMemFallDiv = 2048
	// chainMemMinSec is the time floor under the memory reason: a link must
	// carry at least a minute of audio before a memory reading may end it, so
	// a high bitrate cannot turn chainMinBytes into a seam every ~52 s.
	chainMemMinSec = 60
	// chainProbeBytes is the /proc/meminfo cadence once a link is past
	// chainMinBytes: about two reads a minute at 160 kbps, none in the first
	// two MiB of a track. No timer, no NAND write.
	chainProbeBytes = 512 << 10
	// chainFallbackBytes is the period when MemAvailable cannot be read:
	// retains ~5 MB, what a normal 3.5-min song already retains on every box.
	chainFallbackBytes = 4 << 20
	// chainProbeAfter is how long the seam line waits for the box's free to
	// show: the box holds ~10 s of lead plus a 2 s batch before its decoder
	// reaches the seam.
	chainProbeAfter = 20 * time.Second
	// chainIneffective is how many consecutive seams freeing under 10 % of
	// their link may pass before seaming stops until the next real track
	// boundary (WARN + latch): a box low for another reason, or the engine
	// holding the memory instead of the firmware.
	chainIneffective = 3
	// chainOverridePath is the NAND knob / kill switch (see the file comment).
	chainOverridePath  = "/mnt/nv/streborn/spotify-chain-mb"
	chainOverrideMaxMB = 64
)

// oggChain cuts one go-librespot run's Ogg output into chained logical
// streams. Pure over []byte: its only I/O is the override file and the
// injected memory reader. Owned by the drain goroutine; nothing else touches
// it, the debug section reads the snapshot it publishes.
type oggChain struct {
	logger *slog.Logger
	// memAvail reads MemAvailable in KB; nil or a negative result = unknown
	// (tests, unreadable meminfo). engineRSS reads the engine's resident set
	// for the seam line; nil = unknown. publish hands every state change to
	// the Manager for /api/debug/state.
	memAvail  func() int64
	engineRSS func() int64
	publish   func(chainSnapshot)

	overridePath  string
	defaultPeriod int64 // the compiled ceiling; tests shrink it
	period        int64 // effective ceiling in bytes, 0 = off
	overridden    bool  // NAND value present: quieter logging after the first seam of a track
	// lastRead / lastOverridden remember the previous override read so the
	// override line is logged once per change, not once per track.
	lastRead       int64
	lastOverridden bool
	everRead       bool
	// Thresholds, initialised from the constants; fields so the tests can
	// exercise the trigger on synthetic tracks of a few KB.
	minBytes, probeBytes, fallbackBytes int64
	lowWaterKB                          int64
	probeAfter                          time.Duration
	ineffective                         int

	track    int   // current track number (the drain's trackNum)
	since    int64 // bytes forwarded in the current link
	probedAt int64 // since at the last memory probe
	avail    int64 // last memory reading, -1 unknown
	// availAtStart is the memory reading at the current link's FIRST probe,
	// -1 until one succeeds: the baseline the memory reason is judged
	// against. linkStartGran is the granule the current link started at (0 at
	// a real track boundary), pendingGran the granule of a cut page waiting
	// for its link to open.
	availAtStart     int64
	linkStartGran    int64
	pendingGran      int64
	seams            int  // seams in this track
	seamsTotal       int  // seams since the engine run started
	open             bool // stamping active: pages carry serial/seq below
	linkPending      bool // P went out with EOS, the new link's headers are still owed
	serial           uint32
	prevSerial       uint32
	seq              uint32
	cut              *chainCut // seam awaiting its proof line
	badRun           int
	badFreed         []int64
	latched          bool
	snap             chainSnapshot
	lastOverrideLine string
}

// chainCut is one seam between its cut and its proof line.
type chainCut struct {
	at          time.Time
	track, seam int
	linkBytes   int64
	reason      string
	memBeforeKB int64
	// The memory-reason evidence, so a field bundle can be judged without the
	// box: what MemAvailable was at the link's first probe, how far it fell
	// over the link, and how much audio the link carried.
	memAtStartKB int64
	fellKB       int64
	linkSec      int64
	gran         int64
	oldSerial    uint32
	newSerial    uint32 // 0 until the link opens; stays 0 when a real BOS came first
}

// chainSnapshot is the spotify_chain debug section, kept current by publish.
type chainSnapshot struct {
	Enabled         bool   `json:"enabled"`
	PeriodMB        int64  `json:"periodMB"`
	Overridden      bool   `json:"overridden"`
	SeamsTotal      int    `json:"seamsTotal"`
	LastSeamAt      string `json:"lastSeamAt,omitempty"`
	LastReason      string `json:"lastReason,omitempty"`
	LastLinkKB      int64  `json:"lastLinkKB"`
	LastMemBeforeKB int64  `json:"lastMemBeforeKB"`
	LastMemAfterKB  int64  `json:"lastMemAfterKB"`
	LastFreedKB     int64  `json:"lastFreedKB"`
	Latched         bool   `json:"latched"`
	LiveSerial      string `json:"liveSerial,omitempty"`
}

// newOggChain returns a chain cutter with the compiled thresholds and the
// override file read once.
func newOggChain(memAvail func() int64, logger *slog.Logger) *oggChain {
	c := &oggChain{
		logger:        logger,
		memAvail:      memAvail,
		overridePath:  chainOverridePath,
		defaultPeriod: chainPeriodBytes,
		minBytes:      chainMinBytes,
		probeBytes:    chainProbeBytes,
		fallbackBytes: chainFallbackBytes,
		lowWaterKB:    chainLowWaterKB,
		probeAfter:    chainProbeAfter,
		ineffective:   chainIneffective,
		avail:         -1,
		availAtStart:  -1,
		snap:          chainSnapshot{LastMemBeforeKB: -1, LastMemAfterKB: -1, LastFreedKB: -1},
	}
	c.loadOverride()
	return c
}

// loadOverride reads the NAND knob: absent or unparsable = the compiled
// ceiling, "0" = off, 1..64 = that many MiB. Logged at Info only when the
// value differs from the previous read, so a standing override costs one
// line per engine start, not one per track.
func (c *oggChain) loadOverride() {
	period, overridden := c.defaultPeriod, false
	if b, err := os.ReadFile(c.overridePath); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n >= 0 && n <= chainOverrideMaxMB {
			period, overridden = int64(n)<<20, true
		}
	}
	changed := !c.everRead || overridden != c.lastOverridden || period != c.lastRead
	c.everRead, c.lastOverridden, c.lastRead = true, overridden, period
	c.period, c.overridden = period, overridden
	if !changed {
		return
	}
	switch {
	case overridden && period == 0:
		c.logger.Info("spotify: chain seam period overridden", "mode", "off", "path", c.overridePath)
	case overridden:
		c.logger.Info("spotify: chain seam period overridden", "mb", period>>20, "path", c.overridePath)
	case c.lastOverrideLine != "":
		c.logger.Info("spotify: chain seam period override removed, back to the default", "mb", period>>20)
	}
	if overridden {
		c.lastOverrideLine = fmt.Sprint(period)
	} else {
		c.lastOverrideLine = ""
	}
}

// reset runs at a real track boundary (a BOS from the engine): a seam still
// waiting for its line gets it, stamping stops, a pending link is dropped,
// the per-track counters and the latch clear, and the knob is re-read.
func (c *oggChain) reset(now time.Time, track int) {
	c.probe(now, "bos")
	c.track = track
	c.open, c.linkPending = false, false
	c.since, c.probedAt = 0, 0
	// A new track starts at granule 0, so the link's audio clock does too,
	// and the memory baseline is re-taken at the new link's first probe.
	c.avail, c.availAtStart = -1, -1
	c.linkStartGran, c.pendingGran = 0, 0
	c.seams, c.badRun, c.latched = 0, 0, false
	c.badFreed = c.badFreed[:0]
	c.loadOverride()
	c.emit()
}

// cutHere decides whether page ends the current link, and if so flags it
// EOS in place and reseals it. hdrReady says the drain holds the current
// track's complete header set (nothing can be re-emitted without it).
//
// A page qualifies structurally when it completes at least one packet
// (granule > 0), is neither BOS nor already EOS, and its LAST packet
// completes on it (final lacing value < 255), so the next page starts a fresh
// packet with the continuation flag clear (RFC 3533 / Vorbis I A.2). A
// granule alone is not enough: a page can complete packets in its middle and
// still end with a 255-run.
//
// The memory reason is an ATTRIBUTION, not a level. The field leak
// (2026-09-07) is a box whose MemAvailable falls with the audio of ONE
// logical stream, ~1.25 bytes per byte. A box that merely sits low for an
// unrelated reason (the BoseApp leak family; the memory guard only reboots
// below 6 MiB and only when idle, so a box lives between 6 and 12 MiB for
// hours or days) would otherwise be seamed at chainMinBytes on every
// ordinary song: one seam per ~105 s at 160 kbps, ~52 s at 320, each an
// audible ~23 ms dropout plus one INFO line to the NAND log, and the
// effectiveness latch never engages because each seam does free its own
// link. So a memory-reason seam needs the leak signature on THIS link too:
// MemAvailable must have fallen by at least half a byte per byte of the
// link since its first probe, and the link must carry at least
// chainMemMinSec of audio. The audio clock is taken from the granule
// positions the pages already carry (linkStartGran), not from wall clock:
// granules are the box's own timeline, so a stalled, paced or detached
// stream cannot age a link that carried no audio, and no timer is added to
// a box that has to live on its NAND.
func (c *oggChain) cutHere(page []byte, hdrReady bool, now time.Time) bool {
	if c.period <= 0 || c.latched || c.linkPending || !hdrReady || len(page) < 27 {
		return false
	}
	gran := int64(binary.LittleEndian.Uint64(page[6:14]))
	if gran <= 0 || page[5]&0x06 != 0 || oggLastLacing(page) == 255 {
		return false
	}
	next := c.since + int64(len(page))
	if next < c.minBytes {
		return false
	}
	reason := ""
	switch {
	case next >= c.period:
		reason = "period"
	case c.memAvail != nil:
		// Probe on the first candidate page of a link and then once per
		// probeBytes. A link's first candidate is always probed afresh, so
		// a low reading from before the previous seam can never seam again
		// without the box having had its chance to free.
		//
		// The first reading of a link is kept as availAtStart, the baseline the
		// fall is measured against. It lands at chainMinBytes, deliberately
		// after the box has finished freeing the previous link (the free lags
		// the seam by the box's ~10 s lead plus the batch), so the baseline is a
		// settled reading; the price is that a leak at the field rate seams at
		// ~3.5 MiB rather than at the 2 MiB minimum.
		if c.probedAt == 0 || next-c.probedAt >= c.probeBytes {
			c.avail, c.probedAt = c.memAvail(), next
			if c.availAtStart < 0 {
				c.availAtStart = c.avail
			}
		}
		switch {
		case c.avail >= 0 && c.avail < c.lowWaterKB && c.memoryAttributable(next, gran):
			reason = "memory"
		case c.avail < 0 && next >= c.fallbackBytes:
			reason = "fallback"
		}
	default:
		if next >= c.fallbackBytes {
			reason = "fallback"
		}
	}
	if reason == "" {
		return false
	}
	// A seam still waiting for its line (only possible with tiny periods)
	// is written out before the next one is recorded.
	c.probe(now, "next-seam")
	page[5] |= 0x04
	oggReseal(page)
	before := int64(-1)
	if c.memAvail != nil {
		before = c.memAvail()
	}
	old := binary.LittleEndian.Uint32(page[14:18])
	if c.open {
		old = c.serial
	}
	c.linkPending = true
	fell := int64(-1)
	if c.availAtStart >= 0 && c.avail >= 0 {
		fell = c.availAtStart - c.avail
	}
	// This page ends the link, so the next link starts at its granule.
	c.pendingGran = gran
	c.cut = &chainCut{at: now, track: c.track, seam: c.seams + 1, linkBytes: next,
		reason: reason, memBeforeKB: before, memAtStartKB: c.availAtStart, fellKB: fell,
		linkSec: c.linkAudioSec(gran), gran: gran, oldSerial: old}
	c.snap.LastSeamAt = now.UTC().Format(time.RFC3339)
	c.snap.LastReason = reason
	c.snap.LastLinkKB = next / 1024
	c.snap.LastMemBeforeKB, c.snap.LastMemAfterKB, c.snap.LastFreedKB = before, -1, -1
	c.emit()
	return true
}

// memoryAttributable says whether the current memory reading may be blamed
// on the current link: MemAvailable has fallen by at least half a byte per
// byte of the link since the link's first probe, and the link carries at
// least chainMemMinSec of audio. Both are needed (field 2026-09-07): the
// fall is the leak signature that tells this link's own retention apart
// from a box that is merely low, the time floor keeps a high bitrate from
// turning the byte minimum into a seam every ~52 s.
func (c *oggChain) memoryAttributable(linkBytes, gran int64) bool {
	if c.availAtStart < 0 || c.availAtStart-c.avail < linkBytes/chainMemFallDiv {
		return false
	}
	return c.linkAudioSec(gran) >= chainMemMinSec
}

// linkAudioSec is the audio the current link carries at a page with granule
// gran, in seconds. Granules are absolute within a track and untouched by
// the chain, so the difference to the granule the link started at is the
// link's own playing time as the box will hear it.
func (c *oggChain) linkAudioSec(gran int64) int64 {
	if gran <= c.linkStartGran {
		return 0
	}
	return (gran - c.linkStartGran) / vorbisRate
}

// stamp counts page toward the current link and, once a link is open,
// rewrites its serial and page sequence in place (granule untouched).
func (c *oggChain) stamp(page []byte) {
	c.since += int64(len(page))
	if c.open && len(page) >= 27 {
		rewriteOggPageIDs(page, c.serial, c.seq)
		c.seq++
	}
}

// openLink returns a copy of the track's header pages re-stamped as the
// start of the next logical stream: a fresh serial (never the track's own,
// never the previous link's, never 0) and page sequence 0..h-1, flags and
// granules as captured. The three Vorbis header packets are stream-level
// configuration with no positional state (Vorbis I 4.2), so the same
// track's headers verbatim configure an identical decoder. A fresh serial
// is mandatory: libvorbisfile switches links only on a serial change and
// GStreamer's oggdemux opens a chain only for a BOS with an unknown serial;
// a same-serial BOS is fed into the existing stream and frees nothing.
//
// The copy is patched, never hdr itself: it shares its backing array with
// the Manager's headerPages and the NAND set keeps the original serial.
//
// Primer hook: a sample-exact seam would prepend the old link's last packet,
// re-laced, in front of the first page's body here. Not built (see the file
// comment).
func (c *oggChain) openLink(hdr []byte) []byte {
	var s1 uint32
	if len(hdr) >= 18 {
		s1 = binary.LittleEndian.Uint32(hdr[14:18])
	}
	wire := s1
	if c.open {
		wire = c.serial
	}
	c.prevSerial = wire
	c.serial = newSerial(s1, wire)
	link := append([]byte(nil), hdr...)
	var n uint32
	for off := 0; off < len(link); {
		l := oggPageLen(link[off:])
		if l <= 0 || off+l > len(link) {
			break
		}
		rewriteOggPageIDs(link[off:off+l], c.serial, n)
		n++
		off += l
	}
	c.seq = n
	c.open, c.linkPending = true, false
	c.since, c.probedAt = 0, 0
	// The new link's audio clock starts where the cut page ended, and its
	// memory baseline is taken afresh at its first probe.
	c.avail, c.availAtStart = -1, -1
	c.linkStartGran, c.pendingGran = c.pendingGran, 0
	c.seams++
	c.seamsTotal++
	if c.cut != nil {
		c.cut.newSerial = c.serial
	}
	c.loadOverride()
	c.emit()
	return link
}

// probe writes the seam line once the box has had chainProbeAfter to show
// its free (or at once when force names why it cannot wait: a real BOS, a
// detach, the engine exiting, the next seam). One Info line per seam, with
// the memory reading before AND after the cut, is the whole point: the
// five-minute resource sampler is what left the field forensics
// inconclusive. freedKB understates by the new link's own retention accrued
// during the wait (~0.5 MB at the field rate).
//
// The effectiveness latch counts consecutive unforced seams that freed under
// 10 % of their link; at chainIneffective it warns once and stops seaming
// until the next real track boundary, so a box that is low for another
// reason is not seamed into a storm, and a bundle from a box whose ENGINE
// holds the memory shows the verdict.
func (c *oggChain) probe(now time.Time, force string) {
	cut := c.cut
	if cut == nil {
		return
	}
	waited := now.Sub(cut.at)
	if force == "" && waited < c.probeAfter {
		return
	}
	c.cut = nil
	after := int64(-1)
	if c.memAvail != nil {
		after = c.memAvail()
	}
	engRSS := int64(-1)
	if c.engineRSS != nil {
		engRSS = c.engineRSS()
	}
	linkKB := cut.linkBytes / 1024
	known := cut.memBeforeKB >= 0 && after >= 0
	freed := int64(-1)
	if known {
		freed = after - cut.memBeforeKB
	}
	attrs := []any{
		"track", cut.track, "seam", cut.seam, "reason", cut.reason, "linkKB", linkKB,
		"linkSec", cut.linkSec, "granuleSec", cut.gran / vorbisRate,
		"oldSerial", fmt.Sprintf("%08x", cut.oldSerial), "newSerial", fmt.Sprintf("%08x", cut.newSerial),
		"memBeforeKB", cut.memBeforeKB, "memAfterKB", after,
		// The memory-reason evidence: where the link started and how far
		// MemAvailable fell over it. fellPerByte at or above 0.50 is what let a
		// memory seam through; the field leak runs at ~1.25.
		"availAtLinkStartKB", cut.memAtStartKB, "fellKB", cut.fellKB,
	}
	if cut.fellKB >= 0 && cut.linkBytes > 0 {
		attrs = append(attrs, "fellPerByte", fmt.Sprintf("%.2f", float64(cut.fellKB*1024)/float64(cut.linkBytes)))
	}
	if known {
		attrs = append(attrs, "freedKB", freed)
	}
	attrs = append(attrs, "probeSec", int(waited.Seconds()), "engineRSSKB", engRSS)
	if force != "" {
		attrs = append(attrs, "forcedBy", force)
	}
	if cut.newSerial == 0 {
		// A real track boundary followed the cut before the link opened: the
		// box saw a natural track end, no artificial stream was emitted.
		attrs = append(attrs, "deferredToBOS", true)
	}
	lvl := slog.LevelInfo
	if c.overridden && c.seams > 1 && known && freed*10 >= linkKB {
		// A standing override is a sweep (a 1 MiB period would otherwise fill
		// the 32 KB NAND ring in an hour): after the first effective seam of a
		// track the rest go to Debug.
		lvl = slog.LevelDebug
	}
	c.logger.Log(context.Background(), lvl, "spotify: chain seam, long track split so the box frees its stream buffer", attrs...)
	// Only an unforced probe of an opened link is a fair measurement: a line
	// forced by a real BOS or a detach was read before the box could react.
	if known && force == "" && cut.newSerial != 0 {
		if freed*10 < linkKB {
			c.badRun++
			c.badFreed = append(c.badFreed, freed)
			if len(c.badFreed) > c.ineffective {
				c.badFreed = c.badFreed[len(c.badFreed)-c.ineffective:]
			}
		} else {
			c.badRun = 0
			c.badFreed = c.badFreed[:0]
		}
		if c.badRun >= c.ineffective && !c.latched {
			c.latched = true
			c.logger.Warn("spotify: chain seams are not freeing memory on this box, leaving it to the memory guard",
				"freedKB", fmt.Sprint(c.badFreed), "linkKB", linkKB, "seams", c.seams,
				"memAvailKB", after, "engineRSSKB", engRSS)
		}
	}
	c.snap.LastMemAfterKB, c.snap.LastFreedKB = after, freed
	c.emit()
}

// emit refreshes the published snapshot.
func (c *oggChain) emit() {
	c.snap.Enabled = c.period > 0
	c.snap.PeriodMB = c.period >> 20
	c.snap.Overridden = c.overridden
	c.snap.SeamsTotal = c.seamsTotal
	c.snap.Latched = c.latched
	c.snap.LiveSerial = ""
	if c.open {
		c.snap.LiveSerial = fmt.Sprintf("%08x", c.serial)
	}
	if c.publish != nil {
		c.publish(c.snap)
	}
}

// snapshot returns the current debug view.
func (c *oggChain) snapshot() chainSnapshot {
	return c.snap
}

// oggLastLacing returns a page's final lacing value (0 when the page carries
// no segments). 255 means the page's last packet continues on the next page.
func oggLastLacing(p []byte) byte {
	if len(p) < 27 {
		return 0
	}
	n := int(p[26])
	if n == 0 || len(p) < 27+n {
		return 0
	}
	return p[26+n]
}

// oggPageLen returns the whole length of the page at the start of b (header,
// segment table, body), or 0 when b is too short to hold its header.
func oggPageLen(b []byte) int {
	if len(b) < 27 {
		return 0
	}
	n := int(b[26])
	if len(b) < 27+n {
		return 0
	}
	l := 27 + n
	for _, s := range b[27 : 27+n] {
		l += int(s)
	}
	return l
}

// rewriteOggPageIDs patches a page's serial and page sequence in place and
// reseals it. Granule, flags and body are untouched.
func rewriteOggPageIDs(p []byte, serial, seq uint32) {
	binary.LittleEndian.PutUint32(p[14:18], serial)
	binary.LittleEndian.PutUint32(p[18:22], seq)
	oggReseal(p)
}

// oggReseal recomputes a page's checksum after an in-place edit.
func oggReseal(p []byte) {
	binary.LittleEndian.PutUint32(p[22:26], oggPageCRC(p))
}

// newSerial returns a random non-zero serial that is none of avoid.
func newSerial(avoid ...uint32) uint32 {
	for {
		s := rand.Uint32()
		if s == 0 {
			continue
		}
		clash := false
		for _, a := range avoid {
			if s == a {
				clash = true
				break
			}
		}
		if !clash {
			return s
		}
	}
}
