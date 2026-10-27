package webui

// How long is an MP3 on a file server (#830)?
//
// A custom URL preset that is a file is handed to the speaker the library way,
// and the library way works best with a length: without one the firmware
// reports total=0 and treats the track as open-ended (upnp.TrackMeta). A Library
// track gets its length from the media server's metadata; a URL typed in by hand
// has none, so it is read from the file itself, from two small range reads:
//
//   - the head of the file, to skip an ID3v2 tag (cover art can make that
//     hundreds of KB, so the first frame may need a second read past it);
//   - the first MPEG audio frame, whose Xing/Info or VBRI header carries the
//     frame count of a VBR file, and whose bitrate times the size gives the
//     length of a CBR file.
//
// Any doubt returns 0 and the file plays without a length, exactly like a
// library track that has none.

import (
	"context"
	"encoding/binary"
	"net/http"
	"time"
)

const (
	// mp3HeadRead is the first read: an ID3 tag header plus, for a tag
	// without art, the first frames.
	mp3HeadRead = 16 * 1024
	// mp3FrameRead is the second read, at the end of a large tag.
	mp3FrameRead = 8 * 1024
	// mp3SyncScan is how far past the tag a first frame is looked for
	// (padding, a stray ID3v1-in-front, junk before the sync).
	mp3SyncScan = 4 * 1024
	// mp3LengthMax rejects an estimate no podcast or recording reaches.
	mp3LengthMax = 24 * time.Hour
)

// estimateMP3Length returns the play length of the MP3 at url whose size is
// size bytes, or 0 when it cannot be read with confidence.
func estimateMP3Length(ctx context.Context, cl *http.Client, url string, size int64) time.Duration {
	d, _ := estimateMP3LengthKind(ctx, cl, url, size)
	return d
}

// estimateMP3LengthKind is estimateMP3Length that also says whether the length
// is exact: counted from a Xing/Info or VBRI frame count. A length that is not
// exact is the bitrate of the first frame times the size, which is right for a
// CBR file and wrong for a VBR file that carries no such header.
func estimateMP3LengthKind(ctx context.Context, cl *http.Client, url string, size int64) (time.Duration, bool) {
	head := fetchRange(ctx, cl, url, 0, mp3HeadRead)
	if len(head) < 10 {
		return 0, false
	}
	audioStart := int64(id3v2Size(head))
	buf := head
	if audioStart > 0 {
		if audioStart+mp3SyncScan+4 <= int64(len(head)) {
			buf = head[audioStart:]
		} else {
			buf = fetchRange(ctx, cl, url, audioStart, mp3FrameRead)
		}
	}
	d, exact := mp3LengthFromFramesKind(buf, size-audioStart)
	if d < learnedLengthMin || d > mp3LengthMax {
		return 0, false
	}
	return d, exact
}

// id3v2Size returns the byte length of a leading ID3v2 tag (header, body and
// optional footer), or 0 when the data does not start with one. Unlike
// id3v2Len (announce_leadin.go) it does not need the whole tag in b: only the
// header is read, so a tag larger than the first read can be skipped.
func id3v2Size(b []byte) int {
	if len(b) < 10 || string(b[:3]) != "ID3" {
		return 0
	}
	// The size is "synchsafe": four 7-bit bytes.
	for _, c := range b[6:10] {
		if c&0x80 != 0 {
			return 0
		}
	}
	n := 10 + (int(b[6])<<21 | int(b[7])<<14 | int(b[8])<<7 | int(b[9]))
	if b[5]&0x10 != 0 {
		n += 10 // footer present
	}
	return n
}

// mp3LengthFromFrames finds the first audio frame in buf (which starts where
// the audio starts) and returns the play length of audioBytes of audio. The
// frame header parser is the announcement lead-in's (announce_leadin.go).
func mp3LengthFromFrames(buf []byte, audioBytes int64) time.Duration {
	d, _ := mp3LengthFromFramesKind(buf, audioBytes)
	return d
}

// mp3LengthFromFramesKind is mp3LengthFromFrames that also reports whether the
// length came from a frame count (exact) rather than from the bitrate.
func mp3LengthFromFramesKind(buf []byte, audioBytes int64) (time.Duration, bool) {
	for i := 0; i+4 <= len(buf) && i < mp3SyncScan; i++ {
		f, ok := parseMP3Frame(buf[i:])
		if !ok {
			continue
		}
		// A sync pattern inside other data is common; a real frame is followed
		// by another one exactly a frame length later. When the buffer ends
		// before that, the frame is taken on its own word.
		if next := i + f.length; next+4 <= len(buf) {
			if _, ok := parseMP3Frame(buf[next:]); !ok {
				continue
			}
		}
		bytes := audioBytes - int64(i)
		if frames, stated := mp3VBRFrames(buf[i:], f); frames > 0 {
			sec := float64(frames) * float64(f.samples()) / float64(f.sampleRate)
			d := time.Duration(sec * float64(time.Second))
			// A truncated file (an aborted copy or download) keeps the header of
			// the whole one, so its frame count claims audio that is not there.
			// When the header also states the stream's size and the file holds
			// clearly less, the count is scaled down to what is there and kept
			// only as a guess, so the missing tail is never waited out in silence.
			if stated > 0 && bytes > 0 && bytes*50 < int64(stated)*49 {
				return time.Duration(float64(d) * float64(bytes) / float64(stated)), false
			}
			return d, true
		}
		if bytes <= 0 || f.kbps <= 0 {
			return 0, false
		}
		return time.Duration(float64(bytes) * 8 / float64(f.kbps*1000) * float64(time.Second)), false
	}
	return 0, false
}

// mp3VBRFrames returns the frame count of a Xing/Info or VBRI header in the
// first frame, or 0 when it carries none, and the byte size of the audio
// stream the header states, or 0 when it states none.
func mp3VBRFrames(frame []byte, f mp3Frame) (frames, bytes uint32) {
	// The Xing/Info tag sits after the side information, whose size depends
	// on the version and the channel mode.
	if off := 4 + f.sideInfoLen(); off+12 <= len(frame) {
		if tag := string(frame[off : off+4]); tag == "Xing" || tag == "Info" {
			flags := binary.BigEndian.Uint32(frame[off+4:])
			if flags&0x01 == 0 {
				return 0, 0
			}
			frames = binary.BigEndian.Uint32(frame[off+8:])
			if flags&0x02 != 0 && off+16 <= len(frame) {
				bytes = binary.BigEndian.Uint32(frame[off+12:])
			}
			return frames, bytes
		}
	}
	// VBRI (Fraunhofer) always sits 32 bytes after the header: version,
	// delay and quality (2 bytes each), then the stream size and frame count.
	if off := 4 + 32; off+18 <= len(frame) && string(frame[off:off+4]) == "VBRI" {
		return binary.BigEndian.Uint32(frame[off+14:]), binary.BigEndian.Uint32(frame[off+10:])
	}
	return 0, 0
}
