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

// Both halves of the tally key on the STATION. They used not to: the start hook
// recorded the station and the reconnect hook the resolved edge host, so the
// first drop of a redirecting station looked like a station switch, threw away
// the bytes of the healthy stretch, and left the bundle reporting a CDN address
// instead of the station the listener chose.
func TestTheTallySurvivesTheFirstDropOfARedirectingStation(t *testing.T) {
	s := healthTestServer(t)
	const station = "https://playerservices.streamtheworld.com/api/livestream-redirect/RADIO538AAC.aac"

	s.noteStreamStart(station)
	// Two drops of the same station, as a CDN token expiry produces.
	s.noteReconnect(station, "eof", 4096, time.Second, 200*time.Millisecond)
	s.noteReconnect(station, "read-fail", 8192, time.Second, 300*time.Millisecond)

	m := snap(t, s)
	if got := count(t, m, "reconnectCount"); got != 2 {
		t.Errorf("reconnectCount = %v after two drops of one station, want 2", got)
	}
	if got := count(t, m, "forwardedBytes"); got != 12288 {
		t.Errorf("forwardedBytes = %v, want both connections counted", got)
	}
	if got := m["upstreamURL"]; got != station {
		t.Errorf("upstreamURL = %v, want the station the listener chose", got)
	}
}

// A re-fetch of the SAME station is not a new stream: the box re-issues the URI
// after a display push and on a brief flap. Neither the tally nor the clock may
// restart, or a station that has played for hours reports seconds.
func TestARefetchOfTheSameStationKeepsTheTallyAndTheClock(t *testing.T) {
	s := healthTestServer(t)
	const station = "http://192.0.2.1:8888/stream/raw?u=abc"

	s.noteStreamStart(station)
	s.noteReconnect(station, "eof", 4096, time.Second, 200*time.Millisecond)
	first := snap(t, s)["playingSince"]

	// The box drops and re-fetches the same stream.
	s.noteStreamStart(station)

	m := snap(t, s)
	if got := count(t, m, "reconnectCount"); got != 1 {
		t.Errorf("reconnectCount = %v after a re-fetch, want the tally kept", got)
	}
	if got := m["lastDisconnectReason"]; got != "eof" {
		t.Errorf("lastDisconnectReason = %v after a re-fetch, want it kept", got)
	}
	if got := m["playingSince"]; got != first {
		t.Errorf("the clock restarted on a re-fetch: %v, want %v", got, first)
	}
}

// A real station switch still starts everything over.
func TestASwitchToAnotherStationStartsTheTallyOver(t *testing.T) {
	s := healthTestServer(t)
	s.noteStreamStart("http://radio/a")
	s.noteReconnect("http://radio/a", "eof", 4096, time.Second, 0)

	s.noteStreamStart("http://radio/b")
	m := snap(t, s)
	if got := count(t, m, "reconnectCount"); got != 0 {
		t.Errorf("reconnectCount = %v after a station switch, want 0", got)
	}
	if got := m["upstreamURL"]; got != "http://radio/b" {
		t.Errorf("upstreamURL = %v, want the new station", got)
	}
}
