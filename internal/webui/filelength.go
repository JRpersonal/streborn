package webui

// How long is a custom URL file that is not an MP3 (#830)?
//
// A custom URL preset that is a file goes to the speaker directly only when
// its length is known (customFileMime). Without one the speaker is handed an
// open-ended item: no duration, no DLNA.ORG_OP=01, so it may only read the
// file front to back. That gains nothing over the stream proxy (there is no
// position to come back to after a pause either way) and it broke an m4a: an
// ST10 (FW 27.0.6) on a 56 min AAC-LC m4a from nginx played a few seconds,
// dropped the file, and started over, again and again, while the same preset
// had played through the proxy without a fault up to v1.0.18.
//
// So the length is read from the file itself, the way mp3length.go does for
// an MP3, with a few small range reads:
//
//   - MP4/M4A: the duration and timescale in the movie header (moov/mvhd).
//     A "faststart" file has moov at the top; otherwise the top-level boxes
//     are walked by their sizes, one small read per box, to the moov after
//     the audio.
//   - FLAC: the sample count and sample rate in STREAMINFO, the first
//     metadata block.
//
// Any doubt returns 0, and the preset stays on the proxy.

import (
	"context"
	"encoding/binary"
	"math"
	"net/http"
	"time"
)

const (
	// mp4HeadRead is the first read of an MP4: ftyp, and for a faststart
	// file the moov header with its mvhd.
	mp4HeadRead = 16 * 1024
	// mp4BoxRead is each further read while walking to a moov after the
	// audio: a box header, or the head of the moov.
	mp4BoxRead = 4 * 1024
	// mp4MaxHops bounds the walk: ftyp, free/wide, mdat, moov is the usual
	// shape; a dozen top-level boxes is already unusual.
	mp4MaxHops = 12
	// flacHeadRead holds the "fLaC" marker and STREAMINFO (4 + 4 + 34 bytes).
	flacHeadRead = 64
)

// readFileLength returns the play length of the file at url (size bytes,
// served in ranges) for the formats STR can read one from, else 0.
func readFileLength(ctx context.Context, cl *http.Client, url, mime string, size int64) time.Duration {
	d, _ := readFileLengthKind(ctx, cl, url, mime, size)
	return d
}

// readFileLengthKind is readFileLength that also says whether the length is
// exact: read from the file's own header (an MP4 movie header, a FLAC
// STREAMINFO, an MP3 Xing/Info or VBRI frame count). An MP3 without a frame
// count is only its first frame's bitrate times the size, which is right for a
// CBR file and wrong for a VBR one.
func readFileLengthKind(ctx context.Context, cl *http.Client, url, mime string, size int64) (time.Duration, bool) {
	var d time.Duration
	switch mime {
	case "audio/mpeg":
		return estimateMP3LengthKind(ctx, cl, url, size)
	case "audio/mp4":
		d = mp4Length(ctx, cl, url, size)
	case "audio/flac":
		d = flacLengthFrom(fetchRange(ctx, cl, url, 0, flacHeadRead))
	}
	if d < learnedLengthMin || d > mp3LengthMax {
		return 0, false
	}
	return d, true
}

// mp4Length walks the top-level boxes of the MP4 at url to its moov and reads
// the duration from the mvhd in it.
func mp4Length(ctx context.Context, cl *http.Client, url string, size int64) time.Duration {
	buf := fetchRange(ctx, cl, url, 0, mp4HeadRead)
	var bufAt int64 // file offset of buf[0]
	var off int64
	for hop := 0; hop < mp4MaxHops && off+8 <= size; hop++ {
		// Make sure the box header (and, for moov, the head of the box) is
		// in the buffer; one small read at the box otherwise.
		if off < bufAt || off+16 > bufAt+int64(len(buf)) {
			buf, bufAt = fetchRange(ctx, cl, url, off, mp4BoxRead), off
			if len(buf) < 8 {
				return 0
			}
		}
		b := buf[off-bufAt:]
		boxSize, typ, hdr, ok := mp4BoxHeader(b, size-off)
		if !ok {
			return 0
		}
		if typ == "moov" {
			if int64(len(b)) < hdr+8 {
				b = fetchRange(ctx, cl, url, off, mp4BoxRead)
			}
			end := boxSize
			if end > int64(len(b)) {
				end = int64(len(b))
			}
			if end <= hdr {
				return 0
			}
			return mp4MvhdLength(b[hdr:end])
		}
		off += boxSize
	}
	return 0
}

// mp4BoxHeader parses the box header at the start of b. remain is how many
// bytes of the file are left from there, for a box whose size is "to the end".
// It returns the whole box size, its type and the header length.
func mp4BoxHeader(b []byte, remain int64) (size int64, typ string, hdr int64, ok bool) {
	if len(b) < 8 {
		return 0, "", 0, false
	}
	size = int64(binary.BigEndian.Uint32(b))
	typ = string(b[4:8])
	hdr = 8
	switch size {
	case 0:
		size = remain
	case 1:
		if len(b) < 16 {
			return 0, "", 0, false
		}
		size = int64(binary.BigEndian.Uint64(b[8:]))
		hdr = 16
	}
	if size < hdr || size > remain {
		return 0, "", 0, false
	}
	for i := 0; i < 4; i++ {
		if c := typ[i]; c < 0x20 || c > 0x7E {
			return 0, "", 0, false
		}
	}
	return size, typ, hdr, true
}

// mp4MvhdLength finds the mvhd among the children of a moov (body is the
// moov's content, possibly cut short) and returns duration / timescale.
func mp4MvhdLength(body []byte) time.Duration {
	for off := 0; off+8 <= len(body); {
		// The body may be cut short, so a child is not held to what was read
		// (a trak is far larger than one read); only the mvhd must be in it.
		size, typ, hdr, ok := mp4BoxHeader(body[off:], math.MaxInt64)
		if !ok {
			return 0
		}
		if typ == "mvhd" {
			return mvhdLength(body[off+int(hdr):])
		}
		if size > int64(len(body)-off) {
			return 0 // the next child lies past what was read
		}
		off += int(size)
	}
	return 0
}

// mvhdLength reads a movie header's timescale and duration (version 0: 32-bit
// times, version 1: 64-bit).
func mvhdLength(b []byte) time.Duration {
	if len(b) < 4 {
		return 0
	}
	var scale, dur uint64
	switch b[0] {
	case 0:
		if len(b) < 20 {
			return 0
		}
		scale = uint64(binary.BigEndian.Uint32(b[12:]))
		dur = uint64(binary.BigEndian.Uint32(b[16:]))
		if dur == 0xFFFFFFFF {
			return 0 // "unknown"
		}
	case 1:
		if len(b) < 32 {
			return 0
		}
		scale = uint64(binary.BigEndian.Uint32(b[20:]))
		dur = binary.BigEndian.Uint64(b[24:])
	default:
		return 0
	}
	if scale == 0 || dur == 0 {
		return 0
	}
	sec := float64(dur) / float64(scale)
	return time.Duration(sec * float64(time.Second))
}

// flacLengthFrom reads total samples / sample rate from the STREAMINFO block
// that follows the "fLaC" marker at the start of head.
func flacLengthFrom(head []byte) time.Duration {
	if len(head) < 8+34 || string(head[:4]) != "fLaC" || head[4]&0x7F != 0 {
		return 0
	}
	si := head[8:]
	// Bytes 10..17: 20 bits sample rate, 3 bits channels-1, 5 bits
	// bits-per-sample-1, 36 bits total samples.
	v := binary.BigEndian.Uint64(si[10:18])
	rate := v >> 44
	total := v & (1<<36 - 1)
	if rate == 0 || total == 0 {
		return 0
	}
	return time.Duration(float64(total) / float64(rate) * float64(time.Second))
}
