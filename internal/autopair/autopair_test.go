package autopair

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const pairedInfo = `<info><margeAccountUUID>stick@local</margeAccountUUID></info>`
const unpairedInfo = `<info><margeAccountUUID></margeAccountUUID></info>`

// fakeBox simulates the BoseApp :8090 endpoints the manager talks to.
type fakeBox struct {
	mu        sync.Mutex
	infoBody  string
	infoDelay time.Duration
	pairPosts atomic.Int64
}

func (f *fakeBox) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/info", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		body, delay := f.infoBody, f.infoDelay
		f.mu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("/setMargeAccount", func(w http.ResponseWriter, r *http.Request) {
		f.pairPosts.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func newTestManager(t *testing.T, box *fakeBox) *Manager {
	t.Helper()
	srv := httptest.NewServer(box.handler())
	t.Cleanup(srv.Close)
	m := New(slog.Default(), Config{BoxHost: "127.0.0.1"})
	m.base = srv.URL
	return m
}

func TestEnsurePairedSkipsWhenPaired(t *testing.T) {
	box := &fakeBox{infoBody: pairedInfo}
	m := newTestManager(t, box)
	if err := m.EnsurePaired(context.Background()); err != nil {
		t.Fatalf("EnsurePaired: %v", err)
	}
	if got := box.pairPosts.Load(); got != 0 {
		t.Fatalf("paired box must not be re-paired, got %d setMargeAccount POSTs", got)
	}
}

func TestEnsurePairedPairsWhenUnpaired(t *testing.T) {
	box := &fakeBox{infoBody: unpairedInfo}
	m := newTestManager(t, box)
	if err := m.EnsurePaired(context.Background()); err != nil {
		t.Fatalf("EnsurePaired: %v", err)
	}
	if got := box.pairPosts.Load(); got != 1 {
		t.Fatalf("unpaired box must be paired exactly once, got %d POSTs", got)
	}
}

func TestForceReassertsDespitePaired(t *testing.T) {
	// First cycle after agent start: a stale "paired" must not skip the
	// re-assert (dead-cloud amber icon, ST300 silent login loss).
	box := &fakeBox{infoBody: pairedInfo}
	m := newTestManager(t, box)
	if err := m.ensure(context.Background(), true); err != nil {
		t.Fatalf("ensure(force): %v", err)
	}
	if got := box.pairPosts.Load(); got != 1 {
		t.Fatalf("forced cycle must POST setMargeAccount once, got %d", got)
	}
}

func TestEnsurePairedSingleFlight(t *testing.T) {
	// Concurrent triggers (ticker + hardware press + app recall) must
	// coalesce into ONE cycle instead of stacking POSTs on a slow box (#375).
	box := &fakeBox{infoBody: unpairedInfo, infoDelay: 150 * time.Millisecond}
	m := newTestManager(t, box)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.EnsurePaired(context.Background())
		}()
	}
	wg.Wait()
	if got := box.pairPosts.Load(); got != 1 {
		t.Fatalf("8 concurrent triggers must coalesce into 1 pair POST, got %d", got)
	}
}
