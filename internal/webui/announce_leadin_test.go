package webui

import (
	"bytes"
	"testing"
)

// fakeMP3 builds n frames with the given header, each filled with a non-zero
// payload so they cannot be mistaken for the silent lead-in.
func fakeMP3(t *testing.T, header [4]byte, n int) []byte {
	t.Helper()
	f, ok := parseMP3Frame(header[:])
	if !ok {
		t.Fatalf("test header % X does not parse", header)
	}
	var out []byte
	for i := 0; i < n; i++ {
		frame := bytes.Repeat([]byte{0x55}, f.length)
		copy(frame, header[:])
		out = append(out, frame...)
	}
	return out
}

// walkLeadIn checks that b starts with silent frames, returns their count and
// total duration, and the offset the first non-silent byte sits at.
func walkLeadIn(t *testing.T, b []byte) (frames int, ms float64, end int) {
	t.Helper()
	for end < len(b) {
		f, ok := parseMP3Frame(b[end:])
		if !ok {
			t.Fatalf("no valid frame header at offset %d", end)
		}
		if end+f.length > len(b) {
			t.Fatalf("frame at %d runs past the end", end)
		}
		body := b[end+4 : end+f.length]
		if !bytes.Equal(body, make([]byte, len(body))) {
			return frames, ms, end
		}
		if b[end+1]&0x01 == 0 {
			t.Errorf("silent frame at %d claims a CRC", end)
		}
		frames++
		ms += float64(f.samples()) * 1000 / float64(f.sampleRate)
		end += f.length
	}
	return frames, ms, end
}

func TestSilentLeadInForGoogleTTS(t *testing.T) {
	// MPEG-2 Layer III, 24 kHz, 32 kbit/s, mono: what Google Translate TTS serves.
	clip := fakeMP3(t, [4]byte{0xFF, 0xF3, 0x44, 0xC4}, 40)
	out := withSilentLeadIn(clip, "audio/mpeg", 800)

	frames, ms, end := walkLeadIn(t, out)
	if ms < 800 || ms > 830 {
		t.Errorf("lead-in is %.0f ms in %d frames, want about 800", ms, frames)
	}
	if !bytes.Equal(out[end:], clip) {
		t.Fatal("the original clip does not follow the lead-in unchanged")
	}
	// The whole result is one unbroken chain of frames in the clip's format.
	for off := 0; off < len(out); {
		f, ok := parseMP3Frame(out[off:])
		if !ok || f.sampleRate != 24000 || f.version != 2 || !f.mono {
			t.Fatalf("frame at %d breaks the stream: %+v ok=%v", off, f, ok)
		}
		off += f.length
	}
	// The duration estimate that drives the restore covers the lead-in too.
	if got, want := mp3DurationSec(out), mp3DurationSec(clip)+ms/1000; got < want-0.01 || got > want+0.01 {
		t.Errorf("duration with lead-in %.3f s, want %.3f", got, want)
	}
}

func TestSilentLeadInMatchesAStereoMPEG1Chime(t *testing.T) {
	// MPEG-1 Layer III, 44.1 kHz, 128 kbit/s, joint stereo with CRC and
	// padding: the silent frames drop CRC, padding and mode extension.
	clip := fakeMP3(t, [4]byte{0xFF, 0xFA, 0x92, 0x64}, 10)
	out := withSilentLeadIn(clip, "audio/mpeg", 500)
	frames, ms, end := walkLeadIn(t, out)
	if frames == 0 || ms < 500 {
		t.Fatalf("lead-in %d frames / %.0f ms, want >= 500 ms", frames, ms)
	}
	if out[1] != 0xFB || out[2] != 0x90 || out[3] != 0x44 {
		t.Errorf("silent header % X, want FB 90 44 after FF", out[:4])
	}
	if f, _ := parseMP3Frame(out); f.length != 417 {
		t.Errorf("silent frame is %d bytes, want 417", f.length)
	}
	if !bytes.Equal(out[end:], clip) {
		t.Fatal("the chime does not follow the lead-in unchanged")
	}
}

func TestSilentLeadInKeepsTheID3TagFirst(t *testing.T) {
	tag := append([]byte("ID3\x04\x00\x00\x00\x00\x00\x05"), []byte("TITLE")...)
	clip := fakeMP3(t, [4]byte{0xFF, 0xF3, 0x44, 0xC4}, 5)
	in := append(append([]byte{}, tag...), clip...)
	out := withSilentLeadIn(in, "audio/mpeg", 300)
	if !bytes.HasPrefix(out, tag) {
		t.Fatal("the ID3 tag is no longer at the start")
	}
	frames, _, end := walkLeadIn(t, out[len(tag):])
	if frames == 0 || !bytes.Equal(out[len(tag)+end:], clip) {
		t.Fatalf("lead-in after the tag: %d frames, clip intact=%v", frames, bytes.Equal(out[len(tag)+end:], clip))
	}
}

func TestSilentLeadInLeavesOtherAudioAlone(t *testing.T) {
	mp3 := fakeMP3(t, [4]byte{0xFF, 0xF3, 0x44, 0xC4}, 5)
	cases := map[string]struct {
		audio []byte
		mime  string
	}{
		"ogg mime":         {mp3, "audio/ogg"},
		"ogg bytes":        {[]byte("OggS\x00\x02 some vorbis data here"), ""},
		"wav bytes":        {[]byte("RIFF\x24\x00\x00\x00WAVEfmt "), "audio/wav"},
		"lone sync header": {append([]byte{0xFF, 0xF3, 0x44, 0xC4}, bytes.Repeat([]byte{0x11}, 200)...), "audio/mpeg"},
		"empty":            {nil, "audio/mpeg"},
	}
	for name, c := range cases {
		if out := withSilentLeadIn(c.audio, c.mime, 800); !bytes.Equal(out, c.audio) {
			t.Errorf("%s: audio was changed (%d -> %d bytes)", name, len(c.audio), len(out))
		}
	}
	if out := withSilentLeadIn(mp3, "audio/mpeg", 0); !bytes.Equal(out, mp3) {
		t.Error("a zero lead-in still changed the clip")
	}
	if out := withSilentLeadIn(mp3, "audio/mpeg; charset=binary", 100); bytes.Equal(out, mp3) {
		t.Error("a mime with parameters was not recognised as MP3")
	}
}
