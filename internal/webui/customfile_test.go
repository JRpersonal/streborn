package webui

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
)

// useLoopbackProbeClient lets the probe reach a test server (the real client
// refuses loopback) for the duration of the test.
func useLoopbackProbeClient(t *testing.T) {
	t.Helper()
	prev := customFileProbeClient
	customFileProbeClient = func() *http.Client { return &http.Client{Timeout: customFileProbeStep} }
	t.Cleanup(func() { customFileProbeClient = prev })
}

// cbrMP3 builds an MP3 file: an ID3v2 tag of tagBody bytes (0 for none), then
// frames CBR frames of MPEG-1 Layer III, 128 kbps, 44.1 kHz, stereo.
func cbrMP3(tagBody, frames int) []byte {
	var out []byte
	if tagBody > 0 {
		h := []byte{'I', 'D', '3', 4, 0, 0,
			byte(tagBody >> 21 & 0x7F), byte(tagBody >> 14 & 0x7F), byte(tagBody >> 7 & 0x7F), byte(tagBody & 0x7F)}
		out = append(out, h...)
		out = append(out, make([]byte, tagBody)...)
	}
	frame := make([]byte, 417) // 144 * 128000 / 44100
	copy(frame, []byte{0xFF, 0xFB, 0x90, 0x00})
	for i := 0; i < frames; i++ {
		out = append(out, frame...)
	}
	return out
}

// nasFile serves body the way a NAS web server does: a size, byte ranges, and
// a HEAD that answers like the GET. It counts requests.
func nasFile(t *testing.T, body []byte, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("Accept-Ranges", "bytes")
		var from, to int
		if n, _ := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &from, &to); n >= 1 {
			if n == 1 || to >= len(body) {
				to = len(body) - 1
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, to, len(body)))
			w.Header().Set("Content-Length", strconv.Itoa(to-from+1))
			w.WriteHeader(http.StatusPartialContent)
			if r.Method != http.MethodHead {
				_, _ = w.Write(body[from : to+1])
			}
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(body)
		}
	}))
}

// radioStation answers like Icecast: icy headers, no size, an endless body.
func radioStation(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("icy-name", "NDR 2")
		w.Header().Set("icy-br", "128")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}
		chunk := make([]byte, 4096)
		for r.Context().Err() == nil {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}))
}

func customPreset(slot int, url string) presets.Preset {
	return presets.Preset{Slot: slot, Name: "Custom", Type: "radio", StreamURL: url}
}

func TestClassifyFileResponse(t *testing.T) {
	mk := func(code int, h map[string]string) *http.Response {
		r := &http.Response{StatusCode: code, Header: http.Header{}}
		for k, v := range h {
			r.Header.Set(k, v)
		}
		return r
	}
	cases := []struct {
		name   string
		resp   *http.Response
		ranged bool
		want   fileVerdict
		size   int64
	}{
		{"HEAD: NAS file", mk(200, map[string]string{"Content-Length": "5000", "Accept-Ranges": "bytes"}), false, fileFinite, 5000},
		{"HEAD: no Accept-Ranges is not proof", mk(200, map[string]string{"Content-Length": "5000"}), false, fileUnknown, 0},
		{"HEAD: no size is not proof", mk(200, map[string]string{"Accept-Ranges": "bytes"}), false, fileUnknown, 0},
		{"HEAD: 405", mk(405, nil), false, fileUnknown, 0},
		{"HEAD: icy headers are a station", mk(200, map[string]string{"icy-name": "x", "Content-Length": "5000", "Accept-Ranges": "bytes"}), false, fileStream, 0},
		{"GET: 206 with total", mk(206, map[string]string{"Content-Range": "bytes 0-0/9000"}), true, fileFinite, 9000},
		{"GET: 206 with unknown total", mk(206, map[string]string{"Content-Range": "bytes 0-0/*"}), true, fileUnknown, 0},
		{"GET: range ignored", mk(200, map[string]string{"Content-Length": "9000"}), true, fileStream, 0},
		{"GET: open-ended stream", mk(200, nil), true, fileStream, 0},
		{"GET: icy-metaint", mk(206, map[string]string{"icy-metaint": "16000", "Content-Range": "bytes 0-0/9000"}), true, fileStream, 0},
		{"GET: an html page", mk(206, map[string]string{"Content-Type": "text/html", "Content-Range": "bytes 0-0/9000"}), true, fileStream, 0},
	}
	for _, c := range cases {
		v, size := classifyFileResponse(c.resp, c.ranged)
		if v != c.want || size != c.size {
			t.Errorf("%s: got %v/%d, want %v/%d", c.name, v, size, c.want, c.size)
		}
	}
}

// The #830 file: a podcast MP3 on a NAS, typed in as a custom URL. It is
// handed to the speaker directly, with the length read from the file.
func TestCustomURLToANASFileGoesDirect(t *testing.T) {
	useLoopbackProbeClient(t)
	var hits atomic.Int32
	body := cbrMP3(0, 1000) // 1000 * 1152 / 44100 = 26.1 s
	nas := nasFile(t, body, &hits)
	defer nas.Close()
	s, _ := newPlayTestServer(t)

	p := customPreset(2, nas.URL+"/podcast/latest.mp3")
	if got := s.directFileMime(context.Background(), p); got != "audio/mpeg" {
		t.Fatalf("directFileMime = %q, want audio/mpeg for a finite file", got)
	}
	if got := s.libraryPresetLength(p); got < 25*time.Second || got > 27*time.Second {
		t.Errorf("length = %v, want about 26 s from the file's size and bitrate", got)
	}
	// Cached: the next press costs no request.
	before := hits.Load()
	s.directFileMime(context.Background(), p)
	if hits.Load() != before {
		t.Errorf("a second recall probed again (%d more requests)", hits.Load()-before)
	}
}

// A live station whose URL ends in .mp3 stays on the stream proxy.
func TestCustomURLToAStationEndingInMP3StaysOnTheProxy(t *testing.T) {
	useLoopbackProbeClient(t)
	var hits atomic.Int32
	st := radioStation(t, &hits)
	defer st.Close()
	s, _ := newPlayTestServer(t)

	p := customPreset(1, st.URL+"/ndr2.mp3")
	if got := s.directFileMime(context.Background(), p); got != "" {
		t.Fatalf("directFileMime = %q, want \"\" (proxy) for a live station", got)
	}
	before := hits.Load()
	s.directFileMime(context.Background(), p)
	if hits.Load() != before {
		t.Error("a station's verdict was not cached: every press would probe it again")
	}
}

// A server that does not answer at all keeps today's path, and is not asked
// again on the next press.
func TestCustomURLProbeFailureKeepsTheProxy(t *testing.T) {
	useLoopbackProbeClient(t)
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL + "/gone.mp3"
	dead.Close()
	s, _ := newPlayTestServer(t)

	if got := s.directFileMime(context.Background(), customPreset(1, url)); got != "" {
		t.Fatalf("directFileMime = %q after a failed probe, want the proxy", got)
	}
	if fp, ok := s.fileProbes.get(url, time.Now()); !ok || fp.verdict != fileUnknown {
		t.Fatalf("failed probe not cached: %+v, %v", fp, ok)
	}
	if _, ok := s.fileProbes.get(url, time.Now().Add(customFileRetryAfter)); ok {
		t.Error("an inconclusive verdict must expire so a NAS that was asleep gets another chance")
	}
}

// Only a hand-typed http URL with an audio extension is probed at all.
func TestCustomFileMimeNeverProbesOtherPresets(t *testing.T) {
	prev := customFileProbeClient
	customFileProbeClient = func() *http.Client {
		t.Error("probe ran for a preset that must not be probed")
		return &http.Client{}
	}
	t.Cleanup(func() { customFileProbeClient = prev })
	s, _ := newPlayTestServer(t)
	for _, p := range []presets.Preset{
		{Type: "radio", StreamURL: "https://nas.example/a.mp3"},
		{Type: "radio", StreamURL: "http://stream.example/live"},
		{Type: "spotify", StreamURL: "http://nas.example/a.mp3"},
		{Type: "queue", StreamURL: "http://nas.example/a.mp3"},
	} {
		if got := s.customFileMime(context.Background(), p); got != "" {
			t.Errorf("%+v: customFileMime = %q, want \"\"", p, got)
		}
	}
}

// The speaker's key on a custom file preset plays the file directly too.
func TestHardwareKeyOnCustomFilePlaysTheFileDirectly(t *testing.T) {
	useLoopbackProbeClient(t)
	var hits atomic.Int32
	nas := nasFile(t, cbrMP3(0, 1000), &hits)
	defer nas.Close()
	s, rec := newPlayTestServer(t)
	url := nas.URL + "/podcast/latest.mp3"
	if err := s.presets.SetSlot(customPreset(4, url)); err != nil {
		t.Fatalf("SetSlot: %v", err)
	}

	if !s.RecallSlot(context.Background(), 4) {
		t.Fatal("RecallSlot did not claim the custom file key")
	}
	if !rec.has("SetAVTransportURI") {
		t.Fatalf("the file was not pushed to the speaker: %v", rec.list())
	}
	s.lastPlayMu.Lock()
	lp := s.lastPlay
	s.lastPlayMu.Unlock()
	if lp == nil || lp.boxURL != url || lp.mime != "audio/mpeg" {
		t.Fatalf("last play = %+v, want the file itself with audio/mpeg", lp)
	}
}

func TestEstimateMP3LengthSkipsALargeID3Tag(t *testing.T) {
	useLoopbackProbeClient(t)
	var hits atomic.Int32
	body := cbrMP3(300*1024, 2000) // cover art bigger than the first read
	nas := nasFile(t, body, &hits)
	defer nas.Close()

	got := estimateMP3Length(context.Background(), customFileProbeClient(), nas.URL+"/a.mp3", int64(len(body)))
	want := time.Duration(float64(2000*417*8) / 128000 * float64(time.Second))
	if d := got - want; d < -time.Second || d > time.Second {
		t.Fatalf("length = %v, want about %v", got, want)
	}
}

func TestMP3LengthFromXingFrameCount(t *testing.T) {
	buf := cbrMP3(0, 3)
	off := 4 + 32 // MPEG-1 stereo side information
	copy(buf[off:], "Info")
	binary.BigEndian.PutUint32(buf[off+4:], 0x01)
	binary.BigEndian.PutUint32(buf[off+8:], 5000)
	got := mp3LengthFromFrames(buf, 1) // the size must not matter with a frame count
	want := time.Duration(5000 * 1152 * int64(time.Second) / 44100)
	if d := got - want; d < -time.Millisecond || d > time.Millisecond {
		t.Fatalf("length = %v, want %v", got, want)
	}
}

func TestMP3LengthRejectsNonAudio(t *testing.T) {
	if got := mp3LengthFromFrames([]byte("<!doctype html><html>not audio</html>"), 1<<20); got != 0 {
		t.Fatalf("length = %v for an html page, want 0", got)
	}
}
