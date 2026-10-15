package internet

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestTracker() (*Tracker, *fakeClock, *bytes.Buffer) {
	var buf bytes.Buffer
	clk := &fakeClock{t: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
	tr := New(slog.New(slog.NewTextHandler(&buf, nil)))
	tr.now = clk.now
	return tr, clk, &buf
}

func servfail() error {
	return &net.DNSError{Err: "server misbehaving", Name: "stream.example.org", Server: "192.0.2.1:53", IsTemporary: true}
}

func TestUnknownAtStart(t *testing.T) {
	tr, _, _ := newTestTracker()
	if s, r := tr.Status(); s != StatusUnknown || r != "" {
		t.Fatalf("fresh tracker = %q/%q, want unknown", s, r)
	}
}

func TestDNSFailureThenSuccess(t *testing.T) {
	tr, clk, buf := newTestTracker()
	tr.Success("clocksync")
	if s, _ := tr.Status(); s != StatusOK {
		t.Fatalf("after success = %q, want ok", s)
	}
	clk.advance(time.Second)
	tr.Failure("streamproxy", Classify(servfail(), true), servfail())
	if s, r := tr.Status(); s != StatusUnreachable || r != ReasonDNS {
		t.Fatalf("after SERVFAIL = %q/%q, want unreachable/dns", s, r)
	}
	// More failures: still one log line for the transition.
	for i := 0; i < 5; i++ {
		clk.advance(time.Second)
		tr.Failure("streamproxy", ReasonDNS, servfail())
	}
	clk.advance(time.Second)
	tr.Success("streamproxy")
	if s, r := tr.Status(); s != StatusOK || r != "" {
		t.Fatalf("after recovery = %q/%q, want ok", s, r)
	}
	log := buf.String()
	if n := strings.Count(log, "cannot reach the internet"); n != 1 {
		t.Fatalf("want exactly one unreachable line, got %d:\n%s", n, log)
	}
	if n := strings.Count(log, "reaches the internet"); n != 2 {
		t.Fatalf("want two ok transition lines (unknown->ok, unreachable->ok), got %d:\n%s", n, log)
	}
	if !strings.Contains(log, "reason=dns") {
		t.Fatalf("transition line lacks the reason:\n%s", log)
	}
}

func TestUnclassifiedErrorIsNoEvidence(t *testing.T) {
	tr, _, buf := newTestTracker()
	nx := &net.DNSError{Err: "no such host", Name: "dead.example.org", IsNotFound: true}
	tr.Failure("streamproxy", Classify(nx, true), nx)
	tr.Failure("streamproxy", Classify(context.Canceled, false), context.Canceled)
	if s, _ := tr.Status(); s != StatusUnknown {
		t.Fatalf("NXDOMAIN / cancel must not flip the verdict, got %q", s)
	}
	if buf.Len() != 0 {
		t.Fatalf("no transition expected, log:\n%s", buf.String())
	}
}

func TestFailureGoesStale(t *testing.T) {
	tr, clk, _ := newTestTracker()
	tr.Failure("clocksync", ReasonTimeout, errors.New("i/o timeout"))
	if s, r := tr.Status(); s != StatusUnreachable || r != ReasonTimeout {
		t.Fatalf("got %q/%q", s, r)
	}
	clk.advance(staleAfter + time.Second)
	if s, _ := tr.Status(); s != StatusUnknown {
		t.Fatalf("stale failure = %q, want unknown", s)
	}
}

func TestClassify(t *testing.T) {
	timeoutErr := &net.OpError{Op: "dial", Net: "tcp", Err: &timeoutError{}}
	cases := []struct {
		name   string
		err    error
		strict bool
		want   string
	}{
		{"nil", nil, false, ""},
		{"servfail", servfail(), true, ReasonDNS},
		{"servfail wrapped", fmt.Errorf("lookup: %w", servfail()), true, ReasonDNS},
		{"dns timeout", &net.DNSError{Err: "i/o timeout", IsTimeout: true}, true, ReasonDNS},
		{"nxdomain", &net.DNSError{Err: "no such host", IsNotFound: true}, false, ""},
		{"no route", &net.OpError{Op: "dial", Err: syscall.ENETUNREACH}, true, ReasonRoute},
		{"dial timeout strict", timeoutErr, true, ""},
		{"dial timeout", timeoutErr, false, ReasonTimeout},
		{"refused", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, false, ReasonConnect},
		{"canceled", context.Canceled, false, ""},
		{"other", errors.New("upstream status 404"), false, ""},
	}
	for _, c := range cases {
		if got := Classify(c.err, c.strict); got != c.want {
			t.Errorf("%s: Classify = %q, want %q", c.name, got, c.want)
		}
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestIsPublic(t *testing.T) {
	for ip, want := range map[string]bool{
		"192.0.2.1":    true, // documentation range counts as public here
		"192.168.1.10": false, "10.0.0.1": false, "127.0.0.1": false,
		"169.254.1.1": false, "8.8.8.8": true, "::1": false,
	} {
		if got := IsPublic(net.ParseIP(ip)); got != want {
			t.Errorf("IsPublic(%s) = %v, want %v", ip, got, want)
		}
	}
}

func TestSnapshotFields(t *testing.T) {
	tr, _, _ := newTestTracker()
	tr.Failure("clocksync", ReasonDNS, servfail())
	snap := tr.Snapshot().(map[string]any)
	if snap["status"] != StatusUnreachable || snap["reason"] != ReasonDNS || snap["lastFailSource"] != "clocksync" {
		t.Fatalf("snapshot = %v", snap)
	}
}
