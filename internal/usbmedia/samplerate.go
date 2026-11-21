package usbmedia

import (
	"bytes"
	"encoding/binary"
	"io"
	"path"
	"strings"
)

// The sample rate of a file, read from its header.
//
// Live on the Portable (FW 27.0.6, 2026-11-21): a FLAC at 24 bit / 96 kHz is
// answered with AUDIO_ERROR_DECODER, a 96 kHz WAV stays BUFFERING and never
// plays, while FLAC 16/44.1, AAC, Ogg, WAV 44.1 and ALAC play. The renderer's
// LPCM sinks stop at 48 kHz too (spike section 4). So a file above
// MaxSampleRate is not handed to the speaker: the caller says why instead.
//
// Only the two formats whose rate sits in a fixed place near the start are
// read: FLAC (STREAMINFO, the first metadata block, after an optional ID3v2
// tag) and WAV (the fmt chunk). M4A keeps its rate in the moov box, which may
// sit at the end of the file, so it is not read: an M4A is played as before.

// MaxSampleRate is the highest sample rate the speaker plays.
const MaxSampleRate = 48000

// headerReadLimit bounds the bytes a header read looks at. An ID3v2 tag with
// a cover picture can be larger; such a FLAC counts as unknown, and unknown
// plays as before.
const headerReadLimit = 256 << 10

// AudioFormat is what a header says.
type AudioFormat struct {
	SampleRate int
	// Bits is the bits per sample, 0 when the header does not say.
	Bits int
}

// TooHighForTheSpeaker reports whether the speaker cannot play this rate.
func (f AudioFormat) TooHighForTheSpeaker() bool { return f.SampleRate > MaxSampleRate }

// ReadAudioFormat reads the format of the file named name (only its extension
// matters) from r. ok is false for a format that is not read (M4A, MP3, ...)
// and for a header that cannot be parsed.
func ReadAudioFormat(r io.ReaderAt, name string) (AudioFormat, bool) {
	switch strings.ToLower(path.Ext(name)) {
	case ".flac":
		return flacFormat(r)
	case ".wav":
		return wavFormat(r)
	}
	return AudioFormat{}, false
}

func readAt(r io.ReaderAt, off int64, n int) ([]byte, bool) {
	if off < 0 || n <= 0 || off+int64(n) > headerReadLimit {
		return nil, false
	}
	b := make([]byte, n)
	got, err := r.ReadAt(b, off)
	if got < n && err != nil {
		return nil, false
	}
	return b, true
}

// flacFormat reads STREAMINFO: "fLaC", a 4-byte block header (type 0), then
// the 34-byte block whose bits 80..99 are the sample rate and 103..107 the
// bits per sample minus one.
func flacFormat(r io.ReaderAt) (AudioFormat, bool) {
	var off int64
	head, ok := readAt(r, 0, 10)
	if !ok {
		return AudioFormat{}, false
	}
	// An ID3v2 tag some taggers put in front: "ID3", version, flags, and a
	// syncsafe size (7 bits per byte), plus a 10-byte footer when flagged.
	if bytes.Equal(head[:3], []byte("ID3")) {
		size := int64(head[6]&0x7f)<<21 | int64(head[7]&0x7f)<<14 | int64(head[8]&0x7f)<<7 | int64(head[9]&0x7f)
		off = 10 + size
		if head[5]&0x10 != 0 {
			off += 10
		}
	}
	b, ok := readAt(r, off, 4+4+34)
	if !ok || !bytes.Equal(b[:4], []byte("fLaC")) || b[4]&0x7f != 0 {
		return AudioFormat{}, false
	}
	si := b[8:]
	rate := int(si[10])<<12 | int(si[11])<<4 | int(si[12])>>4
	bits := (int(si[12]&0x01)<<4 | int(si[13])>>4) + 1
	if rate == 0 {
		return AudioFormat{}, false
	}
	return AudioFormat{SampleRate: rate, Bits: bits}, true
}

// wavFormat walks the RIFF chunks to "fmt ": format tag, channels, sample
// rate (uint32), byte rate, block align, bits per sample.
func wavFormat(r io.ReaderAt) (AudioFormat, bool) {
	head, ok := readAt(r, 0, 12)
	if !ok || !bytes.Equal(head[:4], []byte("RIFF")) || !bytes.Equal(head[8:12], []byte("WAVE")) {
		return AudioFormat{}, false
	}
	off := int64(12)
	for i := 0; i < 64; i++ {
		ch, ok := readAt(r, off, 8)
		if !ok {
			return AudioFormat{}, false
		}
		size := int64(binary.LittleEndian.Uint32(ch[4:8]))
		if bytes.Equal(ch[:4], []byte("fmt ")) {
			if size < 16 {
				return AudioFormat{}, false
			}
			f, ok := readAt(r, off+8, 16)
			if !ok {
				return AudioFormat{}, false
			}
			rate := int(binary.LittleEndian.Uint32(f[4:8]))
			bits := int(binary.LittleEndian.Uint16(f[14:16]))
			if rate == 0 {
				return AudioFormat{}, false
			}
			return AudioFormat{SampleRate: rate, Bits: bits}, true
		}
		// Chunks are padded to an even size.
		off += 8 + size + size%2
	}
	return AudioFormat{}, false
}

// AudioFormat reads the format of the playable file p (an on-disk path or an
// object id, as for OpenFile). ok is false when the file cannot be opened or
// its format is not read.
func (l *Library) AudioFormat(p string) (AudioFormat, bool) {
	f, err := l.OpenFile(p)
	if err != nil {
		return AudioFormat{}, false
	}
	defer func() { _ = f.Close() }()
	return ReadAudioFormat(f, p)
}
