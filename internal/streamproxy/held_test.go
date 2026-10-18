package streamproxy

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
)

// seqStation is a live station that sends a running byte sequence (each byte
// one more than the last, mod 251) until the listener goes away, and counts
// its connections and how many are still open.
type seqStation struct {
	*httptest.Server
	conns, open atomic.Int32
	closed      chan struct{}
}

func newSeqStation(t *testing.T, finite int) *seqStation {
	st := &seqStation{closed: make(chan struct{}, 8)}
	st.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.conns.Add(1)
		st.open.Add(1)
		defer func() { st.open.Add(-1); st.closed <- struct{}{} }()
		w.Header().Set("Content-Type", "audio/mpeg")
		if finite > 0 {
			w.Header().Set("Content-Length", strconv.Itoa(finite))
			w.Header().Set("Accept-Ranges", "bytes")
		}
		w.WriteHeader(http.StatusOK)
		chunk := make([]byte, 1024)
		var v byte
		for sent := 0; finite == 0 || sent < finite; sent += len(chunk) {
			for i := range chunk {
				chunk[i] = v
				v = (v + 1) % 251
			}
			if _, err := w.Write(chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}))
	t.Cleanup(st.Close)
	return st
}

// boxWriter is the speaker's side of /stream, readable while it is written.
type boxWriter struct {
	mu  sync.Mutex
	h   http.Header
	buf bytes.Buffer
}

func newBoxWriter() *boxWriter           { return &boxWriter{h: http.Header{}} }
func (b *boxWriter) Header() http.Header { return b.h }
func (b *boxWriter) WriteHeader(int)     {}
func (b *boxWriter) Flush()              {}
func (b *boxWriter) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *boxWriter) bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buf.Bytes())
}

// listen plays url on a fresh box connection until the box has got want
// bytes, then drops it the way a failing speaker does, and returns what it
// got.
func listen(t *testing.T, s *Server, url string, want int) []byte {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	w := newBoxWriter()
	done := make(chan bool, 1)
	go func() {
		alive, _ := s.streamOne(ctx, w, httptest.NewRequest(http.MethodGet, "/stream/1", nil).WithContext(ctx), url, url, true)
		done <- alive
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(w.bytes()) < want {
		if time.Now().After(deadline) {
			t.Fatalf("the box got %d bytes, want %d", len(w.bytes()), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case alive := <-done:
		if alive {
			t.Fatal("streamOne reported the box alive after it left")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("streamOne did not return after the box left")
	}
	return w.bytes()
}

func contiguous(b []byte) bool {
	for i := 1; i < len(b); i++ {
		if b[i] != (b[i-1]+1)%251 {
			return false
		}
	}
	return true
}

func heldServer(hold time.Duration) *Server {
	s := New(presets.New(), silentLogger())
	s.client = &http.Client{}
	s.heldFor = hold
	return s
}

// #1245: the box drops the station and asks for it again two seconds later.
// It continues on the same station connection, with no new dial, starting on
// the audio it got last and running on without a gap or a repeat in between.
func TestHeldStationResumesWithoutDial(t *testing.T) {
	st := newSeqStation(t, 0)
	s := heldServer(time.Minute)
	defer s.dropHeld()
	first := listen(t, s, st.URL, 20*1024)
	time.Sleep(100 * time.Millisecond)
	second := listen(t, s, st.URL, 40*1024)
	if n := st.conns.Load(); n != 1 {
		t.Fatalf("station dialed %d times, want 1", n)
	}
	if !contiguous(second) {
		t.Fatal("the resumed stream skips or repeats bytes")
	}
	lead := first[len(first)-1024:]
	if i := bytes.Index(second, lead); i < 0 || i > spliceTailSize {
		t.Fatal("the resumed stream does not start on what the box got last")
	}
}

// Nobody came back: the held connection is closed once heldFor ran out, and
// the next fetch dials afresh.
func TestHeldStationClosesWhenUnused(t *testing.T) {
	st := newSeqStation(t, 0)
	s := heldServer(150 * time.Millisecond)
	listen(t, s, st.URL, 8*1024)
	select {
	case <-st.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("the held station connection was never closed")
	}
	listen(t, s, st.URL, 8*1024)
	if n := st.conns.Load(); n != 2 {
		t.Fatalf("station dialed %d times, want 2 (the hold had run out)", n)
	}
}

// The box moved to another station: the held one is let go of at once.
func TestHeldStationDroppedForAnotherStation(t *testing.T) {
	a, b := newSeqStation(t, 0), newSeqStation(t, 0)
	s := heldServer(time.Minute)
	defer s.dropHeld()
	listen(t, s, a.URL, 8*1024)
	if a.open.Load() != 1 {
		t.Fatal("the dropped station is not being held")
	}
	listen(t, s, b.URL, 8*1024)
	select {
	case <-a.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("the first station is still held after the box moved on")
	}
}

// A finite file is never held: it closes as soon as the box lets go.
func TestHeldNeverForFiniteFile(t *testing.T) {
	st := newSeqStation(t, 50<<20)
	s := heldServer(time.Minute)
	defer s.dropHeld()
	listen(t, s, st.URL, 8*1024)
	select {
	case <-st.closed:
	case <-time.After(time.Second):
		t.Fatal("a finite file was held after the box let go")
	}
}
