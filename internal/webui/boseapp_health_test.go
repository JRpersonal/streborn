package webui

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// boseappTestServer is a bare Server whose log the test reads (syncBuffer
// lives in speakername_test.go).
func boseappTestServer() (*Server, *syncBuffer) {
	buf := &syncBuffer{}
	return &Server{logger: slog.New(slog.NewTextHandler(buf, nil))}, buf
}

// withAgentStartedAt pins the agent's start time for one test.
func withAgentStartedAt(t *testing.T, at time.Time) {
	t.Helper()
	prev := agentStartedAt
	agentStartedAt = at
	t.Cleanup(func() { agentStartedAt = prev })
}

// failEvery feeds a failed read every step from start through end.
func failEvery(s *Server, start, end time.Time, step time.Duration) {
	for at := start; !at.After(end); at = at.Add(step) {
		s.noteBoseAppFetchAt(false, at)
	}
}

func TestBoseAppUnresponsiveVerdict(t *testing.T) {
	started := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	down := started.Add(time.Hour)
	cases := []struct {
		name string
		tr   *boseappTracker
		now  time.Time
		want bool
	}{
		{"answering", &boseappTracker{}, down.Add(time.Hour), false},
		{"sustained outage", &boseappTracker{downSince: down, lastFail: down.Add(3 * time.Minute), fails: 4}, down.Add(3 * time.Minute), true},
		{"too short", &boseappTracker{downSince: down, lastFail: down.Add(2 * time.Minute), fails: 6}, down.Add(2*time.Minute + 59*time.Second), false},
		{"too few failed reads", &boseappTracker{downSince: down, lastFail: down.Add(8 * time.Minute), fails: 2}, down.Add(8 * time.Minute), false},
		{"boot window", &boseappTracker{downSince: started, lastFail: started.Add(4 * time.Minute), fails: 9}, started.Add(4 * time.Minute), false},
		{"past the boot window, still down since boot", &boseappTracker{downSince: started, lastFail: started.Add(5 * time.Minute), fails: 12}, started.Add(5 * time.Minute), true},
		{"no recent failed read", &boseappTracker{downSince: down, lastFail: down.Add(5 * time.Minute), fails: 5}, down.Add(5*time.Minute + boseappVerdictStale), false},
		{"firmware flashing", &boseappTracker{downSince: down, lastFail: down.Add(10 * time.Minute), fails: 9, firmwareHandoverAt: down.Add(-time.Minute)}, down.Add(10 * time.Minute), false},
		{"long after a flash", &boseappTracker{downSince: down, lastFail: down.Add(50 * time.Minute), fails: 9, firmwareHandoverAt: down.Add(-time.Minute)}, down.Add(50 * time.Minute), true},
	}
	for _, c := range cases {
		if got := c.tr.unresponsiveAt(c.now, started); got != c.want {
			t.Errorf("%s: unresponsive = %v, want %v", c.name, got, c.want)
		}
	}
}

// The live case: every read fails for minutes on end. It is reported once, on
// the failed read that crosses the threshold, logged once, and one answer
// clears it again with one more line.
func TestBoseAppOutageFlagsOnceAndClearsOnAnswer(t *testing.T) {
	now := time.Now()
	withAgentStartedAt(t, now.Add(-24*time.Hour))
	s, logs := boseappTestServer()
	start := now.Add(-10 * time.Minute)

	failEvery(s, start, start.Add(2*time.Minute), 20*time.Second)
	if down, _ := s.boseappUnresponsive(start.Add(2 * time.Minute)); down {
		t.Fatal("flagged after two minutes")
	}
	failEvery(s, start.Add(2*time.Minute+20*time.Second), now.Add(-time.Second), 20*time.Second)
	if status, since := s.BoxHealth(); status != "firmware-unresponsive" || !since.Equal(start) {
		t.Fatalf("BoxHealth = %q since %v, want firmware-unresponsive since %v", status, since, start)
	}
	if n := strings.Count(logs.String(), "has stopped answering"); n != 1 {
		t.Fatalf("down transition logged %d times, want once:\n%s", n, logs)
	}

	s.noteBoseAppFetchAt(true, now)
	if status, _ := s.BoxHealth(); status != "ok" {
		t.Fatalf("BoxHealth after an answer = %q, want ok", status)
	}
	if n := strings.Count(logs.String(), "answers again"); n != 1 {
		t.Fatalf("clear transition logged %d times, want once:\n%s", n, logs)
	}
	h := s.BoseAppHealth().(map[string]any)
	eps := h["episodes"].([]boseappOutage)
	if h["currentlyDown"] != false || len(eps) != 1 || !eps[0].Flagged || eps[0].Fails < 25 {
		t.Fatalf("health after clear = %+v", h)
	}
	// Answers while answering log nothing more.
	s.noteBoseAppFetchAt(true, now.Add(time.Second))
	if n := strings.Count(logs.String(), "boseapp:"); n != 2 {
		t.Fatalf("%d boseapp lines, want 2:\n%s", n, logs)
	}
}

// A single answer in the middle restarts the clock: an outage of two minutes,
// an answer, and another two minutes is not three minutes of silence.
func TestBoseAppAnswerInBetweenRestartsTheClock(t *testing.T) {
	now := time.Now()
	withAgentStartedAt(t, now.Add(-24*time.Hour))
	s, _ := boseappTestServer()
	start := now.Add(-5 * time.Minute)
	failEvery(s, start, start.Add(2*time.Minute), 20*time.Second)
	s.noteBoseAppFetchAt(true, start.Add(2*time.Minute+10*time.Second))
	failEvery(s, start.Add(2*time.Minute+20*time.Second), now.Add(-time.Second), 20*time.Second)
	if status, _ := s.BoxHealth(); status != "ok" {
		t.Fatalf("BoxHealth = %q, want ok", status)
	}
}

// A lone failed read that the next read clears is a blip: counted, but kept
// out of the episode ring the long outages are read from.
func TestBoseAppBlipsStayOutOfTheRing(t *testing.T) {
	s, _ := boseappTestServer()
	now := time.Now()
	for i := 0; i < 20; i++ {
		at := now.Add(time.Duration(i) * time.Minute)
		s.noteBoseAppFetchAt(false, at)
		s.noteBoseAppFetchAt(true, at.Add(time.Second))
	}
	s.noteBoseAppFetchAt(false, now.Add(time.Hour))
	s.noteBoseAppFetchAt(false, now.Add(time.Hour+time.Minute))
	s.noteBoseAppFetchAt(true, now.Add(time.Hour+2*time.Minute))
	h := s.BoseAppHealth().(map[string]any)
	if h["blips"] != 20 || len(h["episodes"].([]boseappOutage)) != 1 {
		t.Fatalf("health = %+v", h)
	}
}

// A Bose firmware hand-over keeps the hint away while the speaker flashes:
// unplugging it then is the one thing that must never be suggested.
func TestBoseAppQuietWhileFirmwareFlashes(t *testing.T) {
	now := time.Now()
	withAgentStartedAt(t, now.Add(-24*time.Hour))
	s, _ := boseappTestServer()
	s.noteBoseFirmwareHandover()
	failEvery(s, now.Add(-10*time.Minute), now.Add(-time.Second), 20*time.Second)
	if status, _ := s.BoxHealth(); status != "ok" {
		t.Fatalf("BoxHealth during a flash = %q, want ok", status)
	}
}

// The version envelope carries the new value, and the wedge latch keeps
// working underneath it.
func TestAgentVersionReportsFirmwareUnresponsive(t *testing.T) {
	now := time.Now()
	withAgentStartedAt(t, now.Add(-24*time.Hour))
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	read := func() map[string]string {
		t.Helper()
		rec := httptest.NewRecorder()
		s.handleAgentVersion(rec, httptest.NewRequest(http.MethodGet, "/api/agent/version", nil))
		var out map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v (%s)", err, rec.Body.String())
		}
		return out
	}
	if got := read()["boxHealth"]; got != "ok" {
		t.Fatalf("boxHealth = %q, want ok", got)
	}
	failEvery(s, now.Add(-4*time.Minute), now.Add(-time.Second), 20*time.Second)
	out := read()
	if out["boxHealth"] != "firmware-unresponsive" || out["boxHealthSinceSec"] == "" {
		t.Fatalf("version = %v", out)
	}

	// Firmware answers again while the wedge latch is set: wedged shows.
	s.wedge.mu.Lock()
	s.wedge.wedged, s.wedge.since = true, now
	s.wedge.mu.Unlock()
	s.noteBoseAppFetchAt(true, now)
	if got := read()["boxHealth"]; got != "wedged" {
		t.Fatalf("boxHealth = %q, want wedged", got)
	}
	if hint := s.BoxStateHint(); hint != "wedged" {
		t.Fatalf("BoxStateHint = %q, want wedged", hint)
	}
}
