package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// fakeTransport is a stand-in for *ssh.Client so the cache bookkeeping (reuse,
// invalidation, per-host isolation, concurrency) is testable without a live SSH
// server. NewSession is never exercised through the cache itself — the cache
// only stores and hands back the transport — so it returns a stub.
type fakeTransport struct {
	id     int
	host   string
	closed atomic.Bool
}

func (f *fakeTransport) NewSession() (*ssh.Session, error) { return nil, nil }

func (f *fakeTransport) Close() error {
	f.closed.Store(true)
	return nil
}

// countingDialer builds a cache whose dialer hands out a fresh fakeTransport per
// call and records how many times it dialed each host. The optional delay lets
// the slow-handshake-warn test simulate a stalling sshd.
type countingDialer struct {
	mu    sync.Mutex
	calls map[string]int
	next  int
	delay time.Duration
	fail  bool
}

func newTestCache(d *countingDialer) *sshClientCache {
	c := newSSHClientCache(func(host string) (sshTransport, error) {
		d.mu.Lock()
		d.calls[host]++
		d.next++
		id := d.next
		delay := d.delay
		fail := d.fail
		d.mu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		if fail {
			return nil, errors.New("dial failed")
		}
		return &fakeTransport{id: id, host: host}, nil
	})
	return c
}

func (d *countingDialer) callsFor(host string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls[host]
}

func newCountingDialer() *countingDialer {
	return &countingDialer{calls: map[string]int{}}
}

// A cached client is reused: the second get() must not dial again and must
// return the same transport.
func TestSSHCacheReuse(t *testing.T) {
	d := newCountingDialer()
	c := newTestCache(d)

	c1, err := c.get("host-a")
	if err != nil {
		t.Fatalf("first get: %v", err)
	}
	c2, err := c.get("host-a")
	if err != nil {
		t.Fatalf("second get: %v", err)
	}
	if c1 != c2 {
		t.Fatalf("get returned different transports; want the cached one reused")
	}
	if n := d.callsFor("host-a"); n != 1 {
		t.Fatalf("dialed %d times; want exactly 1 (handshake once per host)", n)
	}
}

// invalidate drops the cached client, closes it, and the next get() re-dials.
func TestSSHCacheInvalidateOnError(t *testing.T) {
	d := newCountingDialer()
	c := newTestCache(d)

	c1, err := c.get("host-a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	c.invalidate("host-a", c1)
	if !c1.(*fakeTransport).closed.Load() {
		t.Fatalf("invalidate did not close the evicted transport")
	}

	c2, err := c.get("host-a")
	if err != nil {
		t.Fatalf("get after invalidate: %v", err)
	}
	if c1 == c2 {
		t.Fatalf("get returned the invalidated transport; want a fresh dial")
	}
	if n := d.callsFor("host-a"); n != 2 {
		t.Fatalf("dialed %d times; want 2 (initial + re-dial after invalidation)", n)
	}
}

// invalidate must NOT evict a newer connection that another goroutine already
// re-dialed: it only drops the client when the cached one is still the one it
// was handed (identity check).
func TestSSHCacheInvalidateIsIdentityScoped(t *testing.T) {
	d := newCountingDialer()
	c := newTestCache(d)

	stale, _ := c.get("host-a")
	// Simulate a fresh re-dial happening between stale being handed out and its
	// owner deciding to invalidate.
	c.invalidate("host-a", stale)
	fresh, _ := c.get("host-a")

	// A late invalidate for the STALE client must be a no-op for the fresh one.
	c.invalidate("host-a", stale)

	got, _ := c.get("host-a")
	if got != fresh {
		t.Fatalf("stale invalidate evicted the fresh connection; want it left intact")
	}
	if fresh.(*fakeTransport).closed.Load() {
		t.Fatalf("fresh connection was closed by a stale invalidate")
	}
}

// invalidateHost drops whatever is cached (the reboot paths call this).
func TestSSHCacheInvalidateHost(t *testing.T) {
	d := newCountingDialer()
	c := newTestCache(d)

	c1, _ := c.get("host-a")
	c.invalidateHost("host-a")
	if !c1.(*fakeTransport).closed.Load() {
		t.Fatalf("invalidateHost did not close the cached transport")
	}
	c2, _ := c.get("host-a")
	if c1 == c2 {
		t.Fatalf("invalidateHost left the old transport cached")
	}
	if n := d.callsFor("host-a"); n != 2 {
		t.Fatalf("dialed %d times; want 2", n)
	}
	// invalidateHost on a host with nothing cached must be safe.
	c.invalidateHost("never-seen")
}

// Each host has its own slot: dialing/invalidating one never touches another.
func TestSSHCachePerHostIsolation(t *testing.T) {
	d := newCountingDialer()
	c := newTestCache(d)

	a, _ := c.get("host-a")
	b, _ := c.get("host-b")
	if a == b {
		t.Fatalf("two hosts shared one transport")
	}
	if a.(*fakeTransport).host != "host-a" || b.(*fakeTransport).host != "host-b" {
		t.Fatalf("transports not keyed to their host")
	}

	// Invalidating host-a must not disturb host-b's cached client.
	c.invalidate("host-a", a)
	b2, _ := c.get("host-b")
	if b2 != b {
		t.Fatalf("invalidating host-a re-dialed host-b")
	}
	if n := d.callsFor("host-b"); n != 1 {
		t.Fatalf("host-b dialed %d times; want 1 (isolated from host-a)", n)
	}
}

// A dial error propagates and nothing is cached, so the next get() retries.
func TestSSHCacheDialErrorNotCached(t *testing.T) {
	d := newCountingDialer()
	d.fail = true
	c := newTestCache(d)

	if _, err := c.get("host-a"); err == nil {
		t.Fatalf("expected dial error")
	}
	d.mu.Lock()
	d.fail = false
	d.mu.Unlock()
	if _, err := c.get("host-a"); err != nil {
		t.Fatalf("second get after a transient dial failure: %v", err)
	}
	if n := d.callsFor("host-a"); n != 2 {
		t.Fatalf("dialed %d times; want 2 (a failed dial must not be cached)", n)
	}
}

// Concurrent get() on the same host collapses to a single dial and every caller
// sees the same client. Run under -race to catch cache data races.
func TestSSHCacheConcurrentSameHost(t *testing.T) {
	d := newCountingDialer()
	c := newTestCache(d)

	const n = 50
	var wg sync.WaitGroup
	got := make([]sshTransport, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tr, err := c.get("host-a")
			if err != nil {
				t.Errorf("get: %v", err)
				return
			}
			got[i] = tr
		}(i)
	}
	wg.Wait()

	if calls := d.callsFor("host-a"); calls != 1 {
		t.Fatalf("dialed %d times under concurrency; want exactly 1", calls)
	}
	for i := 1; i < n; i++ {
		if got[i] != got[0] {
			t.Fatalf("goroutine %d saw a different transport; want all shared", i)
		}
	}
}

// Concurrent access across many hosts with interleaved invalidation is
// race-free (the value here is the -race run, not the assertions).
func TestSSHCacheConcurrentMultiHost(t *testing.T) {
	d := newCountingDialer()
	c := newTestCache(d)
	hosts := []string{"h1", "h2", "h3", "h4"}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h := hosts[i%len(hosts)]
			tr, err := c.get(h)
			if err != nil {
				t.Errorf("get %s: %v", h, err)
				return
			}
			if i%3 == 0 {
				c.invalidate(h, tr)
			}
			if i%7 == 0 {
				c.invalidateHost(h)
			}
		}(i)
	}
	wg.Wait()
}

// The slow-handshake WARN fires exactly once per host, keyed on a first
// handshake exceeding warnAfter.
func TestSSHCacheSlowHandshakeWarnsOnce(t *testing.T) {
	d := newCountingDialer()
	d.delay = 20 * time.Millisecond
	c := newTestCache(d)
	c.warnAfter = 5 * time.Millisecond

	var buf bytes.Buffer
	c.setLogger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))

	tr, _ := c.get("host-a")
	// Force a re-dial that is also slow; the warn must NOT fire a second time.
	c.invalidate("host-a", tr)
	_, _ = c.get("host-a")

	if n := bytes.Count(buf.Bytes(), []byte("slow SSH handshake")); n != 1 {
		t.Fatalf("slow-handshake warn logged %d times; want exactly 1 per host", n)
	}
}

// A fast handshake never warns.
func TestSSHCacheFastHandshakeNoWarn(t *testing.T) {
	d := newCountingDialer() // no delay
	c := newTestCache(d)
	c.warnAfter = 5 * time.Second

	var buf bytes.Buffer
	c.setLogger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))

	_, _ = c.get("host-a")
	if bytes.Contains(buf.Bytes(), []byte("slow SSH handshake")) {
		t.Fatalf("warned on a fast handshake")
	}
}

// sshTransportSuspect keeps a connection only when the command cleanly returned
// a remote exit status; every transport-level failure marks it suspect.
func TestSSHTransportSuspect(t *testing.T) {
	if sshTransportSuspect(nil) {
		t.Fatalf("nil error must not be suspect")
	}
	if !sshTransportSuspect(errors.New("connection reset")) {
		t.Fatalf("a transport drop must be suspect")
	}
	if !sshTransportSuspect(context.DeadlineExceeded) {
		t.Fatalf("a timeout must be suspect")
	}
	// A real remote non-zero exit proves the transport round-tripped fine, so the
	// cached connection must be kept.
	if sshTransportSuspect(&ssh.ExitError{}) {
		t.Fatalf("a clean remote exit status must not mark the transport suspect")
	}
}

// allTargetsTruncatedError unwraps to its cause so callers can errors.As it.
func TestAllTargetsTruncatedErrorUnwrap(t *testing.T) {
	cause := &uploadTruncatedError{msg: "upload x truncated"}
	wrapped := &allTargetsTruncatedError{err: cause}
	var got *uploadTruncatedError
	if !errors.As(wrapped, &got) {
		t.Fatalf("allTargetsTruncatedError does not unwrap to its cause")
	}
	var all *allTargetsTruncatedError
	if !errors.As(error(wrapped), &all) {
		t.Fatalf("errors.As failed to match allTargetsTruncatedError itself")
	}
}
