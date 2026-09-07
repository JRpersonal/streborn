// drain.go: one Ogg page from go-librespot to the box. The drain loop in
// runOnce hands every checksum-valid page to oggDrain.page, which keeps the
// current track's header set, measures the bitrate, applies the skip cut and
// the realtime pacing, batches pages into large writes and forwards them to
// the attached sink. Extracted from runOnce so the step can be tested through
// the real loop with a fake sink.

package spotify

import (
	"context"
	"encoding/binary"
	"os"
	"strconv"
	"time"
)

// oggDrain is the per-run state of the drain loop: everything that used to
// be a local of runOnce.
type oggDrain struct {
	m          *Manager
	flushBytes int
	leadCapSec float64
	chain      *oggChain

	// hdr accumulates the current track's header pages (BOS + the granule<=0
	// pages that follow); capturing is true between a BOS and the first audio
	// page. paused mirrors the "no consumer, engine paused" state.
	hdr       []byte
	capturing bool
	paused    bool
	// Bitrate measurement: body bytes and the highest granule (sample count at
	// vorbisRate) seen since the current track's BOS. kbps = bytes*8 over the
	// elapsed seconds. This is the real stream rate, not the configured nominal.
	trackBody, maxGran int64
	// pending batches pages so the box receives large chunks instead of one
	// tiny chunk per page (see flushThreshold: small chunks leak box memory).
	// pendingSince stamps the oldest byte in the batch, for the flush-age
	// bound.
	pending      []byte
	pendingSince time.Time
	// trackNum + forwarded count instrument the playback so the occasional
	// "track restarts at its start" can be diagnosed: track boundaries (new
	// BOS) and box (re)attaches are logged with byte/granule context.
	trackNum  int
	forwarded int64
	// Realtime pacing state (see leadCapSec in runOnce). Granules reset per
	// track, so granOffset accumulates finished tracks into one continuous
	// timeline; the anchor is set once per sink attachment at the first audio
	// page, so the lead cannot creep up by one cap per track boundary.
	granOffset, leadBaseGran int64
	leadBaseAt               time.Time
	leadAnchored             bool
}

// newOggDrain returns the drain state for one go-librespot run. flushBytes is
// the batch size before each write+flush to the box, leadCapSec bounds how far
// ahead of realtime the box is fed (0 disables pacing), chain is the seam
// cutter for long tracks (oggchain.go).
func (m *Manager) newOggDrain(flushBytes int, leadCapSec float64, chain *oggChain) *oggDrain {
	return &oggDrain{m: m, flushBytes: flushBytes, leadCapSec: leadCapSec, chain: chain, pendingSince: time.Now()}
}

// page processes one checksum-valid Ogg page from the engine.
func (d *oggDrain) page(ctx context.Context, page []byte) {
	m := d.m
	// Maintain the current track's header pages: a BOS page starts a
	// track (Vorbis identification header), the following granule<=0
	// pages carry comment/setup, the first audio page (granule>0) ends
	// the header sequence.
	htype := page[5]
	gran := int64(binary.LittleEndian.Uint64(page[6:14]))
	numSegs := int(page[26])
	bodyLen := int64(len(page) - 27 - numSegs)
	switch {
	case htype&0x02 != 0: // BOS
		// New logical stream = track boundary. Log it with the previous
		// track's size so a premature/duplicate BOS (the suspected cause of
		// a track restarting at its start) is visible in the log.
		m.logger.Info("spotify: track boundary (BOS)",
			"track", d.trackNum+1, "prevTrackKB", d.trackBody/1024,
			"prevMaxGran", d.maxGran, "forwardedKB", d.forwarded/1024)
		// App-skip detector: a boundary that cuts the previous track
		// clearly short, with no STR skip or recall cut armed, came from
		// the Spotify app itself; re-point the box so it drops the old
		// track's buffered tail (see durQueueMs in Manager).
		m.noteTrackBoundaryCut(d.maxGran, d.trackBody)
		// A fresh logical stream means the engine is demonstrably
		// delivering audio: that is the recovery signal the delayed
		// auto-advance checks before skipping (see handleEnginePlaybackEnd).
		m.mu.Lock()
		m.lastEngineActiveAt = time.Now()
		m.mu.Unlock()
		d.trackNum++
		d.granOffset += d.maxGran // finished track extends the continuous timeline
		d.hdr = append([]byte(nil), page...)
		d.capturing = true
		d.trackBody, d.maxGran = 0, 0
		// A real track boundary ends any artificial chain link: the new
		// track goes out under its own serial, unstamped, and a seam that
		// was cut but not yet opened is simply dropped (the box then sees a
		// natural track end). Runs before the sink block below so the BOS
		// page itself is never stamped.
		d.chain.reset(time.Now(), d.trackNum)
	case d.capturing && gran > 0: // first audio page
		m.mu.Lock()
		m.headerPages = d.hdr
		persist := !m.hdrPersisted && m.hdrPath != ""
		if persist {
			m.hdrPersisted = true
		}
		hdrKbps := m.bitr
		m.mu.Unlock()
		d.capturing = false
		if persist {
			// Persist one valid header set to NAND for the next cold boot.
			// Once only (guarded above), so no per-track flash wear.
			if err := os.WriteFile(m.hdrPath, d.hdr, 0o644); err != nil {
				m.logger.Debug("spotify: persist stream headers failed", "err", err)
			}
			// The set is only valid for the bitrate it was captured at
			// (Vorbis codebooks differ per profile; a mismatched replay
			// decodes to noise). The marker lets the next boot reject a
			// stale set after a quality change.
			if err := os.WriteFile(m.hdrPath+".kbps", []byte(strconv.Itoa(hdrKbps)), 0o644); err != nil {
				m.logger.Debug("spotify: persist header bitrate marker failed", "err", err)
			}
		}
	case d.capturing:
		d.hdr = append(d.hdr, page...)
	}
	d.trackBody += bodyLen
	if gran > d.maxGran {
		d.maxGran = gran
	}
	if d.maxGran > vorbisRate { // at least one second streamed
		kbps := int(d.trackBody * 8 * vorbisRate / (d.maxGran * 1000))
		m.mu.Lock()
		m.actualKbps = kbps
		m.mu.Unlock()
	}

	m.mu.Lock()
	sink := m.sink
	haveHdr := len(m.headerPages) > 0
	m.mu.Unlock()

	if sink != nil {
		d.paused = false
		// Track-boundary flush: a new BOS is a new logical Vorbis stream
		// (the box must reload codebooks). If the BOS is buried mid-batch
		// behind the previous track's tail, the box re-inits on a partial
		// chunk and the new track audibly restarts (live-observed, ~1 in 3
		// tracks). Flushing the tail first makes the BOS begin on a clean
		// chunk boundary so the decoder re-inits cleanly.
		//
		// Skip cut: when this boundary was CAUSED by a user skip, the old
		// track's unsent tail is noise the user asked to get away from, so
		// it is dropped instead of flushed and the new track starts that
		// much sooner. A natural track end never has an armed skip window,
		// so song endings are never clipped. The cut disarms here and the
		// pacing re-anchors, so the new track gets its instant prefill.
		if htype&0x02 != 0 {
			if m.skipCutArmed() {
				m.logger.Info("spotify: skip cut, boundary reached; dropped the old track's unsent tail", "droppedKB", len(d.pending)/1024)
				d.pending = d.pending[:0]
				m.clearSkipCut()
				m.noteSkipBoundary()
				d.leadAnchored = false
			} else if len(d.pending) > 0 {
				m.forward(sink, d.pending)
				d.pending = d.pending[:0]
			}
		} else if m.skipCutArmed() && gran > 0 {
			// Stale audio between the user's skip and the new track's
			// boundary. The first version PACED these pages to realtime, so
			// the boundary reached the box up to a full lead cap late and
			// the skip felt >20 s (Jens, live 2026-08-01 00:30). They carry
			// nothing the user wants to hear: drop them outright and race
			// to the boundary.
			return
		}
		// Realtime pacing: anchor once per attachment at the first audio
		// page, then hold each page until its position on the continuous
		// timeline is within leadCapSec of wall clock. A detach mid-wait
		// (box paused or dropped the stream) bails out immediately.
		if gran > 0 && d.leadCapSec > 0 {
			timeline := d.granOffset + gran
			if !d.leadAnchored {
				d.leadBaseGran, d.leadBaseAt, d.leadAnchored = timeline, time.Now(), true
			}
			for {
				ahead := float64(timeline-d.leadBaseGran)/float64(vorbisRate) - time.Since(d.leadBaseAt).Seconds()
				if ahead <= d.leadCapSec || ctx.Err() != nil {
					break
				}
				time.Sleep(min(time.Duration((ahead-d.leadCapSec)*float64(time.Second)), 250*time.Millisecond))
				m.mu.Lock()
				stillAttached := m.sink == sink
				m.mu.Unlock()
				if !stillAttached {
					break
				}
			}
		}
		now := time.Now()
		// Chain seam, second half: the page before this one was sent with
		// EOS (cutHere below), so this page opens the next logical stream.
		// The track's own header pages go out again under a fresh serial
		// right in front of it, and the late-joiner header set follows
		// suit, so a box that re-attaches mid-link gets headers carrying
		// the serial the live pages carry. Deferred to here on purpose: had
		// a real BOS followed the cut instead, reset() above dropped the
		// pending link and no header-only stream ever reaches the box.
		if d.chain.linkPending {
			link := d.chain.openLink(d.hdr)
			m.mu.Lock()
			m.headerPages = link
			m.sinkSeams++
			m.mu.Unlock()
			d.pending = append(d.pending, link...)
			d.pendingSince = now
			d.forwarded += int64(len(link))
			if page[5]&0x01 != 0 {
				m.logger.Warn("spotify: chain seam opened on a continued page (malformed stream), decoder will drop one packet", "flags", page[5])
			}
		}
		// Chain seam, first half: on a long track, end the current logical
		// stream on this page (EOS flag) so the box frees the buffer it
		// holds per logical stream (oggchain.go). The page is then stamped
		// with the live link's serial and sequence like every other page.
		seam := d.chain.cutHere(page, !d.capturing && len(d.hdr) > 0, now)
		d.chain.stamp(page)
		// Batch pages into large writes (see flushThreshold) so the box
		// gets large chunks, not a tiny chunk per page. With the realtime
		// pacing above, the size threshold alone is a trap: at ~187 kbps it
		// takes ~11 s to fill 256 KB, so the box received its audio as one
		// lump per ~11 s, ran dry in between, and a skip landing in the gap
		// starved it into detaching (live 2026-08-01 01:13, followed by a
		// recovery recall that read as a double skip). A flush-age bound
		// keeps the flow continuous; the chunks stay far above the
		// per-page writes the size threshold exists to prevent.
		if len(d.pending) == 0 {
			d.pendingSince = now
		}
		d.pending = append(d.pending, page...)
		d.forwarded += int64(len(page))
		// A seam flushes at once: the old link ends on a clean chunk, the
		// same rule as the tail flush before a real BOS above.
		if seam || len(d.pending) >= d.flushBytes || time.Since(d.pendingSince) > maxFlushAge {
			m.forward(sink, d.pending)
			d.pending = d.pending[:0]
		}
		d.chain.probe(now, "")
		return
	}
	d.leadAnchored = false // no consumer: next attachment re-anchors fresh
	// No consumer: drop any half-filled batch so a freshly attaching box
	// starts clean, then once a track's headers are captured pause
	// go-librespot so it stops producing (no racing) until a box attaches
	// and ServeOgg resumes it.
	d.pending = d.pending[:0]
	// A seam still waiting for its proof line gets it now: the box is gone,
	// so its memory reading will not move for this link any more. The link
	// byte count is deliberately NOT reset, an HTTP re-attach frees nothing
	// on the box (field 2026-09-07, Phase A).
	d.chain.probe(time.Now(), "detach")
	// During a recall keep the engine playing even with no sink: a hardware
	// preset press makes the box flap its source (1036 INVALID_SOURCE) and
	// drop the sink repeatedly before it settles, and pausing here stranded
	// the engine so the settled box never got audio. engineHot() covers the
	// recall + verify window; outside it, pause as before so an idle box does
	// not keep go-librespot decoding to nothing.
	if !d.paused && haveHdr && !m.engineHot() {
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_ = m.Pause(pctx)
		cancel()
		d.paused = true
	}
}

// finish runs when the engine's output ends: a seam still waiting for its
// proof line is written with what the box shows now.
func (d *oggDrain) finish() {
	d.chain.probe(time.Now(), "engine-exit")
}
