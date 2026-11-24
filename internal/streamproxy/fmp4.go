package streamproxy

// HLS stage 3 (#124): fragmented MP4 (fMP4/CMAF) segments to a stream the
// speaker can play.
//
// The SoundTouch renderer cannot decode fragmented MP4 at all, not even the
// init segment and the fragments joined into one .m4a: tested on a Portable
// on 2026-11-23, the decoder answered "Could not obtain first-frame" and
// ERROR_DECODE (docs/streaming/soundcloud.md, "Results on a real speaker").
// So the proxy unpacks the audio samples and re-serves them in a container
// the speaker does take, the way stage 2 re-serves MPEG-TS:
//
//   - AAC (mp4a, object type 0x40): every sample gets a 7-byte ADTS header
//     built from the AudioSpecificConfig in the init segment.
//   - MP3 in MP4 (object type 0x6B/0x69): MP3 frames carry their own sync,
//     so the samples pass through as they are.
//   - Anything else (encrypted "enca", Opus, FLAC, AC-3) is refused, so the
//     caller reports the stream as not playable instead of sending noise.
//
// Everything here works on bytes already in memory and checks every length
// against the buffer: the input comes from the internet.

import (
	"encoding/binary"
	"errors"
)

// fmp4Codec says what the samples of the audio track are.
type fmp4Codec int

const (
	fmp4AAC fmp4Codec = iota + 1
	fmp4MP3
)

// fmp4Track is what the converter needs from an init segment.
type fmp4Track struct {
	trackID uint32
	codec   fmp4Codec
	// ADTS fields, from the AudioSpecificConfig (AAC only).
	profile  byte // ADTS profile = audio object type - 1 (0..3)
	freqIdx  byte // sampling frequency index (0..12)
	channels byte // channel configuration (1..7)
	// Defaults from mvex/trex, used when a fragment does not say.
	defaultSampleSize uint32
}

var (
	errFMP4NoAudio     = errors.New("fmp4: no audio track in the init segment")
	errFMP4Unsupported = errors.New("fmp4: audio codec not supported (only AAC and MP3)")
	errFMP4Encrypted   = errors.New("fmp4: the audio track is encrypted")
	errFMP4Malformed   = errors.New("fmp4: malformed box structure")
)

// mp4Box is one box inside a buffer: its type, and the byte range of its
// payload (after the header) and of the whole box.
type mp4Box struct {
	typ         string
	start, body int // offset of the box header and of its payload
	end         int // offset just past the box
}

// mp4Boxes lists the boxes directly inside buf[from:to]. It stops at the
// first box whose header or size does not fit, and reports that as an error
// only when nothing at all could be read.
func mp4Boxes(buf []byte, from, to int) ([]mp4Box, error) {
	if to > len(buf) {
		to = len(buf)
	}
	var out []mp4Box
	for p := from; p+8 <= to; {
		size := uint64(binary.BigEndian.Uint32(buf[p:]))
		typ := string(buf[p+4 : p+8])
		hdr := 8
		switch size {
		case 0:
			size = uint64(to - p)
		case 1:
			if p+16 > to {
				return out, nil
			}
			size = binary.BigEndian.Uint64(buf[p+8:])
			hdr = 16
		}
		if size < uint64(hdr) || size > uint64(to-p) {
			if len(out) == 0 {
				return nil, errFMP4Malformed
			}
			return out, nil
		}
		out = append(out, mp4Box{typ: typ, start: p, body: p + hdr, end: p + int(size)})
		p += int(size)
	}
	return out, nil
}

// mp4Find returns the first child of type typ inside box b.
func mp4Find(buf []byte, b mp4Box, typ string) (mp4Box, bool) {
	kids, _ := mp4Boxes(buf, b.body, b.end)
	for _, k := range kids {
		if k.typ == typ {
			return k, true
		}
	}
	return mp4Box{}, false
}

// mp4Path walks a chain of child types from b.
func mp4Path(buf []byte, b mp4Box, path ...string) (mp4Box, bool) {
	cur := b
	for _, t := range path {
		next, ok := mp4Find(buf, cur, t)
		if !ok {
			return mp4Box{}, false
		}
		cur = next
	}
	return cur, true
}

// parseFMP4Init reads the audio track out of an init segment (or out of any
// buffer that carries a moov box, such as a segment with the init inline).
func parseFMP4Init(buf []byte) (fmp4Track, error) {
	top, err := mp4Boxes(buf, 0, len(buf))
	if err != nil {
		return fmp4Track{}, err
	}
	var moov mp4Box
	found := false
	for _, b := range top {
		if b.typ == "moov" {
			moov, found = b, true
			break
		}
	}
	if !found {
		return fmp4Track{}, errFMP4NoAudio
	}
	kids, _ := mp4Boxes(buf, moov.body, moov.end)
	var sawEncrypted, sawOther bool
	for _, trak := range kids {
		if trak.typ != "trak" {
			continue
		}
		hdlr, ok := mp4Path(buf, trak, "mdia", "hdlr")
		// hdlr: version/flags(4) pre_defined(4) handler_type(4)
		if !ok || hdlr.body+12 > hdlr.end || string(buf[hdlr.body+8:hdlr.body+12]) != "soun" {
			continue
		}
		tkhd, ok := mp4Find(buf, trak, "tkhd")
		if !ok || tkhd.body+4 > tkhd.end {
			continue
		}
		// tkhd v0: version/flags(4) ctime(4) mtime(4) track_ID(4)
		// tkhd v1: version/flags(4) ctime(8) mtime(8) track_ID(4)
		idOff := tkhd.body + 12
		if buf[tkhd.body] == 1 {
			idOff = tkhd.body + 20
		}
		if idOff+4 > tkhd.end {
			continue
		}
		tr := fmp4Track{trackID: binary.BigEndian.Uint32(buf[idOff:])}

		stsd, ok := mp4Path(buf, trak, "mdia", "minf", "stbl", "stsd")
		if !ok || stsd.body+8 > stsd.end {
			continue
		}
		// stsd: version/flags(4) entry_count(4), then sample entries.
		entries, _ := mp4Boxes(buf, stsd.body+8, stsd.end)
		if len(entries) == 0 {
			continue
		}
		entry := entries[0]
		switch entry.typ {
		case "mp4a":
		case "enca":
			sawEncrypted = true
			continue
		default:
			sawOther = true
			continue
		}
		esds, ok := mp4aESDS(buf, entry)
		if !ok {
			sawOther = true
			continue
		}
		if !parseESDS(buf[esds.body:esds.end], &tr) {
			sawOther = true
			continue
		}
		tr.defaultSampleSize = trexDefaultSize(buf, moov, tr.trackID)
		return tr, nil
	}
	switch {
	case sawEncrypted:
		return fmp4Track{}, errFMP4Encrypted
	case sawOther:
		return fmp4Track{}, errFMP4Unsupported
	}
	return fmp4Track{}, errFMP4NoAudio
}

// mp4aESDS finds the esds box inside an mp4a sample entry. The entry's fixed
// part is 28 bytes for version 0, 44 for QuickTime version 1, 64 for
// version 2.
func mp4aESDS(buf []byte, entry mp4Box) (mp4Box, bool) {
	if entry.body+28 > entry.end {
		return mp4Box{}, false
	}
	fixed := 28
	switch binary.BigEndian.Uint16(buf[entry.body+8:]) {
	case 1:
		fixed = 44
	case 2:
		fixed = 64
	}
	if entry.body+fixed > entry.end {
		return mp4Box{}, false
	}
	kids, _ := mp4Boxes(buf, entry.body+fixed, entry.end)
	for _, k := range kids {
		if k.typ == "esds" {
			return k, true
		}
	}
	return mp4Box{}, false
}

// descriptor reads one MPEG-4 descriptor header (tag, expandable size) at
// b[p:] and returns the tag, the payload range and ok.
func descriptor(b []byte, p int) (tag byte, start, end int, ok bool) {
	if p >= len(b) {
		return 0, 0, 0, false
	}
	tag = b[p]
	p++
	size := 0
	for i := 0; i < 4; i++ {
		if p >= len(b) {
			return 0, 0, 0, false
		}
		c := b[p]
		p++
		size = size<<7 | int(c&0x7F)
		if c&0x80 == 0 {
			break
		}
	}
	if p+size > len(b) {
		return 0, 0, 0, false
	}
	return tag, p, p + size, true
}

// parseESDS reads the decoder config out of an esds payload (version/flags
// first) and fills codec and, for AAC, the ADTS fields.
func parseESDS(b []byte, tr *fmp4Track) bool {
	if len(b) < 4 {
		return false
	}
	tag, s, e, ok := descriptor(b, 4)
	if !ok || tag != 0x03 || s+3 > e {
		return false
	}
	// ES_Descriptor: ES_ID(2) flags(1) [dependsOn(2)] [URL] [OCR_ES_Id(2)]
	flags := b[s+2]
	p := s + 3
	if flags&0x80 != 0 {
		p += 2
	}
	if flags&0x40 != 0 {
		if p >= e {
			return false
		}
		p += 1 + int(b[p])
	}
	if flags&0x20 != 0 {
		p += 2
	}
	if p >= e {
		return false
	}
	sub := b[:e]
	tag, s, e, ok = descriptor(sub, p)
	if !ok || tag != 0x04 || s+13 > e {
		return false
	}
	switch b[s] { // objectTypeIndication
	case 0x6B, 0x69:
		tr.codec = fmp4MP3
		return true
	case 0x40, 0x66, 0x67, 0x68:
	default:
		return false
	}
	tag, s, e, ok = descriptor(b[:e], s+13)
	if !ok || tag != 0x05 || e-s < 2 {
		return false
	}
	return parseASC(b[s:e], tr)
}

// adtsFreqs maps the ADTS sampling frequency index to its rate.
var adtsFreqs = []uint32{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}

// bitReader reads big-endian bit fields.
type bitReader struct {
	b   []byte
	pos int
}

func (r *bitReader) read(n int) (uint32, bool) {
	var v uint32
	for i := 0; i < n; i++ {
		if r.pos>>3 >= len(r.b) {
			return 0, false
		}
		bit := (r.b[r.pos>>3] >> (7 - uint(r.pos&7))) & 1
		v = v<<1 | uint32(bit)
		r.pos++
	}
	return v, true
}

// parseASC reads an AudioSpecificConfig into the ADTS fields.
func parseASC(asc []byte, tr *fmp4Track) bool {
	r := &bitReader{b: asc}
	objType := func() (uint32, bool) {
		t, ok := r.read(5)
		if ok && t == 31 {
			ext, ok2 := r.read(6)
			return 32 + ext, ok2
		}
		return t, ok
	}
	freqIndex := func() (uint32, bool) {
		i, ok := r.read(4)
		if !ok {
			return 0, false
		}
		if i == 15 {
			f, ok := r.read(24)
			if !ok {
				return 0, false
			}
			for k, v := range adtsFreqs {
				if v == f {
					return uint32(k), true
				}
			}
			return 0, false
		}
		return i, true
	}
	ot, ok := objType()
	if !ok {
		return false
	}
	fi, ok := freqIndex()
	if !ok || fi > 12 {
		return false
	}
	ch, ok := r.read(4)
	if !ok || ch == 0 || ch > 7 {
		return false
	}
	// Explicit SBR/PS (HE-AAC v1/v2): the core codec follows. ADTS cannot
	// say SBR, so it carries the core object type and the core rate, and
	// the decoder finds the SBR data in the stream itself.
	if ot == 5 || ot == 29 {
		if _, ok := freqIndex(); !ok { // extension (output) rate
			return false
		}
		if ot, ok = objType(); !ok {
			return false
		}
	}
	if ot < 1 || ot > 4 {
		ot = 2 // AAC LC: what every ADTS decoder takes
	}
	tr.codec = fmp4AAC
	tr.profile = byte(ot - 1)
	tr.freqIdx = byte(fi)
	tr.channels = byte(ch)
	return true
}

// trexDefaultSize returns the default sample size mvex/trex gives a track.
func trexDefaultSize(buf []byte, moov mp4Box, trackID uint32) uint32 {
	mvex, ok := mp4Find(buf, moov, "mvex")
	if !ok {
		return 0
	}
	kids, _ := mp4Boxes(buf, mvex.body, mvex.end)
	for _, k := range kids {
		// trex: version/flags(4) track_ID(4) desc_index(4) duration(4) size(4) flags(4)
		if k.typ != "trex" || k.body+20 > k.end {
			continue
		}
		if binary.BigEndian.Uint32(buf[k.body+4:]) == trackID {
			return binary.BigEndian.Uint32(buf[k.body+16:])
		}
	}
	return 0
}

// adtsHeader builds the 7-byte ADTS header for one AAC frame of n bytes.
func adtsHeader(tr fmp4Track, n int) [7]byte {
	l := n + 7
	return [7]byte{
		0xFF,
		0xF1, // MPEG-4, layer 0, no CRC
		tr.profile<<6 | tr.freqIdx<<2 | (tr.channels>>2)&1,
		(tr.channels&3)<<6 | byte(l>>11)&3,
		byte(l >> 3),
		byte(l&7)<<5 | 0x1F,
		0xFC,
	}
}

// fmp4MaxFrame bounds one AAC frame. ADTS can describe 8191 bytes; a sample
// bigger than that is not audio this converter can frame.
const fmp4MaxFrame = 8191 - 7

// fmp4ToAudio converts the fragments (moof + mdat pairs) in one media
// segment into the elementary stream the speaker plays: ADTS frames for
// AAC, the bare frames for MP3. ok is false when no sample could be read.
func fmp4ToAudio(seg []byte, tr fmp4Track) (out []byte, ok bool) {
	top, err := mp4Boxes(seg, 0, len(seg))
	if err != nil {
		return nil, false
	}
	for i, moof := range top {
		if moof.typ != "moof" {
			continue
		}
		// The mdat that follows this moof, for runs without a data offset.
		mdatBody := -1
		for _, b := range top[i+1:] {
			if b.typ == "mdat" {
				mdatBody = b.body
				break
			}
			if b.typ == "moof" {
				break
			}
		}
		trafs, _ := mp4Boxes(seg, moof.body, moof.end)
		for _, traf := range trafs {
			if traf.typ != "traf" {
				continue
			}
			out = appendTraf(out, seg, moof, traf, mdatBody, tr)
		}
	}
	return out, len(out) > 0
}

// appendTraf appends the samples of one track fragment, if it is ours.
func appendTraf(out, seg []byte, moof, traf mp4Box, mdatBody int, tr fmp4Track) []byte {
	tfhd, ok := mp4Find(seg, traf, "tfhd")
	if !ok || tfhd.body+8 > tfhd.end {
		return out
	}
	flags := binary.BigEndian.Uint32(seg[tfhd.body:]) & 0xFFFFFF
	if binary.BigEndian.Uint32(seg[tfhd.body+4:]) != tr.trackID {
		return out
	}
	p := tfhd.body + 8
	base := int64(moof.start)
	if flags&0x1 != 0 { // base-data-offset-present
		if p+8 > tfhd.end {
			return out
		}
		base = int64(binary.BigEndian.Uint64(seg[p:]))
		p += 8
	}
	if flags&0x2 != 0 { // sample-description-index-present
		p += 4
	}
	if flags&0x8 != 0 { // default-sample-duration-present
		p += 4
	}
	defSize := tr.defaultSampleSize
	if flags&0x10 != 0 { // default-sample-size-present
		if p+4 > tfhd.end {
			return out
		}
		defSize = binary.BigEndian.Uint32(seg[p:])
	}

	runs, _ := mp4Boxes(seg, traf.body, traf.end)
	next := int64(-1) // where the next run without an offset starts
	for _, run := range runs {
		if run.typ != "trun" || run.body+8 > run.end {
			continue
		}
		rflags := binary.BigEndian.Uint32(seg[run.body:]) & 0xFFFFFF
		count := int(binary.BigEndian.Uint32(seg[run.body+4:]))
		q := run.body + 8
		var data int64
		switch {
		case rflags&0x1 != 0: // data-offset-present
			if q+4 > run.end {
				return out
			}
			data = base + int64(int32(binary.BigEndian.Uint32(seg[q:])))
			q += 4
		case next >= 0:
			data = next
		case mdatBody >= 0:
			data = int64(mdatBody)
		default:
			return out
		}
		if rflags&0x4 != 0 { // first-sample-flags-present
			q += 4
		}
		per := 0
		for _, f := range []uint32{0x100, 0x200, 0x400, 0x800} {
			if rflags&f != 0 {
				per += 4
			}
		}
		if count < 0 || int64(per)*int64(count) > int64(run.end-q) {
			return out
		}
		for s := 0; s < count; s++ {
			e := q + s*per
			if rflags&0x100 != 0 { // sample-duration
				e += 4
			}
			size := defSize
			if rflags&0x200 != 0 { // sample-size
				size = binary.BigEndian.Uint32(seg[e:])
			}
			if size == 0 || data < 0 || data+int64(size) > int64(len(seg)) {
				return out
			}
			sample := seg[data : data+int64(size)]
			data += int64(size)
			switch tr.codec {
			case fmp4AAC:
				if len(sample) > fmp4MaxFrame {
					return out
				}
				h := adtsHeader(tr, len(sample))
				out = append(out, h[:]...)
				out = append(out, sample...)
			case fmp4MP3:
				out = append(out, sample...)
			}
		}
		next = data
	}
	return out
}
