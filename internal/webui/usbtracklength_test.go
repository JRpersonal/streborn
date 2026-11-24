package webui

import (
	"context"
	"encoding/binary"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// wavFile is a WAV of the given length at 44.1 kHz, 16 bit stereo, with a
// LIST chunk of tags between fmt and data, as taggers write it. dataSize
// overrides the size the data chunk states (0 or 0xFFFFFFFF: a streamed file).
func wavFile(length time.Duration, dataSize uint32, override bool) []byte {
	const byteRate = 44100 * 2 * 2
	n := int(length/time.Second) * byteRate
	b := []byte("RIFF\x00\x00\x00\x00WAVE")
	fmtc := make([]byte, 8+16)
	copy(fmtc, "fmt ")
	binary.LittleEndian.PutUint32(fmtc[4:], 16)
	binary.LittleEndian.PutUint16(fmtc[8:], 1)
	binary.LittleEndian.PutUint16(fmtc[10:], 2)
	binary.LittleEndian.PutUint32(fmtc[12:], 44100)
	binary.LittleEndian.PutUint32(fmtc[16:], byteRate)
	binary.LittleEndian.PutUint16(fmtc[20:], 4)
	binary.LittleEndian.PutUint16(fmtc[22:], 16)
	b = append(b, fmtc...)
	list := append([]byte("LIST\x05\x00\x00\x00INFOx"), 0) // odd size, padded
	b = append(b, list...)
	data := make([]byte, 8)
	copy(data, "data")
	stated := uint32(n)
	if override {
		stated = dataSize
	}
	binary.LittleEndian.PutUint32(data[4:], stated)
	b = append(b, data...)
	return append(b, make([]byte, n)...)
}

// oggPage is one Ogg page with the given granule and one packet.
func oggPage(granule int64, packet []byte) []byte {
	h := make([]byte, 27)
	copy(h, "OggS")
	binary.LittleEndian.PutUint64(h[6:], uint64(granule))
	h[26] = 1
	return append(append(h, byte(len(packet))), packet...)
}

// vorbisFile is an Ogg Vorbis file of the given length at 44.1 kHz: the
// identification header page, audio, and a last page whose granule is the
// sample count. An end-of-stream page with no packet end (-1) follows it.
func vorbisFile(length time.Duration) []byte {
	id := make([]byte, 30)
	id[0] = 1
	copy(id[1:], "vorbis")
	binary.LittleEndian.PutUint32(id[12:], 44100)
	out := oggPage(0, id)
	out = append(out, make([]byte, 100*1024)...)
	out = append(out, oggPage(int64(length/time.Second)*44100, []byte("audio"))...)
	return append(out, oggPage(-1, []byte("x"))...)
}

func TestWAVLengthComesFromTheDataChunk(t *testing.T) {
	f := wavFile(200*time.Second, 0, false)
	if d := wavLengthFrom(f[:wavHeadRead], int64(len(f))); d != 200*time.Second {
		t.Fatalf("got %v, want 200s", d)
	}
	for _, stated := range []uint32{0, 0xFFFFFFFF} {
		f := wavFile(30*time.Second, stated, true)
		if d := wavLengthFrom(f[:wavHeadRead], int64(len(f))); d != 30*time.Second {
			t.Errorf("a streamed WAV (data size %#x): got %v, want the rest of the file, 30s", stated, d)
		}
	}
	// A truncated copy states more than it holds: the file is what plays.
	f = wavFile(30*time.Second, 1<<30, true)
	if d := wavLengthFrom(f[:wavHeadRead], int64(len(f))); d != 30*time.Second {
		t.Errorf("a truncated WAV: got %v, want 30s", d)
	}
	if d := wavLengthFrom([]byte("RIFF\x00\x00\x00\x00AVI LIST"), 1000); d != 0 {
		t.Errorf("not a WAV: %v", d)
	}
}

func TestOggVorbisLengthComesFromTheLastGranule(t *testing.T) {
	f := vorbisFile(245 * time.Second)
	if rate := vorbisRate(f[:oggHeadRead]); rate != 44100 {
		t.Fatalf("rate %d", rate)
	}
	if g := lastOggGranule(f[len(f)-oggTailRead:]); g != 245*44100 {
		t.Fatalf("granule %d", g)
	}
	opus := oggPage(0, append([]byte("OpusHead"), make([]byte, 20)...))
	if rate := vorbisRate(opus); rate != 0 {
		t.Errorf("an Opus stream is not read as Vorbis: %d", rate)
	}
}

func TestStickFileURLsAreThisSpeakersOrAnotherSTRSpeakers(t *testing.T) {
	cases := []struct {
		url     string
		own, ok bool
		why     string
	}{
		{usbLoopbackBase + "/usb/file/Album%201/a.flac", true, true, "this speaker's stick, loopback"},
		{"http://192.168.1.213:8888/usb/file/a.mp3", false, true, "another speaker's stick on :8888"},
		{"http://10.0.0.7:17008/usb/file/a.mp3", false, true, "another speaker's stick on :17008"},
		{"http://192.168.1.213:8888/usb/art/cover.jpg", false, false, "a cover, no song"},
		{"http://192.168.1.213:8888/usb/file/", false, false, "no file"},
		{"http://192.168.1.20:8200/usb/file/a.mp3", false, false, "not an agent port"},
		{"http://203.0.113.5:8888/usb/file/a.mp3", false, false, "not on the LAN"},
		{"https://192.168.1.213:8888/usb/file/a.mp3", false, false, "not plain http"},
		{"http://192.168.1.20:8200/MediaItems/1.mp3", false, false, "a NAS song"},
	}
	for _, c := range cases {
		own, ok := stickFileURL(c.url)
		if own != c.own || ok != c.ok {
			t.Errorf("%s (%s): got own=%v ok=%v", c.why, c.url, own, ok)
		}
	}
}

// putFile writes body at rel under root.
func putFile(t *testing.T, root, rel string, body []byte) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Discussion #1136 (ST20, 2026-11-22): no progress bar for a song from the
// stick. The stick's listing carries no duration, so the client sent none and
// the speaker reported a total of zero. The agent now reads the length from
// the file and hands it to the speaker, for a single play and for every track
// of a folder.
func TestAStickSongIsHandedToTheSpeakerWithItsLength(t *testing.T) {
	f := newFakeStick(t, "spotty")
	media := t.TempDir()
	putFile(t, media, "Album 1/02 b.flac", flacFile(44100, 44100*200))
	putFile(t, media, "Album 1/03 c.wav", wavFile(95*time.Second, 0, false))
	putFile(t, media, "Album 1/04 d.ogg", vorbisFile(245*time.Second))
	f.plug(fatSector("fat32", 64))
	f.firmwareMount(media, "vfat")
	s, rec := newDIDLTestServer(t)
	// Close the stick before the temp dirs go: Windows cannot remove an open one.
	t.Cleanup(func() {
		s.usb.mu.Lock()
		s.usbDropLocked("test end")
		s.usb.mu.Unlock()
	})
	srv := startUSBAgent(t, s)
	flac := srv.URL + "/usb/file/Album%201/02%20b.flac"

	w := postPlayBody(t, s, `{"url":"`+flac+`","title":"02 b","mime":"audio/flac"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("play: %d %s", w.Code, w.Body.String())
	}
	if body := rec.first(); !strings.Contains(body, "0:03:20.000") {
		t.Fatalf("the speaker was handed the song without its length:\n%s", body)
	}

	// A folder: each track is pushed with the length read from its file, and
	// the queue keeps it for the watcher.
	ctx := context.Background()
	items := toQueueItems([]queueStartItem{
		{URL: srv.URL + "/usb/file/Album%201/03%20c.wav", Title: "03 c"},
		{URL: srv.URL + "/usb/file/Album%201/04%20d.ogg", Title: "04 d"},
	})
	if err := s.startQueue(ctx, items, 0, false, repeatOff, recentCardCtx{}); err != nil {
		t.Fatal(err)
	}
	if got := queueItemLength(s, items[0].URL); got != 95*time.Second {
		t.Fatalf("the WAV's queue length is %v, want 95s", got)
	}
	if got := s.withStickLength(ctx, items[1]).Duration; got != 245*time.Second {
		t.Fatalf("the Ogg's length is %v, want 245s", got)
	}
	if got := queueItemLength(s, items[1].URL); got != 245*time.Second {
		t.Fatalf("the Ogg's queue length is %v, want 245s", got)
	}

	// A NAS song keeps what its server said, and nothing is read for it.
	if d := s.stickFileLength(ctx, "http://192.168.1.20:8200/MediaItems/1.mp3", "audio/mpeg"); d != 0 {
		t.Fatalf("a NAS song was read: %v", d)
	}
}
