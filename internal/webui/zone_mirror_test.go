package webui

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxurl"
	"github.com/JRpersonal/streborn/internal/zones"
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

// The periodic reconcile must only mirror when the master is audibly playing
// the exact stream lastPlay points at. lastPlay is persisted on NAND, so an
// idle or standby master still "remembers" a station from days ago; mirroring
// that is how a stale group hijacked a slave's Spotify playback every five
// minutes (#342).
func TestMasterMirrorSkipReason(t *testing.T) {
	lp := "http://127.0.0.1:8888/stream/1"
	cases := []struct {
		name     string
		np       nowPlayingSnapshot
		wantSkip bool
	}{
		{"unreadable", nowPlayingSnapshot{}, true},
		{"standby", nowPlayingSnapshot{Source: "STANDBY"}, true},
		{"other source", nowPlayingSnapshot{Source: "UPNP", Location: "http://127.0.0.1:8888/stream/2", PlayStatus: "PLAY_STATE"}, true},
		{"stopped on lastPlay", nowPlayingSnapshot{Source: "UPNP", Location: lp, PlayStatus: "STOP_STATE"}, true},
		{"paused on lastPlay", nowPlayingSnapshot{Source: "UPNP", Location: lp, PlayStatus: "PAUSE_STATE"}, true},
		{"playing lastPlay", nowPlayingSnapshot{Source: "UPNP", Location: lp, PlayStatus: "PLAY_STATE"}, false},
		{"buffering lastPlay", nowPlayingSnapshot{Source: "UPNP", Location: lp, PlayStatus: "BUFFERING_STATE"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := masterMirrorSkipReason(tc.np, lp)
			if got := reason != ""; got != tc.wantSkip {
				t.Errorf("masterMirrorSkipReason(%+v) = %q, want skip=%v", tc.np, reason, tc.wantSkip)
			}
		})
	}
}

// The reconcile repairs the mirror without ever waking a speaker from standby
// or taking it off another source it is playing: the user's direct action on a
// box outranks the group (#342 was a slave's Spotify being yanked to the
// master's old radio station).
func TestSlaveMirrorAction(t *testing.T) {
	slaveURL := "http://192.168.1.50:17008/stream/1"
	cases := []struct {
		name     string
		np       nowPlayingSnapshot
		wantPush bool
	}{
		{"unreadable", nowPlayingSnapshot{}, false},
		{"standby stays asleep", nowPlayingSnapshot{Source: "STANDBY"}, false},
		{"already mirroring", nowPlayingSnapshot{Source: "UPNP", Location: slaveURL, PlayStatus: "PLAY_STATE"}, false},
		{"dropped off mirror", nowPlayingSnapshot{Source: "UPNP", Location: slaveURL, PlayStatus: "STOP_STATE"}, true},
		{"stale master stream", nowPlayingSnapshot{Source: "UPNP", Location: "http://192.168.1.50:17008/stream/4", PlayStatus: "PLAY_STATE"}, true},
		{"idle box joins", nowPlayingSnapshot{Source: "INVALID_SOURCE"}, true},
		{"busy with spotify", nowPlayingSnapshot{Source: "UPNP", Location: "http://127.0.0.1:8888/spotify/stream-4.ogg", PlayStatus: "PLAY_STATE"}, false},
		{"busy with bluetooth", nowPlayingSnapshot{Source: "BLUETOOTH", PlayStatus: "PLAY_STATE"}, false},
		{"stopped on foreign source", nowPlayingSnapshot{Source: "UPNP", Location: "http://192.168.1.9:8200/track.flac", PlayStatus: "STOP_STATE"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			push, reason := slaveMirrorAction(tc.np, slaveURL)
			if push != tc.wantPush {
				t.Errorf("slaveMirrorAction(%+v) = push=%v (%q), want push=%v", tc.np, push, reason, tc.wantPush)
			}
		})
	}
}

// zoneReferences must match by deviceID case-insensitively and by IP hint, as
// master or member, so a dissolving peer can purge the mutual/stale zones the
// wild has shown (two boxes each persisted as master naming the other, #342).
func TestZoneReferences(t *testing.T) {
	z := zones.Zone{
		Master:   "AABBCCDDEEFF",
		MasterIP: "192.168.1.50",
		Slaves: []zones.Member{
			{DeviceID: "112233445566", IP: "192.168.1.60"},
		},
	}
	cases := []struct {
		name         string
		deviceID, ip string
		want         bool
	}{
		{"master by id (case-insensitive)", "aabbccddeeff", "", true},
		{"master by ip", "", "192.168.1.50", true},
		{"slave by id", "112233445566", "", true},
		{"slave by ip", "", "192.168.1.60", true},
		{"unknown", "FFFFFFFFFFFF", "192.168.1.99", false},
		{"empty request", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := zoneReferences(z, tc.deviceID, tc.ip); got != tc.want {
				t.Errorf("zoneReferences(%q, %q) = %v, want %v", tc.deviceID, tc.ip, got, tc.want)
			}
		})
	}
}

// A dissolving peer's purge request clears the persisted zone only when it
// actually references that peer; an unrelated purge leaves the zone alone.
func TestHandleZonePurge(t *testing.T) {
	newServer := func(t *testing.T) (*Server, *zones.Store) {
		t.Helper()
		store, err := zones.Load(filepath.Join(t.TempDir(), "zones.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Set(zones.Zone{
			Master: "AABBCCDDEEFF", MasterIP: "192.168.1.50", Mode: "mirror",
			Slaves: []zones.Member{{DeviceID: "112233445566", IP: "192.168.1.60"}},
		}); err != nil {
			t.Fatal(err)
		}
		return &Server{
			zones:  store,
			logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		}, store
	}

	t.Run("referenced peer clears the zone", func(t *testing.T) {
		s, store := newServer(t)
		req := httptest.NewRequest("POST", "/api/box/zone/purge",
			strings.NewReader(`{"deviceID":"112233445566","ip":"192.168.1.60"}`))
		w := httptest.NewRecorder()
		s.handleZonePurge(w, req)
		if w.Code != 200 {
			t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"cleared":true`) {
			t.Errorf("body = %s, want cleared:true", w.Body.String())
		}
		if _, ok := store.Get(); ok {
			t.Error("zone still persisted after purge")
		}
	})

	t.Run("unrelated peer leaves the zone", func(t *testing.T) {
		s, store := newServer(t)
		req := httptest.NewRequest("POST", "/api/box/zone/purge",
			strings.NewReader(`{"deviceID":"FFFFFFFFFFFF"}`))
		w := httptest.NewRecorder()
		s.handleZonePurge(w, req)
		if w.Code != 200 {
			t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"cleared":false`) {
			t.Errorf("body = %s, want cleared:false", w.Body.String())
		}
		if _, ok := store.Get(); !ok {
			t.Error("zone was wrongly purged")
		}
	})

	t.Run("empty identity is rejected", func(t *testing.T) {
		s, _ := newServer(t)
		req := httptest.NewRequest("POST", "/api/box/zone/purge", strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		s.handleZonePurge(w, req)
		if w.Code != 400 {
			t.Fatalf("status = %d, want 400", w.Code)
		}
	})
}
