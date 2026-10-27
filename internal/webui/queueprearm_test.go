package webui

import (
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/upnp"
)

const (
	prearmFirstURL  = "http://192.0.2.10:49200/AUDIO/1.mp3"
	prearmSecondURL = "http://192.0.2.10:49200/AUDIO/2.mp3"
)

// prearmServer is a play test server with a two-track album queue whose second
// track carries no length, and an MP3 length read scripted by probe.
func prearmServer(t *testing.T, mime string, probe func(context.Context, string) (time.Duration, bool)) *Server {
	t.Helper()
	s, _ := newPlayTestServer(t)
	s.prearmProbeFn = probe
	s.queue.load([]queueItem{
		{URL: prearmFirstURL, Title: "01 First", Mime: "audio/mpeg", Duration: 3 * time.Minute},
		{URL: prearmSecondURL, Title: "02 Second", Mime: mime},
	}, 0, false, repeatOff)
	return s
}

func queueItemLength(s *Server, url string) time.Duration {
	items, _, _ := s.queue.contents()
	for _, it := range items {
		if it.URL == url {
			return it.Duration
		}
	}
	return 0
}

func waitPrearm(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", why)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPrearmGivesTheNextTrackAFrameCountLength(t *testing.T) {
	t.Parallel()
	s := prearmServer(t, "audio/mpeg", func(context.Context, string) (time.Duration, bool) {
		return 200 * time.Second, true
	})
	s.prearmNextLength()
	waitPrearm(t, "the next track to get its length", func() bool { return queueItemLength(s, prearmSecondURL) > 0 })
	if got := queueItemLength(s, prearmSecondURL); got != 200*time.Second {
		t.Fatalf("length = %v, want the 200 s counted from the frame header", got)
	}
	if h := s.lengthHints.get(prearmSecondURL); h != 0 {
		t.Fatalf("an exact length was also kept as a hint: %v", h)
	}
}

func TestPrearmUsesALearnedLengthFirst(t *testing.T) {
	t.Parallel()
	s := prearmServer(t, "audio/mpeg", func(context.Context, string) (time.Duration, bool) {
		t.Error("the media server was read although a length was already known")
		return 0, false
	})
	s.trackLens.put(prearmSecondURL, 181*time.Second)
	s.prearmNextLength()
	waitPrearm(t, "the next track to get its length", func() bool { return queueItemLength(s, prearmSecondURL) > 0 })
	if got := queueItemLength(s, prearmSecondURL); got != 181*time.Second {
		t.Fatalf("length = %v, want the learned 181 s", got)
	}
}

func TestPrearmReadsNothingForANonMP3(t *testing.T) {
	t.Parallel()
	called := make(chan struct{}, 1)
	s := prearmServer(t, "audio/x-flac", func(context.Context, string) (time.Duration, bool) {
		called <- struct{}{}
		return 0, false
	})
	it, _ := s.queue.peekNatural()
	if d, src := s.lookupQueueLength(context.Background(), it); d != 0 || src != prearmNone {
		t.Fatalf("lookup = %v %s, want none", d, src)
	}
	select {
	case <-called:
		t.Fatal("a FLAC was read as an MP3")
	default:
	}
}

// soapBodies records the SOAP bodies a renderer was sent, by action.
type soapBodies struct {
	mu     sync.Mutex
	bodies map[string][]string
}

func (b *soapBodies) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	action := r.Header.Get("SOAPACTION")
	if i := strings.Index(action, "#"); i >= 0 {
		action = strings.Trim(action[i+1:], `"`)
	}
	body, _ := io.ReadAll(r.Body)
	b.mu.Lock()
	if b.bodies == nil {
		b.bodies = map[string][]string{}
	}
	b.bodies[action] = append(b.bodies[action], string(body))
	b.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (b *soapBodies) last(action string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	l := b.bodies[action]
	if len(l) == 0 {
		return ""
	}
	return l[len(l)-1]
}

// A bitrate x size guess is wrong for a VBR file without a frame header, and a
// wrong length handed to the box lets the track be ended at the wrong place.
// It stays a hint for the watcher and never reaches the box.
func TestABitrateGuessIsNeverSentToTheBoxAsALength(t *testing.T) {
	t.Parallel()
	s := prearmServer(t, "audio/mpeg", func(context.Context, string) (time.Duration, bool) {
		return 200 * time.Second, false
	})
	box := &soapBodies{}
	srv := httptest.NewServer(box)
	t.Cleanup(srv.Close)
	s.renderer = &upnp.Renderer{ControlURL: srv.URL, Client: srv.Client()}

	s.prearmNextLength()
	waitPrearm(t, "the hint to be stored", func() bool { return s.lengthHints.get(prearmSecondURL) > 0 })
	if got := queueItemLength(s, prearmSecondURL); got != 0 {
		t.Fatalf("the guess became the track's length: %v", got)
	}

	s.setQueueTiming(3 * time.Minute)
	s.queueMu.Lock()
	gen := s.queueGen
	s.queueMu.Unlock()
	s.advanceAndPlay(true, gen, "test: the first track ended")
	body := box.last("SetAVTransportURI")
	if !strings.Contains(body, "2.mp3") {
		t.Fatalf("the second track was not pushed; body:\n%s", body)
	}
	if strings.Contains(body, "duration=") {
		t.Fatalf("the guessed length was handed to the box; body:\n%s", body)
	}
	s.queueMu.Lock()
	dur := s.queueTrackDur
	s.queueMu.Unlock()
	if dur != 0 {
		t.Fatalf("the watcher times the track by the guess: %v", dur)
	}
}

// A length counted from the frame header is sent, so the box reports a total.
func TestAFrameCountLengthIsSentToTheBox(t *testing.T) {
	t.Parallel()
	s := prearmServer(t, "audio/mpeg", func(context.Context, string) (time.Duration, bool) {
		return 200 * time.Second, true
	})
	box := &soapBodies{}
	srv := httptest.NewServer(box)
	t.Cleanup(srv.Close)
	s.renderer = &upnp.Renderer{ControlURL: srv.URL, Client: srv.Client()}

	s.prearmNextLength()
	waitPrearm(t, "the next track to get its length", func() bool { return queueItemLength(s, prearmSecondURL) > 0 })
	s.setQueueTiming(3 * time.Minute)
	s.queueMu.Lock()
	gen := s.queueGen
	s.queueMu.Unlock()
	s.advanceAndPlay(true, gen, "test: the first track ended")
	if body := box.last("SetAVTransportURI"); !strings.Contains(body, "duration=") {
		t.Fatalf("the counted length was not handed to the box; body:\n%s", body)
	}
}

func TestPeekNatural(t *testing.T) {
	t.Parallel()
	items := []queueItem{{URL: "a"}, {URL: "b"}, {URL: "c"}}
	q := newPlayQueue()
	q.load(items, 0, false, repeatOff)
	if it, ok := q.peekNatural(); !ok || it.URL != "b" {
		t.Fatalf("peek = %v %v, want b", it.URL, ok)
	}
	if cur, _ := q.current(); cur.URL != "a" {
		t.Fatalf("peek moved the queue to %s", cur.URL)
	}
	q.load(items, 2, false, repeatOff)
	if _, ok := q.peekNatural(); ok {
		t.Fatal("the last track without repeat has a next one")
	}
	q.load(items, 2, false, repeatAll)
	if it, ok := q.peekNatural(); !ok || it.URL != "a" {
		t.Fatalf("repeat all at the end: %v %v, want a", it.URL, ok)
	}
	q.load(items, 1, false, repeatOne)
	if it, ok := q.peekNatural(); !ok || it.URL != "b" {
		t.Fatalf("repeat one: %v %v, want b again", it.URL, ok)
	}
	q.load(items, 0, true, repeatAll)
	q.mu.Lock()
	q.pos = len(q.order) - 1
	q.mu.Unlock()
	if _, ok := q.peekNatural(); ok {
		t.Fatal("a shuffled wrap reshuffles first, its next track cannot be known")
	}
}

func TestSetLengthKeepsAKnownLength(t *testing.T) {
	t.Parallel()
	q := newPlayQueue()
	q.load([]queueItem{{URL: "a", Duration: time.Minute}, {URL: "b"}, {URL: "b"}}, 0, false, repeatOff)
	if n := q.setLength("a", 2*time.Minute); n != 0 {
		t.Fatalf("a known length was replaced (%d)", n)
	}
	if n := q.setLength("b", 2*time.Minute); n != 2 {
		t.Fatalf("set %d tracks, want both copies of b", n)
	}
}

func TestMP3LengthKindTellsAFrameCountFromABitrateGuess(t *testing.T) {
	t.Parallel()
	cbr := cbrMP3(0, 100)
	if d, exact := mp3LengthFromFramesKind(cbr, int64(len(cbr))); d <= 0 || exact {
		t.Fatalf("CBR: %v exact=%v, want a length that is not exact", d, exact)
	}
	buf := cbrMP3(0, 3)
	off := 4 + 32 // MPEG-1 stereo side information
	copy(buf[off:], "Xing")
	binary.BigEndian.PutUint32(buf[off+4:], 0x01)
	binary.BigEndian.PutUint32(buf[off+8:], 5000)
	if d, exact := mp3LengthFromFramesKind(buf, 1); d <= 0 || !exact {
		t.Fatalf("Xing: %v exact=%v, want an exact length", d, exact)
	}
}

// A truncated MP3 keeps the Xing header of the whole file. When the header
// states the stream size and the file holds clearly less, the frame count is
// not the length: it is scaled to what is there and kept only as a guess.
func TestMP3LengthOfATruncatedFileIsNotExact(t *testing.T) {
	t.Parallel()
	const frames, stated = 5000, 2_000_000
	full := time.Duration(frames * 1152 * int64(time.Second) / 44100)
	near := func(a, b time.Duration) bool { return a-b > -time.Millisecond && a-b < time.Millisecond }
	xing := func() []byte {
		buf := cbrMP3(0, 3)
		off := 4 + 32 // MPEG-1 stereo side information
		copy(buf[off:], "Xing")
		binary.BigEndian.PutUint32(buf[off+4:], 0x03) // frames and bytes
		binary.BigEndian.PutUint32(buf[off+8:], frames)
		binary.BigEndian.PutUint32(buf[off+12:], stated)
		return buf
	}

	// Whole file (an ID3v1 tag at the end makes it a little larger): exact.
	if d, exact := mp3LengthFromFramesKind(xing(), stated+128); !exact || !near(d, full) {
		t.Fatalf("whole file: %v exact=%v, want %v exact", d, exact, full)
	}
	// Within the 2 % margin: still exact.
	if d, exact := mp3LengthFromFramesKind(xing(), stated*99/100); !exact || !near(d, full) {
		t.Fatalf("1 %% short: %v exact=%v, want %v exact", d, exact, full)
	}
	// Half the file: half the length, not exact.
	d, exact := mp3LengthFromFramesKind(xing(), stated/2)
	if exact {
		t.Fatalf("half file: exact=true, want a guess")
	}
	if diff := d - full/2; diff < -time.Second || diff > time.Second {
		t.Fatalf("half file: %v, want about %v", d, full/2)
	}

	// VBRI states the size too.
	buf := cbrMP3(0, 3)
	off := 4 + 32
	copy(buf[off:], "VBRI")
	binary.BigEndian.PutUint32(buf[off+10:], stated)
	binary.BigEndian.PutUint32(buf[off+14:], frames)
	if _, exact := mp3LengthFromFramesKind(buf, stated/2); exact {
		t.Fatalf("truncated VBRI file: exact=true, want a guess")
	}
	if d, exact := mp3LengthFromFramesKind(buf, stated); !exact || !near(d, full) {
		t.Fatalf("whole VBRI file: %v exact=%v, want %v exact", d, exact, full)
	}
}
