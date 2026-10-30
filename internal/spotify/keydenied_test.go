package spotify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// What a newer engine (JRpersonal/go-librespot#9) logs on the third refusal
// in a row, and while it holds new loads back afterwards.
const (
	engineStopLine = `level=error msg="stopping playback: spotify refused the audio key for several tracks in a row ` +
		`(3 in a row, last: failed retrieving audio key: key provider error code 1), stopping playback and holding back new loads for 30s"`
	engineHeldBackLine = `level=warning msg="not loading spotify:track:x" error="spotify refused the audio key for several tracks in a row: not asking again for 25s"`
)

// recordingEngine answers every engine call with 200 and remembers the paths.
func recordingEngine(t *testing.T) (*Manager, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	m := refusalManager()
	m.client = ts.Client()
	m.apiAddr = strings.TrimPrefix(ts.URL, "http://")
	return m, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), paths...)
	}
}

// The newer engine stops after three refusals, so the agent's run of five is
// never reached. Its own stop line must still put the message in the app.
func TestNewEngineStopLineReportsTheRefusal(t *testing.T) {
	m, _ := recordingEngine(t)
	m.noteLibrespotLine(refusalLine)
	m.noteLibrespotLine(refusalLine)
	if m.AudioKeyRefused() {
		t.Fatal("two refused tracks were already reported")
	}
	m.noteLibrespotLine(engineStopLine)
	if !m.AudioKeyRefused() {
		t.Fatal("the engine stopping on refused audio keys was not reported to the app")
	}
}

// The playback_error event alone is enough too, whichever arrives first.
func TestNewEngineKeyDeniedEventReportsTheRefusal(t *testing.T) {
	m, _ := recordingEngine(t)
	data, _ := json.Marshal(map[string]any{
		"stage": "session", "kind": "key_denied", "unplayable": false,
		"message": "spotify refused the audio key for several tracks in a row",
	})
	m.handleEnginePlaybackError(data)
	if !m.AudioKeyRefused() {
		t.Fatal("a key_denied playback_error was not reported to the app")
	}
}

// Every other playback_error kind is left alone: a single restricted track
// is not a refused account.
func TestOtherPlaybackErrorsSayNothing(t *testing.T) {
	m, _ := recordingEngine(t)
	for _, kind := range []string{"restricted", "unsupported", "rate_limited", "network", "unknown"} {
		data, _ := json.Marshal(map[string]any{"stage": "load", "kind": kind, "unplayable": true})
		m.handleEnginePlaybackError(data)
	}
	if m.AudioKeyRefused() {
		t.Fatal("an ordinary playback_error was reported as a refused account")
	}
}

// The message still expires with the newer engine.
func TestNewEngineRefusalMessageExpires(t *testing.T) {
	m, _ := recordingEngine(t)
	m.noteLibrespotLine(engineStopLine)
	m.mu.Lock()
	m.engineKeyDeniedAt = time.Now().Add(-keyDeniedShowFor - time.Minute)
	m.mu.Unlock()
	if m.AudioKeyRefused() {
		t.Fatal("the message was still showing after its window")
	}
}

// Once the engine has shown it stops by itself, the agent's pause at the end of
// a refusal run is not sent: it would only be a second stop.
func TestNoAgentPauseForAnEngineThatStopsItself(t *testing.T) {
	m, paths := recordingEngine(t)
	m.noteLibrespotLine(engineStopLine)
	// A later episode, logged by the same engine.
	m.mu.Lock()
	m.lastKeyRefusalAt = time.Time{}
	m.mu.Unlock()
	for i := 0; i < keyRefusalRunTrips; i++ {
		m.noteLibrespotLine(refusalLine)
	}
	time.Sleep(500 * time.Millisecond)
	for _, p := range paths() {
		if p == "/player/pause" {
			t.Fatal("the agent paused an engine that stops on refused keys by itself")
		}
	}
	if !m.AudioKeyRefused() {
		t.Fatal("the refusal stopped being reported")
	}
}

// An older engine never says the newer engine's words, so it keeps getting the
// pause at the end of a run (TestARunOfRefusalsPausesTheEngine) and the stop
// line counts nothing towards that run.
func TestStopLineIsNotCountedAsARefusedTrack(t *testing.T) {
	m, _ := recordingEngine(t)
	m.noteLibrespotLine(engineStopLine)
	m.noteLibrespotLine(engineHeldBackLine)
	m.mu.Lock()
	run := m.keyRefusalRun
	m.mu.Unlock()
	if run != 0 {
		t.Fatalf("the engine's own stop lines were counted as %d refused tracks", run)
	}
}

// A play refused during the engine's back-off gets its own hint, not the
// Premium one: the user did nothing wrong by pressing again.
func TestPlayDenialHintCoversTheBackoff(t *testing.T) {
	t.Run("play failure line", func(t *testing.T) {
		m := &Manager{}
		m.lastPlayFailLine = strings.ToLower(`level=error msg="failed handling request play" error="spotify refused the audio key for several tracks in a row: not asking again for 25s"`)
		m.lastPlayFailAt = time.Now()
		if got := m.playDenialHint(); got != keyDeniedBackoffHint {
			t.Fatalf("hint = %q, want the back-off hint", got)
		}
	})
	t.Run("key_denied event with the failure", func(t *testing.T) {
		m := refusalManager()
		m.noteEngineKeyDenied("spotify refused the audio key for several tracks in a row: not asking again for 25s")
		if got := m.playDenialHint(); got != keyDeniedBackoffHint {
			t.Fatalf("hint = %q, want the back-off hint", got)
		}
	})
	t.Run("old engine denial keeps its hint", func(t *testing.T) {
		m := &Manager{}
		m.lastPlayFailLine = `failed retrieving audio key: denied`
		m.lastPlayFailAt = time.Now()
		if got := m.playDenialHint(); !strings.Contains(got, "Premium") {
			t.Fatalf("hint = %q, want the Premium hint", got)
		}
	})
}

// The newer engine accepts the play during its back-off and then refuses the
// load. The recall must say so at once instead of waiting out its load timeouts
// and replaying from the top, which would only ask Spotify again.
func TestRecallDuringBackoffSaysWhy(t *testing.T) {
	var mu sync.Mutex
	plays := 0
	var m *Manager
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/player/play":
			mu.Lock()
			plays++
			mu.Unlock()
			// Context resolved, load refused right behind it.
			go m.noteLibrespotLine(engineHeldBackLine)
			w.WriteHeader(http.StatusOK)
		case "/status":
			_, _ = w.Write([]byte(`{"buffering":true}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer ts.Close()
	m = newTestManagerAt(t, ts.URL)
	m.resume.note("spotify:playlist:abc", "spotify:track:gone")

	start := time.Now()
	err := m.Play(context.Background(), "spotify:playlist:abc", PlayOptions{})
	if err == nil {
		t.Fatal("a recall the engine refused during its back-off reported success")
	}
	if !strings.Contains(err.Error(), keyDeniedBackoffHint) {
		t.Fatalf("error does not explain the back-off: %v", err)
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("the recall waited %s for a load the engine had already refused", d)
	}
	mu.Lock()
	defer mu.Unlock()
	if plays != 1 {
		t.Fatalf("the engine was asked to play %d times during its back-off, want 1", plays)
	}
}
