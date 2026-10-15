package clocksync

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
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

	ok := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer ok.Close()
	_, _ = fetchNetworkTime(context.Background(), &http.Client{Timeout: 2 * time.Second}, []string{ok.URL})
	if s, _ := internet.Status(); s != internet.StatusOK {
		t.Fatalf("after an HTTP answer: %q, want ok", s)
	}
}

// freshVerdict gives the test its own internet tracker.
func freshVerdict(t *testing.T) {
	t.Helper()
	prev := internet.Default
	internet.Default = internet.New(nil)
	t.Cleanup(func() { internet.Default = prev })
}

// Support case 2026-10-15: on an open hotspot every plain HTTP request lands
// on the login page. That answer must not read as "internet ok", while the
// clock may still take its Date as before.
func TestCaptivePortalIsNotInternet(t *testing.T) {
	client := &http.Client{Timeout: 2 * time.Second}
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK) // the login page, with a valid Date
	}))
	defer portal.Close()

	t.Run("redirect to the login host", func(t *testing.T) {
		freshVerdict(t)
		// Same address, other host name: the redirect leaves the host asked.
		target := strings.Replace(portal.URL, "127.0.0.1", "localhost", 1)
		hijack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target+"/login", http.StatusFound)
		}))
		defer hijack.Close()
		tm, err := fetchNetworkTime(context.Background(), client, []string{hijack.URL})
		if err != nil || tm.IsZero() {
			t.Fatalf("clock sync must still take the Date: %v", err)
		}
		if s, _ := internet.Status(); s != internet.StatusUnknown {
			t.Fatalf("after a portal redirect: %q, want unknown", s)
		}
	})

	t.Run("unfollowed redirect elsewhere", func(t *testing.T) {
		freshVerdict(t)
		hijack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://portal.example.net/login", http.StatusFound)
		}))
		defer hijack.Close()
		noFollow := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
		_, _ = fetchNetworkTime(context.Background(), noFollow, []string{hijack.URL})
		if s, _ := internet.Status(); s != internet.StatusUnknown {
			t.Fatalf("after a redirect to another host: %q, want unknown", s)
		}
	})

	t.Run("511 network authentication required", func(t *testing.T) {
		freshVerdict(t)
		auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNetworkAuthenticationRequired)
		}))
		defer auth.Close()
		_, _ = fetchNetworkTime(context.Background(), client, []string{auth.URL})
		if s, _ := internet.Status(); s != internet.StatusUnknown {
			t.Fatalf("after a 511: %q, want unknown", s)
		}
	})

	t.Run("no Date header", func(t *testing.T) {
		freshVerdict(t)
		noDate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header()["Date"] = nil // suppress the automatic header
		}))
		defer noDate.Close()
		_, _ = fetchNetworkTime(context.Background(), client, []string{noDate.URL})
		if s, _ := internet.Status(); s != internet.StatusUnknown {
			t.Fatalf("after an answer without Date: %q, want unknown", s)
		}
	})

	t.Run("same-host redirect and 204 still count", func(t *testing.T) {
		freshVerdict(t)
		var self string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				http.Redirect(w, r, self+"/home", http.StatusMovedPermanently)
			}
		}))
		defer srv.Close()
		self = srv.URL
		_, _ = fetchNetworkTime(context.Background(), client, []string{srv.URL})
		if s, _ := internet.Status(); s != internet.StatusOK {
			t.Fatalf("after a same-host redirect: %q, want ok", s)
		}

		freshVerdict(t)
		nc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header()["Date"] = nil
			w.WriteHeader(http.StatusNoContent)
		}))
		defer nc.Close()
		_, _ = fetchNetworkTime(context.Background(), client, []string{nc.URL + "/generate_204"})
		if s, _ := internet.Status(); s != internet.StatusOK {
			t.Fatalf("after a 204: %q, want ok", s)
		}
	})
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
