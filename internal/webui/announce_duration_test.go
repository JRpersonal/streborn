package webui

import (
	"bytes"
	"encoding/json"
	"testing"
)

// The test clips are synthetic: valid Layer III frame headers with a filler
// payload (fakeMP3), never real audio.

var (
	// MPEG-2 Layer III, 24 kHz, 32 kbit/s, mono: what Google Translate TTS
	// serves. 576 samples at 24 kHz = 24 ms per frame.
	ttsHeader = [4]byte{0xFF, 0xF3, 0x44, 0xC4}
	// MPEG-1 Layer III, 44.1 kHz, 128 kbit/s, joint stereo. 1152 samples at
	// 44.1 kHz = 26.12 ms per frame.
	chimeHeader = [4]byte{0xFF, 0xFB, 0x90, 0x64}
)

// id3Tag builds an ID3v2 tag with size bytes of (zero) content.
func id3Tag(size int) []byte {
	tag := []byte{'I', 'D', '3', 4, 0, 0,
		byte(size >> 21 & 0x7F), byte(size >> 14 & 0x7F), byte(size >> 7 & 0x7F), byte(size & 0x7F)}
	return append(tag, make([]byte, size)...)
}

func TestMP3DurationMsTTS(t *testing.T) {
	t.Parallel()
	clip := fakeMP3(t, ttsHeader, 125) // 125 x 24 ms
	if got := mp3DurationMs(clip); got != 3000 {
		t.Errorf("TTS clip: %d ms, want 3000", got)
	}
}

func TestMP3DurationMsMPEG1WithTags(t *testing.T) {
	t.Parallel()
	clip := append(id3Tag(2000), fakeMP3(t, chimeHeader, 100)...)
	trailer := append([]byte("TAG"), make([]byte, 125)...)
	clip = append(clip, trailer...)
	if got, want := mp3DurationMs(clip), int64(2612); got != want { // 100 x 1152 / 44.1 kHz
		t.Errorf("tagged MPEG-1 clip: %d ms, want %d", got, want)
	}
}

// A TTS text arrives as several MP3 chunks back to back, each possibly with
// its own tag: the answer is the sum of the chunks.
func TestMP3DurationMsSumsTTSChunks(t *testing.T) {
	t.Parallel()
	var clip []byte
	for _, n := range []int{50, 75, 25} {
		clip = append(clip, id3Tag(40)...)
		clip = append(clip, fakeMP3(t, ttsHeader, n)...)
	}
	if got := mp3DurationMs(clip); got != 150*24 {
		t.Errorf("three chunks: %d ms, want %d", got, 150*24)
	}
}

// The silent lead-in is part of what plays, so it is part of the length.
func TestMP3DurationMsCountsLeadIn(t *testing.T) {
	t.Parallel()
	clip := fakeMP3(t, ttsHeader, 100)
	out := withSilentLeadIn(clip, "audio/mpeg", 800)
	got, base := mp3DurationMs(out), mp3DurationMs(clip)
	if got-base < 800 || got-base > 830 {
		t.Errorf("lead-in adds %d ms, want about 800", got-base)
	}
}

// A little junk between frames is stepped over; the frames still count.
func TestMP3DurationMsResyncsOverJunk(t *testing.T) {
	t.Parallel()
	clip := fakeMP3(t, ttsHeader, 100)
	clip = append(clip, 0x00, 0x11, 0x22)
	clip = append(clip, fakeMP3(t, ttsHeader, 100)...)
	if got := mp3DurationMs(clip); got != 200*24 {
		t.Errorf("clip with junk: %d ms, want %d", got, 200*24)
	}
}

// Past the frame cap the rest is extrapolated, not walked.
func TestMP3DurationMsCapExtrapolates(t *testing.T) {
	t.Parallel()
	frames := mp3WalkMaxFrames + mp3WalkMaxFrames/2
	clip := fakeMP3(t, ttsHeader, frames)
	if got, want := mp3DurationMs(clip), int64(frames*24); got < want-1 || got > want+1 {
		t.Errorf("capped walk: %d ms, want %d", got, want)
	}
}

// Anything that is not mostly MP3 frames is unknown.
func TestMP3DurationMsUnknown(t *testing.T) {
	t.Parallel()
	ogg := append([]byte("OggS"), bytes.Repeat([]byte{0x42}, 4000)...)
	// One valid-looking header inside other data: a stray sync word.
	stray := append(bytes.Repeat([]byte{0x10}, 3000), ttsHeader[:]...)
	stray = append(stray, bytes.Repeat([]byte{0x10}, 3000)...)
	// Real frames, but only a small part of a much larger blob.
	mostlyJunk := append(fakeMP3(t, ttsHeader, 10), bytes.Repeat([]byte{0x10}, 20000)...)
	for name, b := range map[string][]byte{
		"empty": nil, "text": []byte("not audio at all"), "ogg": ogg,
		"stray sync word": stray, "mostly junk": mostlyJunk,
	} {
		if got := mp3DurationMs(b); got != 0 {
			t.Errorf("%s: %d ms, want 0 (unknown)", name, got)
		}
	}
}

// The answer carries durationMs only when the length is known.
func TestAnnounceAnswerDuration(t *testing.T) {
	t.Parallel()
	got, _ := json.Marshal(announceAnswer(4800, 1200))
	if string(got) != `{"bytes":4800,"durationMs":1200,"ok":true,"queued":true}` {
		t.Errorf("known length: %s", got)
	}
	got, _ = json.Marshal(announceAnswer(4800, 0))
	if string(got) != `{"bytes":4800,"ok":true,"queued":true}` {
		t.Errorf("unknown length: %s", got)
	}
}

// The announce answer only measures what may be MP3.
func TestAnnounceDurationMsByMime(t *testing.T) {
	t.Parallel()
	clip := fakeMP3(t, ttsHeader, 50)
	if got := announceDurationMs(clip, "audio/mpeg"); got != 1200 {
		t.Errorf("audio/mpeg: %d ms, want 1200", got)
	}
	if got := announceDurationMs(clip, "audio/ogg"); got != 0 {
		t.Errorf("audio/ogg: %d ms, want 0", got)
	}
}
