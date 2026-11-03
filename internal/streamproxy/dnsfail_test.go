package streamproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/netutil"
	"github.com/JRpersonal/streborn/internal/presets"
)

// dialErr wraps err the way a failed station fetch arrives at recordFailure:
// a net.OpError inside the url.Error of the HTTP client.
func dialErr(rawURL string, err error) error {
	return &url.Error{Op: "Get", URL: rawURL, Err: &net.OpError{Op: "dial", Net: "tcp", Err: err}}
}

func TestStationDNSFailureKinds(t *testing.T) {
	const station = "http://stream.example.org/live.mp3"
	cases := []struct {
		name string
		url  string
		err  error
		kind string
		ok   bool
	}{
		{"nxdomain", station, dialErr(station, &net.DNSError{Err: "no such host", Name: "stream.example.org", IsNotFound: true}), dnsNotFound, true},
		{"servfail", station, dialErr(station, &net.DNSError{Err: "server misbehaving", Name: "stream.example.org", IsTemporary: true}), dnsServerFailure, true},
		{"lookup timeout", station, dialErr(station, &net.DNSError{Err: "i/o timeout", Name: "stream.example.org", IsTimeout: true}), dnsTimeout, true},
		{"no resolver", station, dialErr(station, &net.DNSError{Err: "no DNS servers", Name: "stream.example.org"}), dnsOther, true},
		{"pihole null answer", station, dialErr(station, &netutil.BlockedTargetError{IP: net.IPv4zero}), dnsBlockerAnswer, true},
		{"adguard v6 null answer", station, dialErr(station, &netutil.BlockedTargetError{IP: net.IPv6unspecified}), dnsBlockerAnswer, true},
		{"hosts-file loopback answer", station, dialErr(station, &netutil.BlockedTargetError{IP: net.ParseIP("127.0.0.1")}), dnsBlockerAnswer, true},
		// An IP literal involves no lookup: the guard refusing it is the
		// SSRF guard doing its job, not a blocker answering.
		{"loopback literal", "http://127.0.0.1:8090/x", dialErr("http://127.0.0.1:8090/x", &netutil.BlockedTargetError{IP: net.ParseIP("127.0.0.1")}), "", false},
		{"localhost name", "http://localhost/x", dialErr("http://localhost/x", &netutil.BlockedTargetError{IP: net.ParseIP("127.0.0.1")}), "", false},
		{"link-local answer", station, dialErr(station, &netutil.BlockedTargetError{IP: net.ParseIP("169.254.169.254")}), "", false},
		{"text fallback nxdomain", station, fmt.Errorf("hls: playlist fetch failed: %v", errors.New("dial tcp: lookup stream.example.org on 192.0.2.1:53: no such host")), dnsNotFound, true},
		{"text fallback servfail", station, errors.New("dial tcp: lookup stream.example.org: server misbehaving"), dnsServerFailure, true},
		{"text fallback timeout", station, errors.New("dial tcp: lookup stream.example.org on 192.0.2.1:53: read udp: i/o timeout"), dnsTimeout, true},
		// Not lookup failures.
		{"refused", station, dialErr(station, errors.New("connect: connection refused")), "", false},
		{"connect timeout", station, errors.New("dial tcp 192.0.2.7:80: i/o timeout"), "", false},
		{"http 403", station, &upstreamStatusError{Code: http.StatusForbidden}, "", false},
		{"nil", station, nil, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kind, ok := stationDNSFailure(c.url, c.err)
			if ok != c.ok || kind != c.kind {
				t.Fatalf("stationDNSFailure = (%q, %v), want (%q, %v)", kind, ok, c.kind, c.ok)
			}
		})
	}
}

// TestClassifyFailureDNS: a lookup failure gets its own reason, also for an
// HLS playlist (whose error text says "hls"), and the other classes keep
// theirs.
func TestClassifyFailureDNS(t *testing.T) {
	const station = "http://stream.example.org/live.m3u8"
	servfail := &net.DNSError{Err: "server misbehaving", Name: "stream.example.org", IsTemporary: true}
	if _, r := classifyFailure(station, fmt.Errorf("hls: playlist fetch failed: %w", dialErr(station, servfail))); r != "dns" {
		t.Fatalf("HLS lookup failure: reason %q, want dns", r)
	}
	if _, r := classifyFailure(station, dialErr(station, errors.New("connect: connection refused"))); r != "unreachable" {
		t.Fatalf("refused: reason %q, want unreachable", r)
	}
	if _, r := classifyFailure(station, errors.New("hls: no variant found in master playlist")); r != "hls" {
		t.Fatalf("hls: reason %q, want hls", r)
	}
	if c, r := classifyFailure(station, &upstreamStatusError{Code: http.StatusNotFound}); c != 404 || r != "gone" {
		t.Fatalf("404: (%d, %q), want (404, gone)", c, r)
	}
}

// TestStreamStatusDNSWhenOnline: one station host an ad-blocker answers for
// (NXDOMAIN, or 0.0.0.0) on a speaker that otherwise reaches the internet is
// reported as "dns", not as an unreachable station and not as "offline".
func TestStreamStatusDNSWhenOnline(t *testing.T) {
	_ = withTracker(t)
	var logBuf bytes.Buffer
	s := New(presets.New(), slog.New(slog.NewTextHandler(&logBuf, nil)))
	s.netMu.Lock()
	s.netOnline, s.netCheckAt = true, time.Now()
	s.netMu.Unlock()

	const station = "http://stream.example.org/live"
	s.recordFailure(station, dialErr(station, &net.DNSError{Err: "no such host", Name: "stream.example.org", IsNotFound: true}))
	got := streamStatus(t, s)
	if !got.Error || got.Reason != "dns" || got.URL != station {
		t.Fatalf("NXDOMAIN: got %+v, want dns for %s", got, station)
	}
	if !strings.Contains(logBuf.String(), "could not look up the station's address") ||
		!strings.Contains(logBuf.String(), "kind=not-found") {
		t.Fatalf("no lookup WARN line in log: %s", logBuf.String())
	}

	const blocked = "http://ads.example.org/live"
	s.recordFailure(blocked, dialErr(blocked, &netutil.BlockedTargetError{IP: net.IPv4zero}))
	if got := streamStatus(t, s); got.Reason != "dns" || got.URL != blocked {
		t.Fatalf("0.0.0.0 answer: got %+v, want dns for %s", got, blocked)
	}

	// The same failure on a speaker whose outbound probe fails is "offline".
	s.netMu.Lock()
	s.netOnline, s.netCheckAt = false, time.Now()
	s.netMu.Unlock()
	if got := streamStatus(t, s); got.Reason != "offline" {
		t.Fatalf("probe down: got %+v, want offline", got)
	}
}

// TestDNSFailureLogDeduplicated: the speaker's player retries a station that
// does not resolve several times a minute; the lookup line is written once.
func TestDNSFailureLogDeduplicated(t *testing.T) {
	_ = withTracker(t)
	var logBuf bytes.Buffer
	s := New(presets.New(), slog.New(slog.NewTextHandler(&logBuf, nil)))
	const station = "http://stream.example.org/live"
	nx := dialErr(station, &net.DNSError{Err: "no such host", Name: "stream.example.org", IsNotFound: true})
	for i := 0; i < 3; i++ {
		s.recordFailure(station, nx)
	}
	if n := strings.Count(logBuf.String(), "could not look up the station's address"); n != 1 {
		t.Fatalf("lookup line written %d times, want 1:\n%s", n, logBuf.String())
	}
}

// TestGuardRefusalOfNullAnswerIsDNS drives the real guarded dialer: a name
// that a blocker answered with 0.0.0.0 reaches the classifier as a typed
// guard refusal and reads as a lookup failure.
func TestGuardRefusalOfNullAnswerIsDNS(t *testing.T) {
	d := &net.Dialer{Control: netutil.DialGuardSSRF, Timeout: time.Second}
	_, err := d.DialContext(context.Background(), "tcp", "0.0.0.0:80")
	if err == nil {
		t.Skip("dial to 0.0.0.0 was not refused on this host")
	}
	const station = "http://ads.example.org/stream"
	if kind, ok := stationDNSFailure(station, &url.Error{Op: "Get", URL: station, Err: err}); !ok || kind != dnsBlockerAnswer {
		t.Fatalf("stationDNSFailure(%v) = (%q, %v), want blocker-answer", err, kind, ok)
	}
}

type streamStatusBody struct {
	Error  bool   `json:"error"`
	Reason string `json:"reason"`
	URL    string `json:"url"`
}

func streamStatus(t *testing.T, s *Server) streamStatusBody {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleStreamStatus(rec, httptest.NewRequest("GET", "/api/stream-status", nil))
	var got streamStatusBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	return got
}
