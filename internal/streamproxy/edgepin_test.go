package streamproxy

import (
	"io"
	"log/slog"
	"testing"
)

func pinServer() *Server {
	return &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// The behaviour #823 is about: a station URL that redirects somewhere else is
// dialled once through the redirect and afterwards straight at the node.
func TestEdgePinReusesTheResolvedNode(t *testing.T) {
	s := pinServer()
	const station = "https://playerservices.streamtheworld.com/api/livestream-redirect/RADIO538AAC.aac"
	const edge = "https://184.104.206.29/RADIO538AAC.aac"

	if got := s.edgeFor(station); got != station {
		t.Fatalf("with nothing pinned the station itself must be dialled, got %q", got)
	}

	s.pinEdge(station, edge)
	if got := s.edgeFor(station); got != edge {
		t.Errorf("a reconnect dialled %q, want the pinned edge %q", got, edge)
	}

	// A reconnect resolves to itself. That must not be recorded as a new pin
	// of the edge onto itself, which would make the map grow per reconnect.
	s.pinEdge(edge, edge)
	s.edgeMu.Lock()
	n := len(s.edgePins)
	s.edgeMu.Unlock()
	if n != 1 {
		t.Errorf("the pin map holds %d entries after a reconnect, want 1", n)
	}
}

// A node that stops answering must not strand the listener on it.
func TestEdgePinDroppedWhenTheNodeRefuses(t *testing.T) {
	s := pinServer()
	const station = "https://example.invalid/api/livestream-redirect/X.aac"
	const edge = "https://198.51.100.7/X.aac"

	s.pinEdge(station, edge)
	s.dropEdgePinByEdge(edge)

	if got := s.edgeFor(station); got != station {
		t.Errorf("after the node refused, the next attempt dialled %q, want the station %q", got, station)
	}
}

// Dropping something that was never pinned, or an empty URL, must be harmless:
// both happen on every ordinary failure of an unredirected station.
func TestEdgePinDropIsSafeWhenNothingIsPinned(t *testing.T) {
	s := pinServer()
	s.dropEdgePinByEdge("")
	s.dropEdgePinByEdge("https://nothing.example/stream")
	s.pinEdge("", "https://x.example/a")
	s.pinEdge("https://x.example/a", "")
	s.edgeMu.Lock()
	n := len(s.edgePins)
	s.edgeMu.Unlock()
	if n != 0 {
		t.Errorf("the pin map holds %d entries, want 0", n)
	}
}

// Two stations playing at once keep their own nodes.
func TestEdgePinPerStation(t *testing.T) {
	s := pinServer()
	s.pinEdge("https://a.example/station", "https://1.1.1.1/a")
	s.pinEdge("https://b.example/station", "https://2.2.2.2/b")
	if got := s.edgeFor("https://a.example/station"); got != "https://1.1.1.1/a" {
		t.Errorf("station a dialled %q", got)
	}
	if got := s.edgeFor("https://b.example/station"); got != "https://2.2.2.2/b" {
		t.Errorf("station b dialled %q", got)
	}
	// Dropping one leaves the other alone.
	s.dropEdgePinByEdge("https://1.1.1.1/a")
	if got := s.edgeFor("https://b.example/station"); got != "https://2.2.2.2/b" {
		t.Errorf("dropping station a's node disturbed station b: %q", got)
	}
}
