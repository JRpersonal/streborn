package streamproxy

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The fixtures in testdata/fmp4 were made with ffmpeg 8.1 (a 2 s 440 Hz
// tone, 32 kbit/s mono, 44.1 kHz, one-second fMP4 HLS segments):
//
//	ffmpeg -f lavfi -i sine=frequency=440:duration=2 -c:a aac -b:a 32k -ac 1 -ar 44100 \
//	  -f hls -hls_time 1 -hls_segment_type fmp4 -hls_playlist_type vod \
//	  -hls_fmp4_init_filename init.mp4 -hls_segment_filename seg%d.m4s aac.m3u8
//	ffmpeg -i aac.m3u8 -c copy -f adts aac-expected.adts
//
// and the same with libmp3lame (mp3init.mp4, mp3seg%d.m4s, mp3.m3u8,
// mp3-expected.mp3 written with -write_xing 0 -id3v2_version 0). The
// expected files are ffmpeg's own repackaging of the very same frames, so the
// converter has to match them byte for byte.

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "fmp4", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func convertAll(t *testing.T, init string, segs ...string) []byte {
	t.Helper()
	tr, err := parseFMP4Init(fixture(t, init))
	if err != nil {
		t.Fatalf("init %s: %v", init, err)
	}
	var out []byte
	for _, s := range segs {
		a, ok := fmp4ToAudio(fixture(t, s), tr)
		if !ok {
			t.Fatalf("segment %s: no audio", s)
		}
		out = append(out, a...)
	}
	return out
}

func TestFMP4AACMatchesFFmpegADTS(t *testing.T) {
	tr, err := parseFMP4Init(fixture(t, "init.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	// AAC LC (profile 1), 44.1 kHz (index 4), mono.
	if tr.codec != fmp4AAC || tr.profile != 1 || tr.freqIdx != 4 || tr.channels != 1 {
		t.Fatalf("track = %+v, want AAC LC 44.1 kHz mono", tr)
	}
	got := convertAll(t, "init.mp4", "seg0.m4s", "seg1.m4s", "seg2.m4s")
	want := fixture(t, "aac-expected.adts")
	if !bytes.Equal(got, want) {
		t.Fatalf("ADTS differs from ffmpeg's: got %d bytes, want %d (first diff at %d)", len(got), len(want), firstDiff(got, want))
	}
}

func TestFMP4MP3PassesFramesThrough(t *testing.T) {
	tr, err := parseFMP4Init(fixture(t, "mp3init.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if tr.codec != fmp4MP3 {
		t.Fatalf("codec = %v, want MP3", tr.codec)
	}
	got := convertAll(t, "mp3init.mp4", "mp3seg0.m4s", "mp3seg1.m4s", "mp3seg2.m4s")
	want := fixture(t, "mp3-expected.mp3")
	if !bytes.Equal(got, want) {
		t.Fatalf("MP3 differs from ffmpeg's: got %d bytes, want %d (first diff at %d)", len(got), len(want), firstDiff(got, want))
	}
}

func firstDiff(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	if len(a) != len(b) {
		if len(a) < len(b) {
			return len(a)
		}
		return len(b)
	}
	return -1
}

// An encrypted track (SoundCloud Go+, any DRM) must be refused, not sent as
// noise.
func TestFMP4EncryptedTrackRefused(t *testing.T) {
	init := bytes.Replace(fixture(t, "init.mp4"), []byte("mp4a"), []byte("enca"), 1)
	if _, err := parseFMP4Init(init); !errors.Is(err, errFMP4Encrypted) {
		t.Fatalf("err = %v, want errFMP4Encrypted", err)
	}
	opus := bytes.Replace(fixture(t, "init.mp4"), []byte("mp4a"), []byte("Opus"), 1)
	if _, err := parseFMP4Init(opus); !errors.Is(err, errFMP4Unsupported) {
		t.Fatalf("Opus: err = %v, want errFMP4Unsupported", err)
	}
	if _, err := parseFMP4Init(fixture(t, "seg0.m4s")); !errors.Is(err, errFMP4NoAudio) {
		t.Fatalf("segment without moov: err = %v, want errFMP4NoAudio", err)
	}
}

// Another track's fragments are not ours: nothing comes out.
func TestFMP4OtherTrackIgnored(t *testing.T) {
	tr, err := parseFMP4Init(fixture(t, "init.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	tr.trackID += 7
	if out, ok := fmp4ToAudio(fixture(t, "seg0.m4s"), tr); ok || len(out) != 0 {
		t.Fatalf("got %d bytes for a foreign track", len(out))
	}
}

// HE-AAC with explicit SBR signalling: ADTS carries the core codec (LC) at
// the core rate, and the decoder finds SBR in the stream.
func TestParseASCHEAAC(t *testing.T) {
	// objType 5 (SBR), freq index 6 (24 kHz core), channels 2,
	// extension freq index 3 (48 kHz), core objType 2 (LC).
	// bits: 00101 0110 0010 0011 00010 -> 0x2B 0x11 0x88 0x00
	var tr fmp4Track
	if !parseASC([]byte{0x2B, 0x11, 0x88, 0x00}, &tr) {
		t.Fatal("parseASC failed")
	}
	if tr.profile != 1 || tr.freqIdx != 6 || tr.channels != 2 {
		t.Fatalf("got profile %d freqIdx %d channels %d, want 1/6/2", tr.profile, tr.freqIdx, tr.channels)
	}
	// AAC LC 48 kHz stereo: 00010 0011 0010 -> 0x11 0x90
	tr = fmp4Track{}
	if !parseASC([]byte{0x11, 0x90}, &tr) || tr.profile != 1 || tr.freqIdx != 3 || tr.channels != 2 {
		t.Fatalf("LC 48k stereo: %+v", tr)
	}
	if parseASC([]byte{0x11}, &tr) {
		t.Fatal("a truncated config must fail")
	}
}

func TestADTSHeader(t *testing.T) {
	h := adtsHeader(fmp4Track{profile: 1, freqIdx: 4, channels: 2}, 371)
	want := [7]byte{0xFF, 0xF1, 0x50, 0x80, 0x2F, 0x5F, 0xFC}
	if h != want {
		t.Fatalf("header = % X, want % X", h, want)
	}
}

func TestParseMediaPlaylistInitURL(t *testing.T) {
	body := "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:1,\nseg0.m4s\n#EXT-X-ENDLIST\n"
	pl := parseMediaPlaylist(body, "http://radio.example/live/index.m3u8")
	if !pl.isFMP4 || pl.initURL != "http://radio.example/live/init.mp4" {
		t.Fatalf("isFMP4 %v initURL %q", pl.isFMP4, pl.initURL)
	}
	ranged := parseMediaPlaylist("#EXTM3U\n#EXT-X-MAP:URI=\"all.mp4\",BYTERANGE=\"720@0\"\nall.mp4\n", "http://radio.example/a.m3u8")
	if !ranged.isFMP4 || ranged.initURL != "" {
		t.Fatalf("byte-range map: isFMP4 %v initURL %q, want true and empty", ranged.isFMP4, ranged.initURL)
	}
	if got := hlsAttr(`#EXT-X-MAP:BYTERANGE="1@0",URI="x,y.mp4"`, "uri"); got != "x,y.mp4" {
		t.Fatalf("hlsAttr = %q", got)
	}
}

// End to end: an fMP4 HLS playlist served over http comes out of serveHLS as
// the ADTS stream ffmpeg would write.
func TestServeHLSFMP4(t *testing.T) {
	files := map[string][]byte{}
	for _, n := range []string{"aac.m3u8", "init.mp4", "seg0.m4s", "seg1.m4s", "seg2.m4s"} {
		files["/"+n] = fixture(t, n)
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	defer up.Close()

	s := New(nil, silentLogger())
	s.client = &http.Client{} // allow the loopback dial past the SSRF guard
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/stream", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	if err := s.serveHLS(ctx, rec, req, up.URL+"/aac.m3u8"); err != nil {
		t.Fatalf("serveHLS: %v", err)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "audio/aac" {
		t.Fatalf("Content-Type %q", ct)
	}
	if !bytes.Equal(rec.Body.Bytes(), fixture(t, "aac-expected.adts")) {
		t.Fatalf("stream differs from ffmpeg's ADTS: %d bytes", rec.Body.Len())
	}

	// An encrypted init is reported as not playable, with the reason.
	files["/init.mp4"] = bytes.Replace(fixture(t, "init.mp4"), []byte("mp4a"), []byte("enca"), 1)
	rec = httptest.NewRecorder()
	err := s.serveHLS(ctx, rec, req, up.URL+"/aac.m3u8")
	if err == nil || !strings.Contains(err.Error(), "encrypted") {
		t.Fatalf("encrypted init: err = %v", err)
	}
}

func FuzzFMP4(f *testing.F) {
	for _, n := range []string{"init.mp4", "seg0.m4s", "mp3init.mp4", "mp3seg0.m4s"} {
		b, err := os.ReadFile(filepath.Join("testdata", "fmp4", n))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	tr := fmp4Track{trackID: 1, codec: fmp4AAC, profile: 1, freqIdx: 4, channels: 1}
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = parseFMP4Init(b)
		_, _ = fmp4ToAudio(b, tr)
	})
}
