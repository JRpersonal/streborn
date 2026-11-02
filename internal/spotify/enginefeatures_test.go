package spotify

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// rootWithShuffleAtLoad is GET / of an engine that takes shuffle_context on
// /player/play; rootLegacy is GET / of a build from before the feature list.
const (
	rootWithShuffleAtLoad = `{"playback_ready":true,"features":["play_shuffle_context"]}`
	rootLegacy            = `{"playback_ready":true}`
)

// mockEngineRoot is mockLibrespot with a GET / answer: it records every
// request except GET / and /status, and counts the GET / reads.
func mockEngineRoot(t *testing.T, root string) (m *Manager, calls func() []recordedCall, rootReads *atomic.Int32) {
	t.Helper()
	var mu sync.Mutex
	var got []recordedCall
	rootReads = new(atomic.Int32)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			rootReads.Add(1)
			_, _ = w.Write([]byte(root))
			return
		case "/status":
			_, _ = w.Write([]byte(`{"username":"u","track":{"uri":"spotify:track:cur","name":"Cur"}}`))
			return
		}
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, recordedCall{path: r.URL.Path, body: string(b)})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	m = New("", filepath.Join(t.TempDir(), "cfg"), "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.apiAddr = strings.TrimPrefix(ts.URL, "http://")
	calls = func() []recordedCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedCall(nil), got...)
	}
	return m, calls, rootReads
}

func countPath(calls []recordedCall, path string) int {
	n := 0
	for _, c := range calls {
		if c.path == path {
			n++
		}
	}
	return n
}

// A shuffle preset on an engine that shuffles at load sends one play that asks
// for shuffle, and nothing else: no toggle, no skip away from the first song.
// The skip was what the Spotify app showed as the playlist's first song being
// loaded and then skipped.
func TestPlayShuffleAtLoadSendsOnePlay(t *testing.T) {
	m, calls, _ := mockEngineRoot(t, rootWithShuffleAtLoad)
	const ctxURI = "spotify:playlist:abc"
	m.resume.note(ctxURI, "spotify:track:ignored-when-shuffling")

	if err := m.Play(context.Background(), ctxURI, PlayOptions{Shuffle: true}); err != nil {
		t.Fatalf("Play: %v", err)
	}

	playBody, ok := bodyForPath(calls(), "/player/play")
	if !ok {
		t.Fatal("no /player/play call recorded")
	}
	if !strings.Contains(playBody, `"shuffle_context":true`) {
		t.Errorf("the play must ask for shuffle at load, body = %s", playBody)
	}
	if strings.Contains(playBody, "skip_to_uri") {
		t.Errorf("a shuffle recall must not resume a track, body = %s", playBody)
	}
	if n := countPath(calls(), "/player/shuffle_context"); n != 0 {
		t.Errorf("shuffle at load must not toggle shuffle afterwards, got %d /player/shuffle_context", n)
	}
	if n := countPath(calls(), "/player/next"); n != 0 {
		t.Errorf("shuffle at load must not skip away from the first track, got %d /player/next", n)
	}
	if countPath(calls(), "/player/resume") == 0 {
		t.Error("the recall must still resume the paused load")
	}
}

// A non-shuffle preset asks for shuffle OFF in the play itself, so shuffle
// left on by an earlier preset cannot make it start on a random song, and the
// resume point still applies.
func TestPlayNoShuffleAtLoadClearsShuffleAndResumes(t *testing.T) {
	m, calls, _ := mockEngineRoot(t, rootWithShuffleAtLoad)
	const ctxURI = "spotify:playlist:abc"
	m.resume.note(ctxURI, "spotify:track:resume-here")

	if err := m.Play(context.Background(), ctxURI, PlayOptions{Shuffle: false}); err != nil {
		t.Fatalf("Play: %v", err)
	}

	playBody, _ := bodyForPath(calls(), "/player/play")
	if !strings.Contains(playBody, `"shuffle_context":false`) {
		t.Errorf("a non-shuffle play must ask for shuffle off, body = %s", playBody)
	}
	if !strings.Contains(playBody, `"skip_to_uri":"spotify:track:resume-here"`) {
		t.Errorf("a non-shuffle recall must still resume via skip_to_uri, body = %s", playBody)
	}
	if n := countPath(calls(), "/player/shuffle_context"); n != 0 {
		t.Errorf("shuffle at load needs no separate toggle, got %d /player/shuffle_context", n)
	}
	if n := countPath(calls(), "/player/next"); n != 0 {
		t.Errorf("a non-shuffle recall must not skip, got %d /player/next", n)
	}
}

// An engine from before the feature list keeps the toggle-and-skip staging,
// and never sees a field it would drop silently.
func TestPlayShuffleOnALegacyEngineKeepsTheStaging(t *testing.T) {
	defer func(d time.Duration) { shuffleSkipSettle = d }(shuffleSkipSettle)
	shuffleSkipSettle = 200 * time.Millisecond
	m, calls, _ := mockEngineRoot(t, rootLegacy)

	if err := m.Play(context.Background(), "spotify:playlist:abc", PlayOptions{Shuffle: true}); err != nil {
		t.Fatalf("Play: %v", err)
	}

	playBody, _ := bodyForPath(calls(), "/player/play")
	if strings.Contains(playBody, "shuffle_context") {
		t.Errorf("a legacy engine must not be sent shuffle_context in the play, body = %s", playBody)
	}
	if n := countPath(calls(), "/player/shuffle_context"); n < 2 {
		t.Errorf("a legacy engine must get the off-then-on toggle, got %d /player/shuffle_context", n)
	}
	if countPath(calls(), "/player/next") == 0 {
		t.Error("a legacy engine must get the skip to a random track")
	}
}

// GET / is read once per engine process, read again after an engine start,
// and a failed read is not kept.
func TestEngineFeaturesAreReadOncePerEngineProcess(t *testing.T) {
	m, _, rootReads := mockEngineRoot(t, rootWithShuffleAtLoad)
	ctx := context.Background()

	for range 3 {
		if !m.engineSupports(ctx, engineFeaturePlayShuffleContext) {
			t.Fatal("the engine lists play_shuffle_context")
		}
	}
	if n := rootReads.Load(); n != 1 {
		t.Errorf("GET / read %d times, want once per engine process", n)
	}
	if m.engineSupports(ctx, "something-else") {
		t.Error("a feature the engine does not list must read as unsupported")
	}

	m.mu.Lock()
	m.resetEngineFeaturesLocked()
	m.mu.Unlock()
	m.engineSupports(ctx, engineFeaturePlayShuffleContext)
	if n := rootReads.Load(); n != 2 {
		t.Errorf("GET / read %d times, want a fresh read after an engine start", n)
	}
}

func TestEngineFeaturesUnreadableIsNotKept(t *testing.T) {
	var answer atomic.Value
	answer.Store("404 page not found")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(answer.Load().(string)))
	}))
	defer ts.Close()
	m := New("", filepath.Join(t.TempDir(), "cfg"), "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.apiAddr = strings.TrimPrefix(ts.URL, "http://")
	ctx := context.Background()

	if m.engineSupports(ctx, engineFeaturePlayShuffleContext) {
		t.Fatal("an answer that is not the engine's root must read as unsupported")
	}
	answer.Store(rootWithShuffleAtLoad)
	if !m.engineSupports(ctx, engineFeaturePlayShuffleContext) {
		t.Error("a failed read must not be kept: the next call has to ask again")
	}

	// An unreachable engine reads as unsupported, too.
	m2 := New("", filepath.Join(t.TempDir(), "cfg"), "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m2.apiAddr = "127.0.0.1:1"
	if m2.engineSupports(ctx, engineFeaturePlayShuffleContext) {
		t.Error("an unreachable engine must read as unsupported")
	}
}

// A read that began under an older engine process must not be kept for the new one.
func TestEngineFeaturesReadAcrossARestartIsDropped(t *testing.T) {
	asked := make(chan struct{})
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(asked)
		<-release
		_, _ = w.Write([]byte(rootWithShuffleAtLoad))
	}))
	defer ts.Close()
	m := New("", filepath.Join(t.TempDir(), "cfg"), "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.apiAddr = strings.TrimPrefix(ts.URL, "http://")

	done := make(chan bool)
	go func() { done <- m.engineSupports(context.Background(), engineFeaturePlayShuffleContext) }()
	<-asked
	// The engine restarts while the read is on the wire.
	m.mu.Lock()
	m.resetEngineFeaturesLocked()
	m.mu.Unlock()
	close(release)

	if !<-done {
		t.Error("the answer still serves the call that asked")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.engineFeatures != nil {
		t.Error("a read that began under the previous engine process must not be kept")
	}
}
