package streamproxy

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

// Every field of radio_stream_health used to be written only by noteReconnect,
// so a station playing happily for hours reported an empty upstreamURL and
// zeroes across the board. In a diagnostic bundle that reads as "no radio is
// playing", which is the opposite of the truth and exactly the wrong way round
// for a dropout report: the healthy stretch is invisible, only trouble is kept.
//
// It cost a live misreading on 2026-09-24: the empty section was taken as proof
// that a test stream was not going through the proxy at all, while the agent log
// showed the proxy running and the edge pinned.

func healthTestServer(t *testing.T) *Server {
	t.Helper()
	return &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// reconnectCount and friends are int64 in the snapshot, so comparing the any
// against an untyped 0 would always differ. Read it as the number it is.
func count(t *testing.T, m map[string]any, key string) int64 {
	t.Helper()
	n, ok := m[key].(int64)
	if !ok {
		t.Fatalf("%s is %T, want int64", key, m[key])
	}
	return n
}

func snap(t *testing.T, s *Server) map[string]any {
	t.Helper()
	m, ok := s.HealthSnapshot().(map[string]any)
	if !ok {
		t.Fatalf("HealthSnapshot is %T, want a map", s.HealthSnapshot())
	}
	return m
}

func TestAHealthyStreamNamesItsStation(t *testing.T) {
	s := healthTestServer(t)
	s.noteStreamStart("http://stream.example/ndr2")

	got := snap(t, s)
	if got["upstreamURL"] != "http://stream.example/ndr2" {
		t.Fatalf("upstreamURL = %v, want the station that is playing", got["upstreamURL"])
	}
	if n := count(t, got, "reconnectCount"); n != 0 {
		t.Errorf("reconnectCount = %d, want 0 on a stream that has not dropped", n)
	}
	if _, ok := got["playingSince"]; !ok {
		t.Error("playingSince missing: a bundle cannot say how long it has been fine")
	}
}

// The tally belongs to the station currently playing, not to a lifetime, so a
// switch resets it. noteReconnect already had that rule; the start path needs
// the same one or a new station would inherit the old one's drop count.
func TestAStationSwitchResetsTheTally(t *testing.T) {
	s := healthTestServer(t)
	s.noteStreamStart("http://stream.example/a")
	s.noteReconnect("http://stream.example/a", "eof", 4096, time.Second, 250*time.Millisecond)
	if n := count(t, snap(t, s), "reconnectCount"); n != 1 {
		t.Fatalf("reconnectCount = %d after one drop, want 1", n)
	}

	s.noteStreamStart("http://stream.example/b")
	got := snap(t, s)
	if got["upstreamURL"] != "http://stream.example/b" {
		t.Errorf("upstreamURL = %v, want the new station", got["upstreamURL"])
	}
	if n := count(t, got, "reconnectCount"); n != 0 {
		t.Errorf("reconnectCount = %d, want the new station to start clean", n)
	}
	if got["lastDisconnectReason"] != "" {
		t.Errorf("lastDisconnectReason = %q, want it cleared with the station", got["lastDisconnectReason"])
	}
}

// A reconnect to the SAME station must not look like a switch, or every drop
// would wipe the very count it is supposed to be raising.
func TestAReconnectToTheSameStationKeepsCounting(t *testing.T) {
	s := healthTestServer(t)
	s.noteStreamStart("http://stream.example/a")
	s.noteReconnect("http://stream.example/a", "eof", 1024, time.Second, 100*time.Millisecond)
	s.noteStreamStart("http://stream.example/a") // the successor handler takes over
	s.noteReconnect("http://stream.example/a", "eof", 1024, time.Second, 100*time.Millisecond)

	if n := count(t, snap(t, s), "reconnectCount"); n != 2 {
		t.Fatalf("reconnectCount = %d across two drops of one station, want 2", n)
	}
}

// An empty URL is not a station. Recording it would blank a live section.
func TestAnEmptyURLIsIgnored(t *testing.T) {
	s := healthTestServer(t)
	s.noteStreamStart("http://stream.example/a")
	s.noteStreamStart("")

	if got := snap(t, s); got["upstreamURL"] != "http://stream.example/a" {
		t.Fatalf("upstreamURL = %v, want the real station kept", got["upstreamURL"])
	}
}
