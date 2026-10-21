package webui

import "strings"

// Silent lead-in for announcements (#826).
//
// The speaker starts a UPnP clip with its amplifier still muted and only opens
// it a few hundred milliseconds into playback, so the first word of every
// announcement was swallowed. It is the speaker's own start-up behaviour, it
// already did it with Bose's cloud-era notifications, and users worked around
// it by padding the text with characters the TTS does not speak. STR now puts
// a short run of silent MP3 frames in front of the clip, so the amplifier is
// open by the time the speech starts.
//
// The silent frames copy the clip's own first frame header (MPEG version,
// sample rate, channel mode, bitrate), so the stream stays one uniform MP3 the
// player decodes without a format change. A frame whose side information is
// all zero carries no Huffman data (part2_3_length 0 in every granule) and
// decodes to digital silence; its main data bytes are ignored. Anything that is
// not recognisably MP3 (an Ogg or WAV chime passed as url) is served unchanged.

// announceLeadIn is how much silence goes in front of an announcement. Long
// enough to cover the speaker's amplifier unmute, short enough not to feel like
// a pause.
var announceLeadIn = 800 // milliseconds

// mp3Frame is the subset of an MPEG audio Layer III frame header STR needs.
type mp3Frame struct {
	header     [4]byte
	version    byte // 3 = MPEG-1, 2 = MPEG-2, 0 = MPEG-2.5
	sampleRate int
	kbps       int
	mono       bool
	length     int // bytes, including the header
}

// samples is the number of PCM samples per channel one frame decodes to.
func (f mp3Frame) samples() int {
	if f.version == 3 {
		return 1152
	}
	return 576
}

// sideInfoLen is the size of the side information that follows the header
// (no CRC: the silent frames never carry one).
func (f mp3Frame) sideInfoLen() int {
	switch {
	case f.version == 3 && f.mono:
		return 17
	case f.version == 3:
		return 32
	case f.mono:
		return 9
	default:
		return 17
	}
}

// parseMP3Frame decodes a Layer III frame header at b[0:4]. ok is false for
// anything that is not a usable header (free-format bitrate included).
func parseMP3Frame(b []byte) (mp3Frame, bool) {
	if len(b) < 4 || b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return mp3Frame{}, false
	}
	f := mp3Frame{version: (b[1] >> 3) & 0x03}
	copy(f.header[:], b[:4])
	if f.version == 1 || (b[1]>>1)&0x03 != 0x01 { // reserved version, not Layer III
		return mp3Frame{}, false
	}
	brIdx := (b[2] >> 4) & 0x0F
	srIdx := (b[2] >> 2) & 0x03
	if brIdx == 0 || brIdx == 0x0F || srIdx == 0x03 {
		return mp3Frame{}, false
	}
	mpeg1L3 := [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	mpeg2L3 := [16]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
	rates := [3]int{44100, 48000, 32000}
	f.sampleRate = rates[srIdx]
	f.kbps = mpeg2L3[brIdx]
	switch f.version {
	case 3:
		f.kbps = mpeg1L3[brIdx]
	case 2:
		f.sampleRate /= 2
	case 0:
		f.sampleRate /= 4
	}
	f.mono = (b[3]>>6)&0x03 == 0x03
	padding := int((b[2] >> 1) & 0x01)
	f.length = (f.samples()/8)*f.kbps*1000/f.sampleRate + padding
	if f.length <= 4+f.sideInfoLen() {
		return mp3Frame{}, false
	}
	return f, true
}

// id3v2Len returns the size of an ID3v2 tag at the start of b, or 0.
func id3v2Len(b []byte) int {
	if len(b) < 10 || string(b[:3]) != "ID3" {
		return 0
	}
	for _, c := range b[6:10] {
		if c&0x80 != 0 { // not a syncsafe integer: not a tag we understand
			return 0
		}
	}
	size := int(b[6])<<21 | int(b[7])<<14 | int(b[8])<<7 | int(b[9])
	n := 10 + size
	if b[5]&0x10 != 0 { // footer present
		n += 10
	}
	if n > len(b) {
		return 0
	}
	return n
}

// silentMP3Frame builds one frame that decodes to silence, in the format of f:
// the same header with the CRC flag, padding and private bits cleared and no
// mode extension, then all-zero side information and main data.
func silentMP3Frame(f mp3Frame) []byte {
	h := f.header
	h[1] |= 0x01  // protection bit set: no CRC follows
	h[2] &^= 0x03 // no padding, private bit clear
	h[3] &^= 0x30 // no joint-stereo mode extension
	silent, _ := parseMP3Frame(h[:])
	out := make([]byte, silent.length)
	copy(out, h[:])
	return out
}

// isMP3Mime reports whether a content type may hold MP3 audio. An empty type
// counts: the bytes themselves are checked before anything is added.
func isMP3Mime(mime string) bool {
	m := strings.ToLower(strings.TrimSpace(mime))
	if i := strings.IndexByte(m, ';'); i >= 0 {
		m = strings.TrimSpace(m[:i])
	}
	switch m {
	case "", "audio/mpeg", "audio/mp3", "audio/mpeg3", "audio/x-mpeg", "audio/x-mp3", "audio/x-mpeg-3", "application/octet-stream":
		return true
	}
	return false
}

// withSilentLeadIn returns audio with about leadMS milliseconds of silent MP3
// frames placed before the first audio frame (after a leading ID3v2 tag, which
// has to stay first). Audio that does not start with two consecutive valid
// Layer III frames is returned unchanged, so a non-MP3 clip is never corrupted.
func withSilentLeadIn(audio []byte, mime string, leadMS int) []byte {
	if leadMS <= 0 || !isMP3Mime(mime) {
		return audio
	}
	start := id3v2Len(audio)
	f, ok := parseMP3Frame(audio[start:])
	if !ok {
		return audio
	}
	// One header could be a coincidence; the next frame must line up too.
	if next := start + f.length; next < len(audio) {
		g, ok := parseMP3Frame(audio[next:])
		if !ok || g.version != f.version || g.sampleRate != f.sampleRate {
			return audio
		}
	}
	frame := silentMP3Frame(f)
	frameMS := float64(f.samples()) * 1000 / float64(f.sampleRate)
	count := int(float64(leadMS)/frameMS + 0.999)
	out := make([]byte, 0, len(audio)+count*len(frame))
	out = append(out, audio[:start]...)
	for i := 0; i < count; i++ {
		out = append(out, frame...)
	}
	return append(out, audio[start:]...)
}
