package usbmedia

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// flacHeader builds "fLaC" plus a STREAMINFO block for rate and bits.
func flacHeader(rate, bits int) []byte {
	si := make([]byte, 34)
	binary.BigEndian.PutUint16(si[0:], 4096)
	binary.BigEndian.PutUint16(si[2:], 4096)
	// 20 bits rate, 3 bits channels-1 (stereo), 5 bits bits-1, 36 bits samples.
	v := uint64(rate)<<44 | uint64(1)<<41 | uint64(bits-1)<<36 | 123456
	binary.BigEndian.PutUint64(si[10:], v)
	out := append([]byte("fLaC"), 0x80, 0, 0, 34) // last block, STREAMINFO, length 34
	return append(out, si...)
}

// wavHeader builds a RIFF/WAVE header with a LIST chunk before fmt, the way
// some editors write it.
func wavHeader(rate, bits int) []byte {
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(1000))
	b.WriteString("WAVE")
	b.WriteString("LIST")
	_ = binary.Write(&b, binary.LittleEndian, uint32(5)) // odd: padded to 6
	b.Write([]byte{'I', 'N', 'F', 'O', 'x', 0})
	b.WriteString("fmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1)) // PCM
	_ = binary.Write(&b, binary.LittleEndian, uint16(2))
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate))
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate*2*bits/8))
	_ = binary.Write(&b, binary.LittleEndian, uint16(2*bits/8))
	_ = binary.Write(&b, binary.LittleEndian, uint16(bits))
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(0))
	return b.Bytes()
}

func TestReadAudioFormat(t *testing.T) {
	id3 := append([]byte("ID3\x04\x00\x00"), 0, 0, 0x01, 0x00) // syncsafe 128
	id3 = append(id3, make([]byte, 128)...)
	cases := []struct {
		name string
		data []byte
		want AudioFormat
		ok   bool
		high bool
	}{
		{"a.flac", flacHeader(44100, 16), AudioFormat{44100, 16}, true, false},
		{"hires-2496.FLAC", flacHeader(96000, 24), AudioFormat{96000, 24}, true, true},
		{"b.flac", flacHeader(48000, 24), AudioFormat{48000, 24}, true, false},
		{"tagged.flac", append(id3, flacHeader(192000, 24)...), AudioFormat{192000, 24}, true, true},
		{"c.wav", wavHeader(44100, 16), AudioFormat{44100, 16}, true, false},
		{"wav-96k.wav", wavHeader(96000, 24), AudioFormat{96000, 24}, true, true},
		{"not-a-flac.flac", []byte("ID3 but nothing else"), AudioFormat{}, false, false},
		{"broken.wav", []byte("RIFF\x00\x00\x00\x00WAVE"), AudioFormat{}, false, false},
		{"song.m4a", flacHeader(96000, 24), AudioFormat{}, false, false}, // not read
		{"song.mp3", nil, AudioFormat{}, false, false},
	}
	for _, c := range cases {
		got, ok := ReadAudioFormat(bytes.NewReader(c.data), c.name)
		if ok != c.ok || got != c.want || got.TooHighForTheSpeaker() != c.high {
			t.Errorf("%s: %+v %v, want %+v %v (high %v)", c.name, got, ok, c.want, c.ok, c.high)
		}
	}
}

func TestLibraryAudioFormatReadsOnlyTheHeaderOfAStickFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Album 1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Album 1", "hires-2496.flac"), flacHeader(96000, 24), 0o644); err != nil {
		t.Fatal(err)
	}
	lib, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lib.Close() }()
	if f, ok := lib.AudioFormat("Album 1/hires-2496.flac"); !ok || f.SampleRate != 96000 {
		t.Fatalf("format: %+v %v", f, ok)
	}
	// Outside the stick, or not a playable file: nothing is read.
	for _, p := range []string{"../x.flac", "Album 1/missing.flac", "Album 1"} {
		if _, ok := lib.AudioFormat(p); ok {
			t.Fatalf("%s must not be read", p)
		}
	}
}
