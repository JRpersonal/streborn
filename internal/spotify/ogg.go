// ogg.go: the Ogg page plane — batching/pacing constants, page parsing
// with CRC verification and splice recovery, the forward path to the box
// sink, stall detection, and the skip-cut window.

package spotify

import (
	"bufio"
	"encoding/binary"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// vorbisRate is Spotify's Ogg/Vorbis sample rate; the Ogg granule position
// counts samples at this rate, which is how the drain turns bytes into a
// measured bitrate.
const vorbisRate = 44100

// flushThreshold batches Ogg pages before each write+flush to the box. The
// Bose firmware leaks memory in proportion to how many tiny HTTP chunks it
// receives, so bigger batches mean less leak. Live sweep (2026-06-05, per-value
// leak rate over a fresh boot):
//
//	  4 KB (per-page)  3.4 MB/min   stable
//	 16 KB            1.4 MB/min   stable
//	128 KB            0.6 MB/min   borderline
//	256 KB            0.38 MB/min  occasional underrun restart (~1 in 3 tracks)
//	512 KB            0.48 MB/min  frequent underrun restarts (no leak gain)
//
// The leak floors at ~0.4 MB/min (the irreducible live-streaming component);
// past ~256 KB there is no gain, only more underruns. Underruns happen because
// the single-goroutine drain blocks while writing a big batch and stops reading
// go-librespot, leaving a gap the box re-fetches over. 256 KB is the chosen
// operating point: lowest leak at the floor, with rare restarts Jens accepted
// in exchange. A lead/jitter buffer (decouple read from write) would remove the
// underruns and let it run here cleanly; tracked as a follow-up. Runtime
// override: /mnt/nv/streborn/spotify-flush-kb. Header replay is exempt.
const flushThreshold = 256 * 1024

// oggLeadCapSec is how many seconds of audio the box may hold ahead of
// realtime playback (see the pacing block in runOnce). Large enough to ride
// out Wi-Fi hiccups and give a fresh attachment an instant prefill, small
// enough that a track skip becomes audible in seconds rather than after the
// minutes of buffer the unpaced passthrough used to hand the box.
const oggLeadCapSec = 10

// maxFlushAge bounds how long a partial batch may sit before it is flushed to
// the box anyway. Under realtime pacing the size threshold alone would turn
// delivery into one 256 KB lump per ~11 s and let the box run dry in between.
const maxFlushAge = 2 * time.Second

// forward writes p to the box sink; on write error it drops the sink.
// StreamStalled reports whether a box is attached to the Ogg stream but has
// not been handed a single audio page for longer than stallAfter. That is the
// silent-stream failure the recall verify used to count as success: the box
// shows the playlist name and a spinner while nothing decodes, until the Bose
// transport gives up ~30 s later with AUDIO_ERROR_BAD_URL (field 2026-07-27).
// False when no box is attached at all.
func (m *Manager) StreamStalled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sink == nil || m.sinkAttachedAt.IsZero() {
		return false
	}
	if !m.sinkFirstAudioAt.IsZero() {
		return false // audio did flow at least once on this attachment
	}
	return time.Since(m.sinkAttachedAt) > stallAfter
}

// stallAfter is how long an attached box may wait for its first audio page
// before STR calls it a stall. Comfortably above a normal cold start (the box
// gets the cached headers immediately and audio within a second or two) and
// well below the box's own ~30 s transport timeout, so STR notices first.
const stallAfter = 6 * time.Second

// oggGranule reads an Ogg page's granule position (sample count so far). A
// value above zero marks a page that carries actual audio, as opposed to the
// identification/comment/setup header pages that open every logical stream.
// Returns 0 for anything too short to be a page.
func oggGranule(page []byte) int64 {
	if len(page) < 14 {
		return 0
	}
	return int64(binary.LittleEndian.Uint64(page[6:14]))
}

func (m *Manager) forward(sink io.Writer, p []byte) {
	if _, err := sink.Write(p); err != nil {
		m.mu.Lock()
		if m.sink == sink {
			m.sink = nil
		}
		m.mu.Unlock()
		return
	}
	m.mu.Lock()
	if m.sink == sink {
		m.sinkBytes += int64(len(p))
		m.sinkPages++
		now := time.Now()
		m.sinkLastPageAt = now
		// The first page carrying audio (as opposed to the header pages) is
		// the moment the box can actually start decoding.
		if m.sinkFirstAudioAt.IsZero() && oggGranule(p) > 0 {
			m.sinkFirstAudioAt = now
		}
	}
	m.mu.Unlock()
	if f, ok := sink.(http.Flusher); ok {
		f.Flush()
	}
}

// oggCRCTable implements the Ogg page checksum: CRC-32 with polynomial
// 0x04c11db7, initial value 0, no bit reversal, no final XOR (RFC 3533
// appendix A), computed over the whole page with the CRC field zeroed.
var oggCRCTable = func() (t [256]uint32) {
	for i := range t {
		r := uint32(i) << 24
		for k := 0; k < 8; k++ {
			if r&0x80000000 != 0 {
				r = (r << 1) ^ 0x04c11db7
			} else {
				r <<= 1
			}
		}
		t[i] = r
	}
	return
}()

// oggPageCRC computes the RFC 3533 checksum of a whole assembled page,
// treating the page's own CRC field (bytes 22..25) as zero.
func oggPageCRC(page []byte) uint32 {
	var crc uint32
	for i, b := range page {
		if i >= 22 && i < 26 {
			b = 0
		}
		crc = (crc << 8) ^ oggCRCTable[byte(crc>>24)^b]
	}
	return crc
}

// oggPageReader reads whole Ogg pages from the engine's output and VERIFIES
// each page's checksum. The verification is not pedantry: when go-librespot
// swaps passthrough sources mid-page (a user skip replaces the source at an
// arbitrary chunk offset), the truncated old page's declared body length
// swallows the NEXT track's BOS and header pages, and the drain then splices
// the new track's audio into the old logical stream: no boundary log, the
// skip-cut never disarms, and the box decodes a frankenstein stream
// (field-verified 2026-08-01). A swallowed page cannot checksum correctly,
// so on a CRC mismatch the reader pushes everything after the capture
// pattern back and rescans INSIDE those bytes for the real page start,
// recovering the swallowed BOS regardless of engine version.
type oggPageReader struct {
	r      *bufio.Reader
	push   []byte // bytes to re-serve before r (resync after a CRC mismatch)
	logger *slog.Logger
}

func newOggPageReader(r io.Reader, logger *slog.Logger) *oggPageReader {
	return &oggPageReader{r: bufio.NewReaderSize(r, 256*1024), logger: logger}
}

func (o *oggPageReader) readByte() (byte, error) {
	if len(o.push) > 0 {
		b := o.push[0]
		o.push = o.push[1:]
		return b, nil
	}
	return o.r.ReadByte()
}

func (o *oggPageReader) readFull(p []byte) error {
	n := copy(p, o.push)
	o.push = o.push[n:]
	if n == len(p) {
		return nil
	}
	_, err := io.ReadFull(o.r, p[n:])
	return err
}

// ReadPage returns the next checksum-valid Ogg page (27-byte header +
// segment table + body), resyncing past corrupt/spliced framing.
func (o *oggPageReader) ReadPage() ([]byte, error) {
	for {
		// Sync to the "OggS" capture pattern with a rolling window.
		var w [4]byte
		n := 0
		for {
			b, err := o.readByte()
			if err != nil {
				return nil, err
			}
			if n < 4 {
				w[n] = b
				n++
			} else {
				w[0], w[1], w[2], w[3] = w[1], w[2], w[3], b
			}
			if n == 4 && w[0] == 'O' && w[1] == 'g' && w[2] == 'g' && w[3] == 'S' {
				break
			}
		}
		// 23 bytes after "OggS": version, header_type, granule(8), serial(4),
		// page_seq(4), crc(4), page_segments(1).
		rest := make([]byte, 23)
		if err := o.readFull(rest); err != nil {
			return nil, err
		}
		numSegs := int(rest[22])
		segs := make([]byte, numSegs)
		if err := o.readFull(segs); err != nil {
			return nil, err
		}
		bodyLen := 0
		for _, s := range segs {
			bodyLen += int(s)
		}
		body := make([]byte, bodyLen)
		if err := o.readFull(body); err != nil {
			return nil, err
		}
		page := make([]byte, 0, 4+23+numSegs+bodyLen)
		page = append(page, 'O', 'g', 'g', 'S')
		page = append(page, rest...)
		page = append(page, segs...)
		page = append(page, body...)
		if oggPageCRC(page) == binary.LittleEndian.Uint32(page[22:26]) {
			return page, nil
		}
		// Spliced/corrupt page: rescan everything after the capture pattern.
		// The swallowed real page (usually the next track's BOS) begins
		// somewhere inside these bytes and will checksum correctly once the
		// scan lands on it. Each retry consumes at least the leading four
		// pattern bytes, so this terminates.
		if o.logger != nil {
			o.logger.Warn("spotify: dropped a page with a bad checksum, rescanning for the real page start (mid-page source handoff)", "pageBytes", len(page))
		}
		back := make([]byte, 0, len(page)-4+len(o.push))
		back = append(back, page[4:]...)
		back = append(back, o.push...)
		o.push = back
	}
}

// NoteSkip arms the skip-cut window: until the next track boundary (or the
// window's expiry) the forward loop drops the old track's audio instead of
// playing it out. Called by the webui skip worker right before
// /player/next|prev. Sized above the slowest observed engine track load
// (11 s, Portable 2026-08-01) so the boundary always lands inside it.
func (m *Manager) NoteSkip() {
	m.mu.Lock()
	m.skipCutUntil = time.Now().Add(15 * time.Second)
	m.mu.Unlock()
}

// skipCutArmed reports whether an armed user skip is awaiting its track
// boundary; clearSkipCut disarms it the moment that boundary arrives, so the
// new track's pages are never mistaken for stale ones.
func (m *Manager) skipCutArmed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return time.Now().Before(m.skipCutUntil)
}

func (m *Manager) clearSkipCut() {
	m.mu.Lock()
	m.skipCutUntil = time.Time{}
	m.mu.Unlock()
}
