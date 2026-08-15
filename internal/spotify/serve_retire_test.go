package spotify

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// handoverWriter stands in for net/http's response writer. After the handler
// returns, net/http puts the buffered writer back in a pool and nils its
// destination, so a late write is a segfault rather than an error. Here that
// same moment is a recorded test failure instead of a dead speaker.
type handoverWriter struct {
	mu       sync.Mutex
	returned bool
	late     atomic.Int32
	writes   atomic.Int32
}

func (h *handoverWriter) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.returned {
		h.late.Add(1)
		return 0, errors.New("write after the handler returned: this is a segfault on the box")
	}
	h.writes.Add(1)
	return len(p), nil
}

func (h *handoverWriter) handlerReturned() {
	h.mu.Lock()
	h.returned = true
	h.mu.Unlock()
}

// The crash of 2026-08-15 16:51:03: a box detached mid-track, ServeOgg
// returned, and the engine goroutine wrote one more batch into a writer
// net/http had already recycled. The agent died and took the audio on all
// five speakers of the group with it.
//
// retire() must therefore be a barrier, not a flag: once it returns, no write
// can be in flight and no later write may reach the ResponseWriter.
func TestNoPageReachesTheResponseWriterAfterRetire(t *testing.T) {
	for attempt := 0; attempt < 200; attempt++ {
		h := &handoverWriter{}
		cw := &closeNotifyWriter{w: h, done: make(chan struct{})}

		var engine sync.WaitGroup
		engine.Add(1)
		go func() { // the engine goroutine, writing Ogg pages
			defer engine.Done()
			for i := 0; i < 50; i++ {
				if _, err := cw.Write([]byte("ogg-page")); err != nil {
					return
				}
			}
		}()

		cw.retire()         // what ServeOgg defers
		h.handlerReturned() // what net/http does the instant the handler is done
		engine.Wait()

		if h.late.Load() != 0 {
			t.Fatalf("attempt %d: %d write(s) reached the writer after it was handed back; on the box that is the SIGSEGV that killed the agent",
				attempt, h.late.Load())
		}
	}
}

// A retired writer has to report the failure, not swallow it: forward() drops
// the sink on a write error, which is how the engine learns the box is gone.
func TestARetiredWriterRefusesAndSaysSo(t *testing.T) {
	h := &handoverWriter{}
	cw := &closeNotifyWriter{w: h, done: make(chan struct{})}
	cw.retire()

	n, err := cw.Write([]byte("ogg-page"))
	if err == nil {
		t.Fatal("a retired writer must report an error so the engine drops the sink")
	}
	if !errors.Is(err, errSinkRetired) {
		t.Errorf("err = %v, want errSinkRetired", err)
	}
	if n != 0 {
		t.Errorf("n = %d, want 0: nothing was written", n)
	}
	if h.writes.Load() != 0 {
		t.Errorf("the underlying writer was touched %d time(s) after retirement", h.writes.Load())
	}
}

// retire has to be safe to call more than once: ServeOgg defers it and the
// single-connection invariant may already have closed the same writer.
func TestRetireIsIdempotent(t *testing.T) {
	cw := &closeNotifyWriter{w: &handoverWriter{}, done: make(chan struct{})}
	cw.retire()
	cw.retire() // must not panic on a second close of done
	cw.closeConn()
	select {
	case <-cw.done:
	default:
		t.Error("retire must also release ServeOgg's wait")
	}
}
