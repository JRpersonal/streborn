package spotify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// likedEngine stands in for go-librespot's local API: /status names the
// loaded song, POST /library/liked answers as scripted.
type likedEngine struct {
	mu     sync.Mutex
	status string
	// answer writes the engine's reply to POST /library/liked.
	answer func(w http.ResponseWriter)
	posts  []string
	other  []string
}

func newLikedEngine(t *testing.T, status string, answer func(w http.ResponseWriter)) (*Manager, *likedEngine) {
	t.Helper()
	e := &likedEngine{status: status, answer: answer}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		defer e.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/status":
			_, _ = w.Write([]byte(e.status))
		case r.Method == http.MethodPost && r.URL.Path == "/library/liked":
			b, _ := io.ReadAll(r.Body)
			e.posts = append(e.posts, string(b))
			if e.answer != nil {
				e.answer(w)
			}
		default:
			e.other = append(e.other, r.Method+" "+r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	m := New("", filepath.Join(t.TempDir(), "cfg"), "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.apiAddr = strings.TrimPrefix(ts.URL, "http://")
	return m, e
}

func (e *likedEngine) postCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.posts)
}

const likedStatusTrack = `{"username":"u","track":{"uri":"spotify:track:4uLU6hMCjMI75M1A2tKUQC","name":"A Song"}}`

func TestLikeCurrentTrackSavesTheLoadedSong(t *testing.T) {
	m, e := newLikedEngine(t, likedStatusTrack, nil)
	tr, err := m.LikeCurrentTrack(context.Background())
	if err != nil {
		t.Fatalf("like failed: %v", err)
	}
	if tr.URI != "spotify:track:4uLU6hMCjMI75M1A2tKUQC" || tr.Name != "A Song" || tr.Repeat {
		t.Fatalf("track = %+v", tr)
	}
	if e.postCount() != 1 {
		t.Fatalf("engine got %d writes, want 1", e.postCount())
	}
	var body struct {
		URIs  []string `json:"uris"`
		Liked *bool    `json:"liked"`
	}
	if err := json.Unmarshal([]byte(e.posts[0]), &body); err != nil {
		t.Fatalf("body %q: %v", e.posts[0], err)
	}
	// liked must be present and true: the engine reads a missing value as a
	// bad request and false as "remove".
	if len(body.URIs) != 1 || body.URIs[0] != tr.URI || body.Liked == nil || !*body.Liked {
		t.Fatalf("body = %s", e.posts[0])
	}
	if len(e.other) != 0 {
		t.Fatalf("unexpected engine calls: %v", e.other)
	}
}

// A second press on the same song writes nothing; another song writes again,
// and so does the same song once the window is over.
func TestLikeCurrentTrackWritesASongOnce(t *testing.T) {
	m, e := newLikedEngine(t, likedStatusTrack, nil)
	ctx := context.Background()
	if _, err := m.LikeCurrentTrack(ctx); err != nil {
		t.Fatal(err)
	}
	tr, err := m.LikeCurrentTrack(ctx)
	if err != nil || !tr.Repeat {
		t.Fatalf("second press: track=%+v err=%v, want a repeat", tr, err)
	}
	if e.postCount() != 1 {
		t.Fatalf("the same song was written %d times", e.postCount())
	}

	e.mu.Lock()
	e.status = `{"track":{"uri":"spotify:track:1301WleyT98MSxVHPZCA6M","name":"Another"}}`
	e.mu.Unlock()
	if tr, err := m.LikeCurrentTrack(ctx); err != nil || tr.Repeat {
		t.Fatalf("next song: track=%+v err=%v", tr, err)
	}
	if e.postCount() != 2 {
		t.Fatalf("writes = %d, want 2", e.postCount())
	}

	m.mu.Lock()
	m.likedAt = time.Now().Add(-likeRepeatWindow - time.Second)
	m.mu.Unlock()
	if tr, err := m.LikeCurrentTrack(ctx); err != nil || tr.Repeat {
		t.Fatalf("after the window: track=%+v err=%v", tr, err)
	}
	if e.postCount() != 3 {
		t.Fatalf("writes = %d, want 3", e.postCount())
	}
}

// A failed write is not remembered: the next press tries again.
func TestLikeCurrentTrackRetriesAfterAFailure(t *testing.T) {
	fail := true
	m, e := newLikedEngine(t, likedStatusTrack, nil)
	e.answer = func(w http.ResponseWriter) {
		if fail {
			w.WriteHeader(http.StatusBadGateway)
		}
	}
	if _, err := m.LikeCurrentTrack(context.Background()); !errors.Is(err, ErrLikeFailed) {
		t.Fatalf("err = %v, want ErrLikeFailed", err)
	}
	e.mu.Lock()
	fail = false
	e.mu.Unlock()
	if tr, err := m.LikeCurrentTrack(context.Background()); err != nil || tr.Repeat {
		t.Fatalf("retry: track=%+v err=%v", tr, err)
	}
	if e.postCount() != 2 {
		t.Fatalf("writes = %d, want 2", e.postCount())
	}
}

func TestLikeCurrentTrackEngineAnswers(t *testing.T) {
	status := func(code int) func(w http.ResponseWriter) {
		return func(w http.ResponseWriter) { w.WriteHeader(code) }
	}
	for _, tc := range []struct {
		name   string
		answer func(w http.ResponseWriter)
		want   error
		reason string
	}{
		{"no session", status(http.StatusNoContent), ErrLikeNoSession, "no-session"},
		{"bad request", status(http.StatusBadRequest), ErrLikeRejected, "rejected-400"},
		{"unauthorized", status(http.StatusUnauthorized), ErrLikeUnauthorized, "unauthorized-401"},
		{"forbidden", status(http.StatusForbidden), ErrLikeForbidden, "forbidden-403"},
		{"rate limited", status(http.StatusTooManyRequests), ErrLikeRateLimited, "rate-limited-429"},
		{"spotify fault", status(http.StatusBadGateway), ErrLikeFailed, "failed"},
		{"daemon fault", status(http.StatusInternalServerError), ErrLikeFailed, "failed"},
		// The daemon passes Spotify's 404 on without a body: not "too old".
		{"spotify not found", status(http.StatusNotFound), ErrLikeFailed, "failed"},
		// An engine build without the route: net/http's own 404 and 405.
		{"route unknown", func(w http.ResponseWriter) {
			http.Error(w, "404 page not found", http.StatusNotFound)
		}, ErrLikeUnsupported, "engine-too-old"},
		{"method unknown", func(w http.ResponseWriter) {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}, ErrLikeUnsupported, "engine-too-old"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, e := newLikedEngine(t, likedStatusTrack, tc.answer)
			tr, err := m.LikeCurrentTrack(context.Background())
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if got := LikeFailureReason(err); got != tc.reason {
				t.Fatalf("reason = %q, want %q", got, tc.reason)
			}
			if tr.URI == "" {
				t.Fatal("the failed like does not name its song")
			}
			if e.postCount() != 1 {
				t.Fatalf("writes = %d, want 1", e.postCount())
			}
		})
	}
}

// Nothing is written when there is no song to save.
func TestLikeCurrentTrackWithoutASong(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		want         error
		reason       string
	}{
		{"nothing loaded", `{"username":"u","track":null}`, ErrLikeNoTrack, "no-track"},
		{"empty uri", `{"track":{"uri":"","name":""}}`, ErrLikeNoTrack, "no-track"},
		{"podcast episode", `{"track":{"uri":"spotify:episode:512ojhOuo1ktJprKbVcKyQ","name":"An Episode"}}`, ErrLikeNotATrack, "not-a-song"},
		{"local file", `{"track":{"uri":"spotify:local:a:b:c:123","name":"Local"}}`, ErrLikeNotATrack, "not-a-song"},
		{"unreadable status", `not json`, ErrLikeFailed, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, e := newLikedEngine(t, tc.status, nil)
			_, err := m.LikeCurrentTrack(context.Background())
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if got := LikeFailureReason(err); got != tc.reason {
				t.Fatalf("reason = %q, want %q", got, tc.reason)
			}
			if e.postCount() != 0 {
				t.Fatalf("the engine got %d writes for %s", e.postCount(), tc.name)
			}
		})
	}
}

// An engine that is gone is an error, not a panic, and so is a nil Manager.
func TestLikeCurrentTrackWithoutAnEngine(t *testing.T) {
	var none *Manager
	if _, err := none.LikeCurrentTrack(context.Background()); !errors.Is(err, ErrLikeNoSession) {
		t.Fatalf("nil manager: err = %v", err)
	}
	m, _ := newLikedEngine(t, likedStatusTrack, nil)
	m.apiAddr = "127.0.0.1:1" // nothing listens
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := m.LikeCurrentTrack(ctx); !errors.Is(err, ErrLikeFailed) {
		t.Fatalf("unreachable engine: err = %v", err)
	}
}

func TestIsTrackURI(t *testing.T) {
	for uri, want := range map[string]bool{
		"spotify:track:4uLU6hMCjMI75M1A2tKUQC":   true,
		"spotify:track:":                         false,
		"spotify:episode:512ojhOuo1ktJprKbVcKyQ": false,
		"spotify:track:abc:def":                  false,
		"spotify:album:4uLU6hMCjMI75M1A2tKUQC":   false,
		"":                                       false,
	} {
		if got := isTrackURI(uri); got != want {
			t.Errorf("isTrackURI(%q) = %v, want %v", uri, got, want)
		}
	}
}
