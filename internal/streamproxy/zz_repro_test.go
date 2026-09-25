package streamproxy

import (
	"testing"
	"time"
)

func TestReproEdgePinWipesTheTally(t *testing.T) {
	s := healthTestServer(t)
	const station = "https://playerservices.streamtheworld.com/api/livestream-redirect/X.aac"
	const edge = "https://192.0.2.7/X.aac"

	// --- handler #1, attempt 0: dials edgeFor(station) == station (no pin yet)
	s.noteStreamStart(station)
	dialed := s.edgeFor(station)
	s.pinEdge(station, edge) // streamproxy.go:858, resp.Request.URL after the redirect
	s.noteReconnect(dialed, "eof", 4096, time.Second, 200*time.Millisecond)
	t.Logf("drop 1 (dialled %s): %v", dialed, snap(t, s))

	// --- handler #1, attempt 1: dials edgeFor(station) == the pinned EDGE
	dialed = s.edgeFor(station)
	s.noteReconnect(dialed, "eof", 4096, time.Second, 200*time.Millisecond)
	t.Logf("drop 2 (dialled %s): %v", dialed, snap(t, s))

	// --- handler #1, attempt 2: same edge again
	dialed = s.edgeFor(station)
	s.noteReconnect(dialed, "eof", 4096, time.Second, 200*time.Millisecond)
	m := snap(t, s)
	t.Logf("drop 3 (dialled %s): %v", dialed, m)
	if n := count(t, m, "reconnectCount"); n != 3 {
		t.Errorf("BUG A: reconnectCount = %d after three drops of ONE station, want 3", n)
	}
	if m["upstreamURL"] != station {
		t.Errorf("BUG B: upstreamURL = %v, want the station the user chose", m["upstreamURL"])
	}

	// --- the box re-fetches the SAME stream (display push / re-push watchdog)
	s.noteStreamStart(station)
	m = snap(t, s)
	t.Logf("after the box re-fetched the same stream: %v", m)
	if n := count(t, m, "reconnectCount"); n == 0 {
		t.Errorf("BUG C: reconnectCount wiped to 0 by a re-fetch of the SAME station")
	}
	if m["lastDisconnectReason"] == "" {
		t.Errorf("BUG C: lastDisconnectReason wiped by a re-fetch of the SAME station")
	}
}
