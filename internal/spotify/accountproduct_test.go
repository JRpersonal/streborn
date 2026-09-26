package spotify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
			// The engine registers this READ as a POST (daemon/api_gen.go), and
			// answering a GET with a token would let the test pass while a real
			// speaker returns 405 and the plan stays unknown. That happened.
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", "POST")
				http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
				return
			}
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
				if r.Method != http.MethodPost {
					http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
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

// The three states have to be visible from outside, or a diagnostic cannot tell
// a Premium account from a question nobody could answer: both leave
// premiumRequired false.
func TestInfoReportsWhatItBelievesTheAccountPlanIs(t *testing.T) {
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer engine.Close()
	m := productTestManager(t, engine.URL)

	// Nothing known yet: the field is absent rather than a guess.
	rr := httptest.NewRecorder()
	m.ServeInfo(rr, httptest.NewRequest("GET", "/spotify/info", nil))
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, present := got["product"]; present {
		t.Errorf("an unknown plan must not be reported as one: %v", got["product"])
	}

	m.mu.Lock()
	m.productType = "premium"
	m.mu.Unlock()

	rr = httptest.NewRecorder()
	m.ServeInfo(rr, httptest.NewRequest("GET", "/spotify/info", nil))
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["product"] != "premium" {
		t.Errorf("product = %v, want premium", got["product"])
	}
}

// Being rate-limited has to change what the speaker does, not just what it
// logs. Measured on the Portable on 2026-09-26: a 30 s retry on every unknown
// plan kept asking through a 429 and stayed throttled.
func TestARateLimitedPlanReadGoesQuiet(t *testing.T) {
	var meCalls atomic.Int32
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		_, _ = w.Write([]byte(`{"token":"BQ-test-token"}`))
	}))
	defer engine.Close()
	spotify := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		meCalls.Add(1)
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer spotify.Close()

	m := productTestManager(t, engine.URL)
	if got := m.accountProductAt(context.Background(), spotify.URL); got != "" {
		t.Fatalf("product = %q, want unknown", got)
	}
	// Every further read inside the quiet window must not touch Spotify again.
	for i := 0; i < 5; i++ {
		_ = m.accountProductAt(context.Background(), spotify.URL)
	}
	if n := meCalls.Load(); n != 1 {
		t.Fatalf("Spotify was asked %d times after a 429; the speaker must wait instead", n)
	}

	// And the window does end: it is a pause, not a permanent surrender.
	m.mu.Lock()
	m.productQuietUntil = time.Now().Add(-time.Second)
	m.mu.Unlock()
	_ = m.accountProductAt(context.Background(), spotify.URL)
	if n := meCalls.Load(); n != 2 {
		t.Fatalf("after the quiet window the speaker must ask again, got %d calls", n)
	}
}

// Spotify says how long it wants to be left alone, and the speaker has to
// believe it. The first version waited six hours on a header asking for
// forty-two seconds, which is the same mistake as not waiting at all, pointed
// the other way.
func TestRetryAfterIsHonoured(t *testing.T) {
	cases := []struct {
		header string
		want   time.Duration
	}{
		{"42", 42 * time.Second},
		{"600", 10 * time.Minute},
		{"", productRateLimitQuiet},         // no header: a sane fallback
		{"nonsense", productRateLimitQuiet}, // unparseable: the same
		{"1", productRateLimitMin},          // too eager to be believed
		{"999999", productRateLimitMax},     // too long to be useful
	}
	for _, c := range cases {
		if got := parseRetryAfter(c.header); got != c.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", c.header, got, c.want)
		}
	}
	// The date form the spec also allows.
	if got := parseRetryAfter(time.Now().Add(5 * time.Minute).UTC().Format(http.TimeFormat)); got < 4*time.Minute || got > 5*time.Minute {
		t.Errorf("an HTTP-date Retry-After gave %v, want about five minutes", got)
	}
}

// Honouring the header is not enough on its own: Spotify escalates. Measured on
// a real account on 2026-09-26, it asked for 37 s, then 47 s, then 57 s, because
// the speaker came back the moment each window expired. Each refusal has to cost
// more than the last, or the speaker keeps feeding the throttle.
func TestRepeatedRefusalsBackOffFurtherEachTime(t *testing.T) {
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		_, _ = w.Write([]byte(`{"token":"BQ-test-token"}`))
	}))
	defer engine.Close()
	spotify := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "40")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer spotify.Close()

	m := productTestManager(t, engine.URL)
	waits := []time.Duration{}
	for i := 0; i < 4; i++ {
		m.mu.Lock()
		m.productQuietUntil = time.Time{} // the window has passed
		m.mu.Unlock()
		_ = m.accountProductAt(context.Background(), spotify.URL)
		m.mu.Lock()
		waits = append(waits, m.productQuietFor)
		m.mu.Unlock()
	}
	for i := 1; i < len(waits); i++ {
		if waits[i] <= waits[i-1] {
			t.Fatalf("waits did not grow: %v", waits)
		}
	}
	if waits[len(waits)-1] > productRateLimitMax {
		t.Fatalf("the backoff ran past its ceiling: %v", waits)
	}
	t.Logf("waits: %v", waits)
}
