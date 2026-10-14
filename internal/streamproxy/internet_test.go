package streamproxy

import (
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/internet"
	"github.com/JRpersonal/streborn/internal/presets"
)

// withTracker swaps the process-wide internet verdict for a fresh one for the
// duration of a test.
func withTracker(t *testing.T) *internet.Tracker {
	t.Helper()
	prev := internet.Default
	tr := internet.New(nil)
	internet.Default = tr
	t.Cleanup(func() { internet.Default = prev })
	return tr
}

// TestStreamStatusOfflineFromVerdict: a network-level station failure on a box
// whose internet verdict is already "unreachable" is reported as "offline"
// straight away, without the outbound probe.
func TestStreamStatusOfflineFromVerdict(t *testing.T) {
	tr := withTracker(t)
	s := New(presets.New(), silentLogger())
	// Make the probe cache say "online": if the handler consulted it, the
	// reason would stay "unreachable".
	s.netMu.Lock()
	s.netOnline, s.netCheckAt = true, time.Now()
	s.netMu.Unlock()

	servfail := &net.DNSError{Err: "server misbehaving", Name: "stream.example.org", IsTemporary: true}
	tr.Failure("streamproxy", internet.Classify(servfail, true), servfail)
	s.recordFailure("http://stream.example.org/live", servfail)

	rec := httptest.NewRecorder()
	s.handleStreamStatus(rec, httptest.NewRequest("GET", "/api/stream-status", nil))
	var got struct {
		Error  bool   `json:"error"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if !got.Error || got.Reason != "offline" {
		t.Fatalf("got %+v, want offline", got)
	}

	// Recovered: the same failure with a healthy verdict stays "unreachable".
	tr.Success("streamproxy")
	s.recordFailure("http://stream.example.org/live", errors.New("dial tcp: connection refused"))
	rec = httptest.NewRecorder()
	s.handleStreamStatus(rec, httptest.NewRequest("GET", "/api/stream-status", nil))
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Reason != "unreachable" {
		t.Fatalf("after recovery got %+v, want unreachable", got)
	}
}
