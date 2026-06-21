package webui

import (
	"context"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxurl"
)

// mirrorURLForSlaves must turn the master's own loopback stream URL
// (http://127.0.0.1:8888/...) into one a remote SLAVE can fetch: the master's
// LAN IP on the externally reachable :17008 redirect, with the path and query
// preserved. A slave handed the loopback URL plays its own stream (or nothing),
// which is why the follower's display updated but its audio did not (#70).
func TestMirrorURLForSlaves(t *testing.T) {
	s := &Server{}
	ctx := context.Background()
	const masterIP = "192.168.1.50"
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "radio slot",
			in:   boxurl.StreamSlot(3), // http://127.0.0.1:8888/stream/3
			want: "http://192.168.1.50:17008/stream/3",
		},
		{
			name: "spotify slot keeps path",
			in:   boxurl.SpotifySlot(6),
			want: "http://192.168.1.50:17008/spotify/stream-6.ogg",
		},
		{
			name: "raw stream keeps query",
			in:   boxurl.RawStream("http://example.com/x"),
			want: "http://192.168.1.50:17008/stream/raw?u=aHR0cDovL2V4YW1wbGUuY29tL3g",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.mirrorURLForSlaves(ctx, tc.in, masterIP); got != tc.want {
				t.Errorf("mirrorURLForSlaves(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// With no master IP and no reachable firmware (empty boxHost), the helper must
// leave the URL unchanged rather than point a slave at a bogus host: a no-op
// push beats a wrong one.
func TestMirrorURLForSlavesNoIPIsNoOp(t *testing.T) {
	s := &Server{} // boxHost == "" -> GetInfo fallback fails
	in := boxurl.StreamSlot(2)
	if got := s.mirrorURLForSlaves(context.Background(), in, ""); got != in {
		t.Errorf("mirrorURLForSlaves with no IP = %q, want unchanged %q", got, in)
	}
}

// A URL that does not parse is returned unchanged (never panics).
func TestMirrorURLForSlavesBadURL(t *testing.T) {
	s := &Server{}
	in := "://not a url"
	if got := s.mirrorURLForSlaves(context.Background(), in, "192.168.1.50"); got != in {
		t.Errorf("mirrorURLForSlaves(bad) = %q, want unchanged %q", got, in)
	}
}
