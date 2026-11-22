package webui

// The length of a song on a USB stick STR serves (#1136).
//
// The stick's listing reads no file, so its DIDL carries no duration, and the
// clients send none when they play a stick file. Without a duration the
// speaker reports a total time of zero (upnp.TrackMeta: measured on a Portable,
// `duration` alone turned total=0 into total=18), so the app and the phone page
// drew no progress bar for a stick song while a NAS song had one (discussion
// #1136, ST20, 2026-11-22), and a folder ended each track on the frozen-
// position net instead of on time.
//
// So the agent reads the length from the file's own header when it hands a
// stick file to the speaker without one: a single play, every track of a
// folder as it is pushed, and a song key. The read is a HEAD and a few small
// range reads (readFileLengthKind, plus WAV and Ogg Vorbis here), cached per
// URL and size in fileLens, and only an exact length is used: a FLAC
// STREAMINFO, an MP4 movie header, an MP3 Xing/Info or VBRI frame count, a WAV
// data chunk, the last Ogg granule. Nothing else on the stick is read, and
// nothing is read while browsing, so the listing stays as cheap as before.
//
// This speaker's own stick is read over loopback. Another STR speaker's stick
// (a song from the stick in the kitchen played on the living room speaker) is
// read from that speaker by its LAN address, the same URL the speaker fetches
// the song from.

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/usbmedia"
)

const (
	// stickLengthBudget bounds one length read. Loopback answers in
	// milliseconds; another speaker's stick a little slower.
	stickLengthBudget = 4 * time.Second
	// wavHeadRead holds the RIFF header, the fmt chunk and the header of the
	// data chunk, past a LIST chunk of tags.
	wavHeadRead = 8 * 1024
	// oggHeadRead holds the first Ogg page with the Vorbis identification
	// header.
	oggHeadRead = 4 * 1024
	// oggTailRead is the end of the file the last page is looked for in. An
	// Ogg page is at most about 64 KB.
	oggTailRead = 64 * 1024
)

// stickFileURL reports whether raw is a song file on a USB stick an STR agent
// serves, and whether that is this speaker's own stick (the loopback form,
// usbLoopbackURL) or another STR speaker's, at its LAN address on the agent's
// port (:8888 or :17008).
func stickFileURL(raw string) (own, ok bool) {
	if isUSBLoopbackFile(raw) {
		return true, true
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || !strings.HasPrefix(u.Path, usbmedia.FilePrefix) ||
		len(u.Path) == len(usbmedia.FilePrefix) {
		return false, false
	}
	switch u.Port() {
	case "8888", "17008":
	default:
		return false, false
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsPrivate() {
		return false, false
	}
	return false, true
}

// stickLengthClient is the client a stick file's length is read with: a plain
// one for this speaker's own stick (the guarded client refuses loopback), the
// guarded one, which lets the LAN through, for another speaker's. A seam for
// the tests, whose "other speaker" is a loopback test server.
var stickLengthClient = func(own bool) *http.Client {
	if own {
		return usbProbeClient()
	}
	return customFileProbeClient()
}

// stickFileLength returns the exact play length of the stick file at raw, 0
// for any other URL and when no exact length can be read.
func (s *Server) stickFileLength(ctx context.Context, raw, mime string) time.Duration {
	own, ok := stickFileURL(raw)
	if !ok || mime == "" {
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, stickLengthBudget)
	defer cancel()
	start := time.Now()
	fl, readNow, reachable := currentFileLengthWith(ctx, stickLengthClient(own), &s.fileLens, raw, mime, readStickFileLengthKind)
	if readNow || !reachable {
		s.logger.Info("usb music: read the song's length from the file", "url", raw, "own", own,
			"reachable", reachable, "lengthSec", int(fl.length.Seconds()), "exact", fl.exact,
			"checkMs", time.Since(start).Milliseconds())
	}
	if !reachable || !fl.exact || fl.length <= 0 {
		return 0
	}
	return fl.length
}

// withStickLength gives a queue track on a USB stick that has no length the
// one read from its file, in the item and in every queue entry of the same
// URL, so the speaker is handed it and the watcher times the end by it.
func (s *Server) withStickLength(ctx context.Context, it queueItem) queueItem {
	if it.Duration > 0 || it.Mime == "" {
		return it
	}
	if _, ok := stickFileURL(it.URL); !ok {
		return it
	}
	if d := s.stickFileLength(ctx, it.URL, it.Mime); d > 0 {
		it.Duration = d
		if s.queue != nil {
			s.queue.setLength(it.URL, d)
		}
	}
	return it
}

// readStickFileLengthKind is readFileLengthKind with WAV and Ogg Vorbis read
// too. Those two stay out of readFileLengthKind on purpose: a length there
// moves a custom URL preset of that type from the stream proxy to the direct
// path, which is a separate decision from a progress bar.
func readStickFileLengthKind(ctx context.Context, cl *http.Client, url, mime string, size int64) (time.Duration, bool) {
	var d time.Duration
	switch mime {
	case "audio/wav":
		d = wavLengthFrom(fetchRange(ctx, cl, url, 0, wavHeadRead), size)
	case "audio/ogg":
		d = oggLength(ctx, cl, url, size)
	default:
		return readFileLengthKind(ctx, cl, url, mime, size)
	}
	if d < learnedLengthMin || d > mp3LengthMax {
		return 0, false
	}
	return d, true
}

// wavLengthFrom reads the byte rate from the fmt chunk and the size of the
// data chunk from head, the start of a WAV file of size bytes. A data chunk
// whose size is unset (0 or 0xFFFFFFFF, written by a recorder that streamed)
// or larger than the file runs to the end of the file.
func wavLengthFrom(head []byte, size int64) time.Duration {
	if len(head) < 12 || !bytes.Equal(head[:4], []byte("RIFF")) || !bytes.Equal(head[8:12], []byte("WAVE")) {
		return 0
	}
	var byteRate int64
	for off := 12; off+8 <= len(head); {
		id := head[off : off+4]
		n := int64(binary.LittleEndian.Uint32(head[off+4 : off+8]))
		body := off + 8
		switch {
		case bytes.Equal(id, []byte("fmt ")):
			if n < 16 || body+16 > len(head) {
				return 0
			}
			byteRate = int64(binary.LittleEndian.Uint32(head[body+8 : body+12]))
		case bytes.Equal(id, []byte("data")):
			if byteRate <= 0 {
				return 0
			}
			left := size - int64(body)
			if n == 0 || n == 0xFFFFFFFF || n > left {
				n = left
			}
			if n <= 0 {
				return 0
			}
			return time.Duration(float64(n) / float64(byteRate) * float64(time.Second))
		}
		// Chunks are padded to an even size.
		next := int64(body) + n + n%2
		if next > int64(len(head)) {
			return 0
		}
		off = int(next)
	}
	return 0
}

// oggLength is the last granule position of an Ogg Vorbis file divided by its
// sample rate: the first page carries the Vorbis identification header with
// the rate, the last page the granule, the number of samples up to its end.
func oggLength(ctx context.Context, cl *http.Client, url string, size int64) time.Duration {
	rate := vorbisRate(fetchRange(ctx, cl, url, 0, oggHeadRead))
	if rate <= 0 || size <= 0 {
		return 0
	}
	from := size - oggTailRead
	if from < 0 {
		from = 0
	}
	g := lastOggGranule(fetchRange(ctx, cl, url, from, size-from))
	if g <= 0 {
		return 0
	}
	return time.Duration(float64(g) / float64(rate) * float64(time.Second))
}

// vorbisRate reads the sample rate from the Vorbis identification header in
// the first Ogg page, 0 for anything else (an Opus or FLAC stream in Ogg).
func vorbisRate(head []byte) int64 {
	if len(head) < 27 || !bytes.Equal(head[:4], []byte("OggS")) || head[4] != 0 {
		return 0
	}
	p := 27 + int(head[26]) // the segment table follows the page header
	if p+16 > len(head) || head[p] != 1 || !bytes.Equal(head[p+1:p+7], []byte("vorbis")) {
		return 0
	}
	return int64(binary.LittleEndian.Uint32(head[p+12 : p+16]))
}

// lastOggGranule returns the granule position of the last Ogg page that
// starts in tail and has one, 0 when none does.
func lastOggGranule(tail []byte) int64 {
	for i := len(tail) - 27; i >= 0; i-- {
		if tail[i] != 'O' || !bytes.Equal(tail[i:i+4], []byte("OggS")) || tail[i+4] != 0 || tail[i+5]&^0x07 != 0 {
			continue
		}
		// -1 is "no packet ends on this page".
		if g := int64(binary.LittleEndian.Uint64(tail[i+6 : i+14])); g > 0 {
			return g
		}
	}
	return 0
}
