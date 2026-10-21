package streamproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// #1394: a library track started over a radio station showed the station's
// song under its own title for as long as it played. The proxy holds a dropped
// station connection open for the box's return (held.go) and kept parsing its
// ICY metadata while held: the end-of-stream wipe cleared the song, the held
// read put it straight back a moment later, and nothing cleared it again.

// icyStation is a live station with ICY metadata every metaint bytes, whose
// StreamTitle the test can change while it runs.
type icyStation struct {
	*httptest.Server
	title atomic.Value // string
	conns atomic.Int32
}

func newICYStation(t *testing.T, metaint int, first string) *icyStation {
	st := &icyStation{}
	st.title.Store(first)
	st.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.conns.Add(1)
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("icy-metaint", strconv.Itoa(metaint))
		w.WriteHeader(http.StatusOK)
		audio := make([]byte, metaint)
		for {
			meta := "StreamTitle='" + st.title.Load().(string) + "';"
			if _, err := w.Write(buildICYStream(metaint, audio, meta)); err != nil {
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

// titleLog records every title the proxy pushed to its onTitle callback.
type titleLog struct {
	mu  sync.Mutex
	got []string
}

func (l *titleLog) add(t string) { l.mu.Lock(); l.got = append(l.got, t); l.mu.Unlock() }
func (l *titleLog) has(t string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, g := range l.got {
		if g == t {
			return true
		}
	}
	return false
}

func waitTitle(t *testing.T, s *Server, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for s.CurrentTitle() != want {
		if time.Now().After(deadline) {
			t.Fatalf("live title = %q, want %q", s.CurrentTitle(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHeldStationDoesNotSetTheLiveTitle(t *testing.T) {
	st := newICYStation(t, 1024, "Radio Song - One")
	s := heldServer(time.Minute)
	defer s.dropHeld()
	var log titleLog
	s.SetOnTitle(log.add)

	// The box plays the station, then lets go of it (the UPnP push of a
	// library track); the proxy holds the connection.
	listen(t, s, st.URL, 8*1024)
	waitTitle(t, s, "Radio Song - One")

	// The station moves on while nobody listens.
	st.title.Store("Radio Song - Two")
	time.Sleep(200 * time.Millisecond)
	if log.has("Radio Song - Two") {
		t.Fatal("a held station nobody listens to pushed its next song as the live title")
	}

	// The end-of-stream wipe clears the song, as it does titleEndGrace after
	// the box left. The held read must not put a song back afterwards: this is
	// the title that then sat under the library track.
	s.wipeTitleIfUnclaimed(st.URL, genOf(s, st.URL))
	time.Sleep(200 * time.Millisecond)
	if got := s.CurrentTitle(); got != "" {
		t.Fatalf("live title after the wipe = %q, want empty while the station is only held", got)
	}
}

// The box coming back to the same station claims the held connection, and the
// song it then hears is live again, including on a second hold of the same
// connection.
func TestClaimedHeldStationSetsTheLiveTitleAgain(t *testing.T) {
	st := newICYStation(t, 1024, "Radio Song - One")
	s := heldServer(time.Minute)
	defer s.dropHeld()

	listen(t, s, st.URL, 8*1024)
	waitTitle(t, s, "Radio Song - One")
	st.title.Store("Radio Song - Two")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w := newBoxWriter()
		_, _ = s.streamOne(ctx, w, httptest.NewRequest(http.MethodGet, "/stream/1", nil).WithContext(ctx), st.URL, st.URL, true)
	}()
	waitTitle(t, s, "Radio Song - Two")
	cancel()
	<-done
	if n := st.conns.Load(); n != 1 {
		t.Fatalf("station dialed %d times, want 1 (the re-fetch should claim the held connection)", n)
	}

	// Held a second time: the reader the claim continued on goes quiet again.
	time.Sleep(100 * time.Millisecond)
	st.title.Store("Radio Song - Three")
	time.Sleep(200 * time.Millisecond)
	if got := s.CurrentTitle(); got == "Radio Song - Three" {
		t.Fatal("the second hold of the same connection still set the live title")
	}
}
