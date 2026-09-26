package spotify

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// Whether the Spotify account is Premium or free decides whether a saved key is
// offered, refused, or warned about while it is being saved. The engine used to
// proxy that question (GET /web-api/v1/me); upstream removed the proxy and hands
// out the session's access token instead, so the speaker makes the one call
// itself.
//
// What must stay true through that change: a positive answer is read, and every
// way the call can fail ends in "unknown" rather than in a wrong answer. Wrong
// here is expensive in both directions: a Premium user told their key will not
// play, or a free user told nothing until the key stays silent.
func TestAccountProductReadsTheProductThroughTheToken(t *testing.T) {
	var tokenCalls, meCalls atomic.Int32
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			tokenCalls.Add(1)
			_, _ = w.Write([]byte(`{"token":"BQ-test-token"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer engine.Close()

	spotify := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		meCalls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer BQ-test-token" {
			t.Errorf("Authorization header = %q, want the engine's token", got)
		}
		_, _ = w.Write([]byte(`{"product":"premium","display_name":"someone"}`))
	}))
	defer spotify.Close()

	m := productTestManager(t, engine.URL)
	got := m.accountProductAt(context.Background(), spotify.URL)
	if got != "premium" {
		t.Fatalf("accountProduct = %q, want premium", got)
	}
	if tokenCalls.Load() != 1 || meCalls.Load() != 1 {
		t.Fatalf("calls: token=%d me=%d, want one each", tokenCalls.Load(), meCalls.Load())
	}

	// Cached: the second read must not touch either server again. This runs on a
	// speaker, and the answer changes about as often as somebody changes plan.
	if got := m.accountProductAt(context.Background(), spotify.URL); got != "premium" {
		t.Fatalf("second read = %q, want the cached premium", got)
	}
	if tokenCalls.Load() != 1 || meCalls.Load() != 1 {
		t.Fatalf("the cache did not hold: token=%d me=%d", tokenCalls.Load(), meCalls.Load())
	}
}

func TestAccountProductStaysUnknownWhenAnythingGoesWrong(t *testing.T) {
	cases := []struct {
		name          string
		engineHandler http.HandlerFunc
		spotifyBody   string
		spotifyStatus int
	}{
		{
			// An engine that is not logged in has no token to give.
			name:          "no token",
			engineHandler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) },
		},
		{
			// An older engine that still answers the path but with nothing in it.
			name:          "empty token",
			engineHandler: func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"token":""}`)) },
		},
		{
			// The token was refused, or the scope does not cover /v1/me.
			name:          "spotify refuses",
			spotifyStatus: http.StatusUnauthorized,
		},
		{
			// The call worked but the account has no product field (a zeroconf
			// token without user-read-private).
			name:        "no product field",
			spotifyBody: `{"display_name":"someone"}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if c.engineHandler != nil {
					c.engineHandler(w, r)
					return
				}
				_, _ = w.Write([]byte(`{"token":"BQ-test-token"}`))
			}))
			defer engine.Close()
			spotify := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if c.spotifyStatus != 0 {
					w.WriteHeader(c.spotifyStatus)
					return
				}
				_, _ = w.Write([]byte(c.spotifyBody))
			}))
			defer spotify.Close()

			m := productTestManager(t, engine.URL)
			if got := m.accountProductAt(context.Background(), spotify.URL); got != "" {
				t.Fatalf("accountProduct = %q, want \"\" (unknown) so the log signal decides", got)
			}
			// And "unknown" must never read as "not Premium": a Premium user
			// must not be told their key cannot play because a call failed.
			if m.PremiumRequired() {
				t.Error("an unknown product type was treated as a free account")
			}
		})
	}
}

// A speaker that cannot reach Spotify at all (no route, a TLS store missing a
// root) is the case this must not turn into a wrong answer.
func TestAccountProductSurvivesAnUnreachableSpotify(t *testing.T) {
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"token":"BQ-test-token"}`))
	}))
	defer engine.Close()

	m := productTestManager(t, engine.URL)
	// A port nothing listens on, closed immediately so the dial is refused.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close()

	if got := m.accountProductAt(context.Background(), url); got != "" {
		t.Fatalf("accountProduct = %q, want \"\" when Spotify cannot be reached", got)
	}
}

func productTestManager(t *testing.T, engineURL string) *Manager {
	t.Helper()
	m := New("", filepath.Join(t.TempDir(), "cfg"), "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.apiAddr = strings.TrimPrefix(engineURL, "http://")
	return m
}
