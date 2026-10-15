package clocksync

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/internet"
)

type dialTimeout struct{}

func (dialTimeout) Error() string   { return "i/o timeout" }
func (dialTimeout) Timeout() bool   { return true }
func (dialTimeout) Temporary() bool { return true }

// TestFetchFeedsInternetVerdict replays the 2026-10-14 field case: the named
// hosts fail with a DNS SERVFAIL, the IP literals time out. That marks the
// internet unreachable with the reason of the last host (timeout). One HTTP
// answer, even without a Date header, marks it ok again.
func TestFetchFeedsInternetVerdict(t *testing.T) {
	broken := &http.Client{Transport: &http.Transport{
		DialContext: func(_ context.Context, network, addr string) (net.Conn, error) {
			host, _, _ := net.SplitHostPort(addr)
			if net.ParseIP(host) == nil {
				return nil, &net.DNSError{Err: "server misbehaving", Name: host, IsTemporary: true}
			}
			return nil, &net.OpError{Op: "dial", Net: network, Err: dialTimeout{}}
		},
	}}
	_, err := fetchNetworkTime(context.Background(), broken,
		[]string{"http://time.example.org/", "http://192.0.2.1/"})
	if err == nil {
		t.Fatal("expected an error with every host failing")
	}
	if s, r := internet.Status(); s != internet.StatusUnreachable || r != internet.ReasonTimeout {
		t.Fatalf("after all hosts failed: %q/%q, want unreachable/timeout", s, r)
	}

	noDate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header()["Date"] = nil // suppress the automatic header
	}))
	defer noDate.Close()
	_, _ = fetchNetworkTime(context.Background(), &http.Client{Timeout: 2 * time.Second}, []string{noDate.URL})
	if s, _ := internet.Status(); s != internet.StatusOK {
		t.Fatalf("after an HTTP answer: %q, want ok", s)
	}
}

func TestImplausible(t *testing.T) {
	cases := []struct {
		name string
		when time.Time
		want bool
	}{
		{"box RTC reset to 2015", time.Date(2015, 7, 6, 21, 0, 0, 0, time.UTC), true},
		{"just before the floor", minPlausible.Add(-time.Second), true},
		{"at the floor", minPlausible, false},
		{"present day", time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC), false},
	}
	for _, c := range cases {
		if got := Implausible(c.when); got != c.want {
			t.Errorf("%s: Implausible(%v)=%v, want %v", c.name, c.when, got, c.want)
		}
	}
}

func TestPlausible(t *testing.T) {
	if plausible(time.Date(2015, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Error("2015 should not be plausible")
	}
	if plausible(time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Error("2200 should not be plausible (bogus far-future Date)")
	}
	if !plausible(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)) {
		t.Error("2026 should be plausible")
	}
}

func TestFetchNetworkTime(t *testing.T) {
	// httptest sets a valid RFC1123 Date header on every response.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	got, err := fetchNetworkTime(context.Background(), srv.Client(), []string{srv.URL})
	if err != nil {
		t.Fatalf("fetchNetworkTime: %v", err)
	}
	if !plausible(got) {
		t.Fatalf("fetched time %v is not plausible", got)
	}
}

func TestFetchNetworkTimeFallsThroughToReachableHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	// First host is unreachable; the second (the test server) must still answer.
	hosts := []string{"http://127.0.0.1:1/", srv.URL}
	if _, err := fetchNetworkTime(context.Background(), srv.Client(), hosts); err != nil {
		t.Fatalf("expected fallback to the reachable host, got %v", err)
	}
}

func TestFetchNetworkTimeRejectsImplausibleDate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Force an implausible far-past Date the fetcher must reject.
		w.Header().Set("Date", "Mon, 06 Jul 2015 21:00:00 GMT")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if _, err := fetchNetworkTime(context.Background(), srv.Client(), []string{srv.URL}); err == nil {
		t.Fatal("expected an implausible Date header to be rejected")
	}
}

func TestRunUntilSyncedReturnsWhenClockIsFine(t *testing.T) {
	// The test host clock is current, so RunUntilSynced must return immediately
	// rather than spin (no network, no root needed). A hang here fails the test
	// via the deadline.
	done := make(chan struct{})
	go func() {
		RunUntilSynced(context.Background(), nil, time.Second)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunUntilSynced did not return promptly with a plausible clock")
	}
}

func TestSyncIfImplausibleNoopWhenClockIsFine(t *testing.T) {
	// The local clock is current here (the test host), so SyncIfImplausible must
	// do nothing and must not need network or root.
	synced, err := SyncIfImplausible(context.Background(), http.DefaultClient, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if synced {
		t.Fatal("clock should not have been set when the local clock is plausible")
	}
}
