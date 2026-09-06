package boxlog

import (
	"context"
	"testing"
	"time"
)

// Lines are verbatim from the Portable (taigan) and kitchen ST10 (rhino)
// captures of 2026-09-06, timestamps and ids left as the firmware wrote them.
const (
	linePortableThumbsUpPress   = `Sep  6 17:19:37 taigan local0.debug BoseApp[1959]: [(002830):IrDevice:DEBUG]IR Key event: Key()=5, State()=0, Producer()=2`
	linePortableThumbsUpRelease = `Sep  6 17:19:40 taigan local0.debug BoseApp[1959]: [(002830):IrDevice:DEBUG]IR Key event: Key()=5, State()=1, Producer()=2`
	linePortableConsolePreset1  = `Sep  6 17:19:55 taigan local0.debug BoseApp[1959]: [(002866):ConsoleButtons:DEBUG]CONSOLE Key event: Key()=12, State()=0, Producer()=1`
	lineRhinoThumbsUp           = `Sep  6 18:50:27 rhino local0.info BoseApp[1943]: [(002161):StatsDataCaptureClient:INFO]buildJson(), like-pressed, THUMBS_UP, ir-remote`
	lineRhinoPowerConsole       = `Sep  6 18:50:36 rhino local0.info BoseApp[1943]: [(002164):StatsDataCaptureClient:INFO]buildJson(), power-pressed, POWER, console`
	lineNoise                   = `Sep  6 17:19:37 taigan local0.err TPDA[1949]: [(002991):Network:ERROR]Failed connect call for 127.0.0.1:30034 (fd 54): Connection refused`
)

func TestParseKeyLine(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		line string
		want KeyEvent
		ok   bool
	}{
		{"remote thumbs up press", linePortableThumbsUpPress, KeyEvent{Key: 5, Name: "THUMBS_UP", Producer: ProducerIRRemote, State: StatePressed, Origin: OriginTrace}, true},
		{"remote thumbs up release", linePortableThumbsUpRelease, KeyEvent{Key: 5, Name: "THUMBS_UP", Producer: ProducerIRRemote, State: StateReleased, Origin: OriginTrace}, true},
		{"console preset 1", linePortableConsolePreset1, KeyEvent{Key: 12, Name: "PRESET_1", Producer: ProducerConsole, State: StatePressed, Origin: OriginTrace}, true},
		{"rhino analytics thumbs up", lineRhinoThumbsUp, KeyEvent{Key: 5, Name: "THUMBS_UP", Producer: ProducerIRRemote, State: StatePressed, Origin: OriginStats}, true},
		{"rhino analytics power console", lineRhinoPowerConsole, KeyEvent{Key: 8, Name: "POWER", Producer: ProducerConsole, State: StatePressed, Origin: OriginStats}, true},
		{"noise", lineNoise, KeyEvent{}, false},
		{"empty", "", KeyEvent{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ParseKeyLine(c.line, now)
			if ok != c.ok {
				t.Fatalf("ok=%v want %v", ok, c.ok)
			}
			if !ok {
				return
			}
			got.At = time.Time{}
			if got != c.want {
				t.Fatalf("got %+v want %+v", got, c.want)
			}
		})
	}
}

func TestKeyNamesMatchHardware(t *testing.T) {
	// Measured on the Portable: Back=3, Forward=4, Thumbs up=5, Thumbs down=6,
	// Volume up=10, Preset 1=12, Preset 2=13, AUX=18.
	for key, want := range map[int]string{3: "PREV_TRACK", 4: "NEXT_TRACK", 5: "THUMBS_UP", 6: "THUMBS_DOWN", 10: "VOLUME_UP", 12: "PRESET_1", 13: "PRESET_2", 18: "AUX_INPUT"} {
		if got := KeyName(key); got != want {
			t.Errorf("KeyName(%d)=%q want %q", key, got, want)
		}
		if got := KeyNumber(want); got != key {
			t.Errorf("KeyNumber(%q)=%d want %d", want, got, key)
		}
	}
	if got := KeyName(99); got != "KEY_99" {
		t.Errorf("unknown key: %q", got)
	}
}

func TestReaderDedupsTraceAndStatsForOnePress(t *testing.T) {
	var got []KeyEvent
	r := New(nil, nil, func(e KeyEvent) { got = append(got, e) })
	base := time.Now()
	r.handleLine(linePortableThumbsUpPress, base)
	r.handleLine(lineRhinoThumbsUp, base.Add(80*time.Millisecond)) // same press, analytics copy
	r.handleLine(linePortableThumbsUpRelease, base.Add(3*time.Second))
	r.handleLine(lineNoise, base.Add(3*time.Second))
	if len(got) != 2 {
		t.Fatalf("want press+release only, got %d events: %+v", len(got), got)
	}
	if got[0].State != StatePressed || got[1].State != StateReleased {
		t.Fatalf("unexpected order: %+v", got)
	}
	// A genuine second press of the same key from the same origin is kept.
	r.handleLine(linePortableThumbsUpPress, base.Add(4*time.Second))
	if len(got) != 3 {
		t.Fatalf("second real press dropped: %d", len(got))
	}
}

func TestReaderNeedsReassertOnlyWhenTraceIsMissing(t *testing.T) {
	r := New(nil, nil, nil)
	now := time.Now()
	r.lastReassert = now.Add(-time.Hour)
	// Analytics line without any DEBUG trace around it: re-assert once.
	r.handleLine(lineRhinoThumbsUp, now)
	if !r.NeedsReassert(now.Add(100 * time.Millisecond)) {
		t.Fatal("expected a re-assert after a stats-only press")
	}
	if r.NeedsReassert(now.Add(200 * time.Millisecond)) {
		t.Fatal("re-assert must be rate-limited")
	}
	// With the trace present the analytics copy is explained: no re-assert.
	r2 := New(nil, nil, nil)
	r2.lastReassert = now.Add(-time.Hour)
	r2.handleLine(linePortableThumbsUpPress, now)
	r2.handleLine(lineRhinoThumbsUp, now.Add(50*time.Millisecond))
	if r2.NeedsReassert(now.Add(100 * time.Millisecond)) {
		t.Fatal("trace present, must not re-assert")
	}
}

func TestReaderHealthAndWindow(t *testing.T) {
	r := New(nil, nil, nil)
	if r.Healthy() {
		t.Fatal("not running yet")
	}
	r.mu.Lock()
	r.running = true
	r.mu.Unlock()
	r.handleLine(lineNoise, time.Now())
	if !r.Healthy() {
		t.Fatal("running with a fresh line must be healthy")
	}
	if r.KeyEventWithin(time.Second) {
		t.Fatal("noise is not a key event")
	}
	r.handleLine(linePortableConsolePreset1, time.Now())
	if !r.KeyEventWithin(time.Second) {
		t.Fatal("key event just arrived")
	}
	snap := r.Snapshot()
	if snap["traceSeen"] != true || snap["statsSeen"] != false {
		t.Fatalf("snapshot flags: %+v", snap)
	}
}

func TestEnableTraceRecordsReply(t *testing.T) {
	var sent []string
	r := New(nil, func(_ context.Context, cmd string) (string, error) {
		sent = append(sent, cmd)
		return "->OK\n", nil
	}, nil)
	r.enableTrace(context.Background())
	if len(sent) != 2 || !r.Snapshot()["traceLevelOK"].(bool) {
		t.Fatalf("sent=%v ok=%v", sent, r.Snapshot()["traceLevelOK"])
	}
	r2 := New(nil, func(_ context.Context, cmd string) (string, error) {
		return "->Command not found\n", nil
	}, nil)
	r2.enableTrace(context.Background())
	if r2.Snapshot()["traceLevelOK"].(bool) {
		t.Fatal("a refused loglevel must not read as OK")
	}
}

// TestEnableTraceRetriesUntilBoseAppRegistersTheCommand: the TAP server
// answers "Command not found" until BoseApp is up; the retry loop keeps going
// and stops at the first OK.
func TestEnableTraceRetriesUntilBoseAppRegistersTheCommand(t *testing.T) {
	calls := 0
	r := New(nil, func(_ context.Context, cmd string) (string, error) {
		calls++
		if calls < 3 {
			return "->Command not found\n->", nil
		}
		return "->OK\n", nil
	}, nil)
	r.retryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond}
	r.enableTraceWithRetry(context.Background())
	if !r.Snapshot()["traceLevelOK"].(bool) {
		t.Fatalf("expected OK after retries, calls=%d", calls)
	}
	// calls: attempt1 fails on cmd 1, attempt2 fails on cmd 1, attempt3 sends both.
	if calls != 4 {
		t.Fatalf("calls=%d", calls)
	}
	// A second round is a no-op once accepted.
	r.enableTraceWithRetry(context.Background())
	if calls != 4 {
		t.Fatalf("re-ran after success: calls=%d", calls)
	}
}
