package webui

import (
	"context"
	"encoding/binary"
	"sync/atomic"
	"testing"
	"time"
)

// mp4Box builds one MP4 box: a 32-bit size, the type, the body.
func mp4Box(typ string, body []byte) []byte {
	b := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(b, uint32(8+len(body)))
	copy(b[4:], typ)
	return append(b, body...)
}

// mvhdV0 is a version 0 movie header with the given timescale and duration.
func mvhdV0(scale, dur uint32) []byte {
	body := make([]byte, 100)
	binary.BigEndian.PutUint32(body[12:], scale)
	binary.BigEndian.PutUint32(body[16:], dur)
	return mp4Box("mvhd", body)
}

// m4aFile builds an AAC m4a of the given length at 48 kHz: ftyp, then moov
// and mdat in faststart order, or mdat first and moov at the end.
func m4aFile(length time.Duration, faststart bool, mdatBytes int) []byte {
	ftyp := mp4Box("ftyp", []byte("M4A \x00\x00\x00\x00M4A isom"))
	moov := mp4Box("moov", append(mvhdV0(48000, uint32(length/time.Second)*48000), mp4Box("trak", make([]byte, 600))...))
	mdat := mp4Box("mdat", make([]byte, mdatBytes))
	out := append([]byte(nil), ftyp...)
	if faststart {
		out = append(out, moov...)
		return append(out, mdat...)
	}
	out = append(out, mdat...)
	return append(out, moov...)
}

// flacFile is a FLAC head: the marker and a STREAMINFO for total samples at
// rate, then some frames.
func flacFile(rate, total uint64) []byte {
	b := []byte("fLaC")
	b = append(b, 0x80, 0, 0, 34) // last metadata block, STREAMINFO, 34 bytes
	si := make([]byte, 34)
	v := rate<<44 | 1<<41 | 15<<36 | total // stereo, 16 bit
	binary.BigEndian.PutUint64(si[10:], v)
	b = append(b, si...)
	return append(b, make([]byte, 4096)...)
}

// The #830 m4a: 56 min of AAC-LC, faststart, on nginx. Its length is read
// from the movie header, so it goes to the speaker directly with a length
// and a position to come back to.
func TestCustomURLToAFaststartM4AGoesDirectWithItsLength(t *testing.T) {
	useLoopbackProbeClient(t)
	var hits atomic.Int32
	nas := nasFile(t, m4aFile(56*time.Minute, true, 64*1024), &hits)
	defer nas.Close()
	s, _ := newPlayTestServer(t)

	p := customPreset(3, nas.URL+"/hoerbuch/kapitel.m4a")
	if got := s.directFileMime(context.Background(), p); got != "audio/mp4" {
		t.Fatalf("directFileMime = %q, want audio/mp4 for an m4a with a readable length", got)
	}
	if got := s.libraryPresetLength(p); got != 56*time.Minute {
		t.Fatalf("length = %v, want the 56 min from the movie header", got)
	}
}

func TestM4ALengthWithTheMovieHeaderAfterTheAudio(t *testing.T) {
	useLoopbackProbeClient(t)
	var hits atomic.Int32
	body := m4aFile(3*time.Minute, false, 200*1024)
	nas := nasFile(t, body, &hits)
	defer nas.Close()

	got := mp4Length(context.Background(), customFileProbeClient(), nas.URL+"/a.m4a", int64(len(body)))
	if got != 3*time.Minute {
		t.Fatalf("length = %v, want 3m0s from a moov behind the mdat", got)
	}
}

// The regression itself: since v1.0.19 an m4a whose length STR could not tell
// was handed to the speaker as an open-ended item, which an ST10 played for a
// few seconds, dropped and started over, again and again. Without a length it
// now plays through the stream proxy, as it did up to v1.0.18.
func TestCustomURLToAFileOfUnknownLengthStaysOnTheProxy(t *testing.T) {
	useLoopbackProbeClient(t)
	var hits atomic.Int32
	// Raw ADTS AAC under an .m4a name: no MP4 boxes, nothing to read a
	// length from.
	adts := make([]byte, 64*1024)
	for i := 0; i+7 <= len(adts); i += 512 {
		copy(adts[i:], []byte{0xFF, 0xF1, 0x4C, 0x80, 0x40, 0x1F, 0xFC})
	}
	nas := nasFile(t, adts, &hits)
	defer nas.Close()
	s, _ := newPlayTestServer(t)

	p := customPreset(3, nas.URL+"/kapitel.m4a")
	if got := s.directFileMime(context.Background(), p); got != "" {
		t.Fatalf("directFileMime = %q, want \"\" (proxy) for a file of unknown length", got)
	}
	// The hardware key is not claimed either: the caller's radio recall
	// plays the slot through the proxy.
	if err := s.presets.SetSlot(p); err != nil {
		t.Fatalf("SetSlot: %v", err)
	}
	if s.RecallSlot(context.Background(), 3) {
		t.Fatal("the hardware key of a file of unknown length was claimed for the direct play")
	}
}

func TestFLACLengthFromStreamInfo(t *testing.T) {
	if got := flacLengthFrom(flacFile(44100, 44100*200)); got != 200*time.Second {
		t.Fatalf("length = %v, want 200s", got)
	}
	if got := flacLengthFrom([]byte("not a flac file at all, just some text bytes here")); got != 0 {
		t.Fatalf("length = %v for a non-FLAC, want 0", got)
	}
}

func TestMP4LengthRejectsGarbage(t *testing.T) {
	// Each is the body of a moov: what mp4MvhdLength reads.
	for name, body := range map[string][]byte{
		"html":            []byte("<!doctype html><html>not audio</html>"),
		"truncated":       {0, 0, 0},
		"unknown version": mp4Box("mvhd", append([]byte{7}, make([]byte, 99)...)),
		"zero timescale":  mvhdV0(0, 1000),
		"no mvhd":         mp4Box("trak", make([]byte, 40)),
	} {
		if got := mp4MvhdLength(body); got != 0 {
			t.Errorf("%s: length = %v, want 0", name, got)
		}
	}
	if _, _, _, ok := mp4BoxHeader([]byte{0xFF, 0xFF, 0xFF, 0xFF, 'm', 'o', 'o', 'v'}, 100); ok {
		t.Error("a box larger than the file was accepted")
	}
}

// A custom file the speaker drops early goes back to the proxy for the rest
// of the agent run, so the key does not fail the same way on every press.
func TestACustomFileTheSpeakerDropsEarlyGoesBackToTheProxy(t *testing.T) {
	useLoopbackProbeClient(t)
	var hits atomic.Int32
	nas := nasFile(t, m4aFile(56*time.Minute, true, 64*1024), &hits)
	defer nas.Close()
	s, _ := newPlayTestServer(t)
	p := customPreset(3, nas.URL+"/kapitel.m4a")
	if got := s.directFileMime(context.Background(), p); got != "audio/mp4" {
		t.Fatalf("directFileMime = %q before the drop", got)
	}

	// A drop long into the file is not the speaker refusing it.
	s.demoteCustomFile(p.StreamURL, "test", 10*time.Minute)
	if got := s.directFileMime(context.Background(), p); got != "audio/mp4" {
		t.Fatalf("a late drop demoted the file: directFileMime = %q", got)
	}
	s.demoteCustomFile(p.StreamURL, "test", 5*time.Second)
	before := hits.Load()
	if got := s.directFileMime(context.Background(), p); got != "" {
		t.Fatalf("directFileMime = %q after an early drop, want the proxy", got)
	}
	if hits.Load() != before {
		t.Error("the demoted verdict was probed again instead of being kept")
	}
}
