package webui

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/keyactions"
)

// fakeBass is a speaker's bass for the bass key: what a read answers, and
// every write and notice the presses made.
type fakeBass struct {
	mu      sync.Mutex
	bass    boxapi.Bass
	readErr error
	reads   int
	writes  []int
	notices []int
}

func (f *fakeBass) install(s *Server) {
	s.bassReadFn = func(context.Context) (boxapi.Bass, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.reads++
		return f.bass, f.readErr
	}
	s.bassWriteFn = func(_ context.Context, v int) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.writes = append(f.writes, v)
		// the speaker takes the new target, the next press reads it
		f.bass.Target, f.bass.Actual = v, v
		return nil
	}
	s.showBassNoticeFn = func(level, _, _ int) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.notices = append(f.notices, level)
	}
}

func bassServer(f *fakeBass) *Server {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	f.install(s)
	return s
}

// pressBass runs one press and clears the debounce, as a press a second
// later would find it.
func pressBass(s *Server, action string) {
	s.BassKeyPress(action)
	s.bassKey.mu.Lock()
	s.bassKey.last = time.Time{}
	s.bassKey.mu.Unlock()
}

func TestNextBassLevel(t *testing.T) {
	for _, tc := range []struct {
		b    boxapi.Bass
		dir  int
		want int
	}{
		{boxapi.Bass{Target: -3, Min: -9, Max: 0}, 1, -2},
		{boxapi.Bass{Target: -3, Min: -9, Max: 0}, -1, -4},
		{boxapi.Bass{Target: 0, Min: -9, Max: 0}, 1, 0},
		{boxapi.Bass{Target: -9, Min: -9, Max: 0}, -1, -9},
		// a target outside the range (a stale value) comes back inside
		{boxapi.Bass{Target: 4, Min: -9, Max: 0}, 1, 0},
		// the speaker's own step granularity
		{boxapi.Bass{Target: 0, Min: -10, Max: 10, Step: 2}, 1, 2},
		{boxapi.Bass{Target: 8, Min: -10, Max: 10, Step: 2}, 1, 10},
		{boxapi.Bass{Target: -10, Min: -10, Max: 10, Step: 2}, -1, -10},
		// a range that is no whole number of steps stops on the last step
		{boxapi.Bass{Target: 6, Min: -9, Max: 9, Step: 5}, 1, 6},
	} {
		if got := nextBassLevel(tc.b, tc.dir); got != tc.want {
			t.Errorf("nextBassLevel(%+v, %d) = %d, want %d", tc.b, tc.dir, got, tc.want)
		}
	}
}

// Each press reads the speaker fresh, takes one step and stops at the ends
// without writing.
func TestBassKeyStepsAndClamps(t *testing.T) {
	t.Parallel()
	f := &fakeBass{bass: boxapi.Bass{Target: -2, Actual: -2, Min: -9, Max: 0, Avail: true}}
	s := bassServer(f)
	for range 4 {
		pressBass(s, keyactions.ActionBassUp)
	}
	if want := []int{-1, 0}; !slices.Equal(f.writes, want) {
		t.Errorf("writes %v, want %v (no write past the top)", f.writes, want)
	}
	if want := []int{-1, 0, 0, 0}; !slices.Equal(f.notices, want) {
		t.Errorf("notices %v, want %v (the top shows on every press)", f.notices, want)
	}
	if f.reads != 4 {
		t.Errorf("%d reads for 4 presses, want one fresh read each", f.reads)
	}
	// someone moves the slider in the app: the next press steps from there
	f.mu.Lock()
	f.bass.Target = -8
	f.writes = nil
	f.mu.Unlock()
	for range 3 {
		pressBass(s, keyactions.ActionBassDown)
	}
	if want := []int{-9}; !slices.Equal(f.writes, want) {
		t.Errorf("writes %v, want %v (from the slider's level, no write past the bottom)", f.writes, want)
	}
}

// One press reported twice steps once.
func TestBassKeyDebouncesOnePress(t *testing.T) {
	t.Parallel()
	f := &fakeBass{bass: boxapi.Bass{Target: -5, Min: -9, Max: 0, Avail: true}}
	s := bassServer(f)
	s.BassKeyPress(keyactions.ActionBassUp)
	s.BassKeyPress(keyactions.ActionBassUp)
	if want := []int{-4}; !slices.Equal(f.writes, want) {
		t.Errorf("a doubled press wrote %v, want %v", f.writes, want)
	}
}

// lockedBuffer collects log output from the press.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// A speaker without adjustable bass: nothing is written, nothing shown, and
// the log says so once.
func TestBassKeyWithoutBass(t *testing.T) {
	t.Parallel()
	var logs lockedBuffer
	f := &fakeBass{bass: boxapi.Bass{Avail: false}}
	s := bassServer(f)
	s.logger = slog.New(slog.NewTextHandler(&logs, nil))
	for range 3 {
		pressBass(s, keyactions.ActionBassUp)
	}
	if len(f.writes) != 0 || len(f.notices) != 0 {
		t.Errorf("a speaker without bass got writes %v, notices %v", f.writes, f.notices)
	}
	if n := strings.Count(logs.String(), "no adjustable bass"); n != 1 {
		t.Errorf("the no-bass line was logged %d times, want once", n)
	}
	// available but with a collapsed range is the same
	f2 := &fakeBass{bass: boxapi.Bass{Avail: true}}
	s2 := bassServer(f2)
	pressBass(s2, keyactions.ActionBassDown)
	if len(f2.writes) != 0 {
		t.Errorf("a collapsed range was written: %v", f2.writes)
	}
}

// A read that fails writes nothing; an action other than a bass step does
// nothing at all.
func TestBassKeyReadFailureAndOtherAction(t *testing.T) {
	t.Parallel()
	f := &fakeBass{readErr: errors.New("timeout")}
	s := bassServer(f)
	pressBass(s, keyactions.ActionBassUp)
	if len(f.writes) != 0 || len(f.notices) != 0 {
		t.Errorf("a failed read still wrote %v / showed %v", f.writes, f.notices)
	}
	pressBass(s, keyactions.ActionSleep)
	if f.reads != 1 {
		t.Errorf("an action other than a bass step read the speaker (%d reads)", f.reads)
	}
}

// The key-actions PUT takes a bass step on a thumbs key only, and the
// group-keys PUT names it when the key already carries one.
func TestKeyActionsBassAndGroupConflict(t *testing.T) {
	t.Parallel()
	s := keyActionsServer(t)
	if w := putTo(s.handleKeyActions, "/api/keyactions", `{"keys":{"thumbsDown":"bassDown"}}`); w.Code != http.StatusOK {
		t.Fatalf("bass step on thumbs down: %d %s", w.Code, w.Body.String())
	}
	if s.keyActions.Action("thumbsDown") != keyactions.ActionBassDown {
		t.Fatal("binding not stored")
	}
	if w := putTo(s.handleKeyActions, "/api/keyactions", `{"keys":{"aux":"bassUp"}}`); w.Code != http.StatusBadRequest {
		t.Errorf("bass step on AUX: %d, want 400", w.Code)
	}
	group := `{"templates":[{"name":"Evening","master":{"deviceID":"AAAA","ip":"192.0.2.1"},"members":[{"deviceID":"BBBB","ip":"192.0.2.2"}]}],"bindings":{"thumbsDown":"Evening"}}`
	w := putTo(s.handleGroupKeys, "/api/groupkeys", group)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "bass step") {
		t.Errorf("group on the bass key: %d %s, want 409 naming the bass step", w.Code, w.Body.String())
	}
}
