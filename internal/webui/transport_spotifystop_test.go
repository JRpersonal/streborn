package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Stop and Pause tell the Spotify manager about the user's intent, so an
// engine kept running by a recall window is paused instead of bringing the
// speaker back at its next track boundary (fleet roll 2026-10-04).
func TestStopAndPauseTellSpotifyTheUserStopped(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		handle func(*Server, http.ResponseWriter, *http.Request)
	}{
		{"pause", "/api/pause", (*Server).handlePause},
		{"stop", "/api/stop", (*Server).handleStop},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newIdleBoxServer(t, wrongStateFault)
			got := make(chan string, 1)
			s.spotifyUserStopped = func(_ context.Context, reason string) { got <- reason }

			w := httptest.NewRecorder()
			tc.handle(s, w, httptest.NewRequest(http.MethodPost, tc.path, nil))

			select {
			case reason := <-got:
				if reason != tc.name {
					t.Errorf("reason = %q, want %q", reason, tc.name)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("the Spotify manager was never told about the user's stop")
			}
		})
	}
}
