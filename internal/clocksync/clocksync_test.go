package clocksync

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

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
