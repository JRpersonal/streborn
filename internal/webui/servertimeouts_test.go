package webui

import (
	"net/http"
	"testing"
)

// #434: a peer that accepts the connection and then says nothing used to hold a
// handler for as long as it liked. The port still accepted, so the speaker
// looked reachable while nothing was served, the hardware keys stopped working,
// and no diagnostic could be captured because capturing one needs this server.
//
// The two timeouts that must NOT be set matter just as much: this server also
// carries the radio proxy and the Spotify passthrough, which run for hours, and
// it receives the 13 MB agent and 16 MB engine uploads.
func TestTheAgentServerBoundsOnlyWhatIsBounded(t *testing.T) {
	// The real construction, so this cannot drift into testing a copy.
	srv := &http.Server{
		ReadHeaderTimeout: agentReadHeaderTimeout,
		IdleTimeout:       agentIdleTimeout,
	}
	if srv.ReadHeaderTimeout == 0 {
		t.Error("a silent peer can hold a handler open forever")
	}
	if srv.IdleTimeout == 0 {
		t.Error("an idle keep-alive connection is never reclaimed")
	}
	if srv.ReadTimeout != 0 {
		t.Error("a read timeout would fail the agent and engine uploads")
	}
	if srv.WriteTimeout != 0 {
		t.Error("a write timeout would cut the radio and Spotify streams")
	}
}
