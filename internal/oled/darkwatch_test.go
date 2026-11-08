package oled

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

// Samples for the watch tests: a drawn clock whose picture changes every
// minute, and the two dark frames seen on real speakers.
var (
	sampleClock1 = panelSample{sum: 1}
	sampleClock2 = panelSample{sum: 2}
	sampleClock3 = panelSample{sum: 3}
	sampleClock4 = panelSample{sum: 4}
	sampleBlack  = panelSample{dark: true, fill: 0x00, sum: 100}
	sampleBose01 = panelSample{dark: true, fill: 0x01, sum: 101}
)

type darkCase struct {
	frames     []panelSample
	unreadable bool
	ours       bool
	superseded bool
	// expected answers ClockExpected, one entry per call (the last repeats)
	expected  []bool
	expectErr error
	noHooks   bool
	noRestart bool
	restartOK bool
}

type darkRun struct {
	verdict  darkVerdict
	nudges   int
	restarts int
	looks    int
}

func runDarkWatch(c darkCase) darkRun {
	var r darkRun
	asked := 0
	hooks := DarkPanelHooks{
		ClockExpected: func(context.Context) (bool, error) {
			i := min(asked, len(c.expected)-1)
			asked++
			if i < 0 {
				return false, c.expectErr
			}
			return c.expected[i], c.expectErr
		},
		Recover: func(context.Context) bool { r.nudges++; return true },
		Restart: func(context.Context) bool { r.restarts++; return c.restartOK },
	}
	if c.noRestart {
		hooks.Restart = nil
	}
	if c.noHooks {
		hooks = DarkPanelHooks{}
	}
	w := darkWatch{
		hooks: hooks,
		readFrame: func() (panelSample, bool) {
			s := c.frames[r.looks]
			r.looks++
			return s, !c.unreadable
		},
		panelOurs:    func() bool { return c.ours },
		threadInfo:   func() []any { return []any{"state", "S (sleeping)"} },
		current:      func() bool { return !c.superseded },
		sleep:        func(context.Context, time.Duration) bool { return true },
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		first:        darkFirstLook,
		second:       darkSecondLook,
		afterNudge:   darkAfterNudge,
		restartAfter: darkRestartAfter,
	}
	r.verdict = w.run(context.Background(), "source STANDBY")
	return r
}

// #841 / #1784: the standby clock stayed dark after the speaker was switched
// off. Only a panel that is dark or stands still across a minute, while the
// firmware should be drawing its clock, gets the nudge; only one the nudge
// did not bring back, still stuck minutes later and still in standby with
// the clock on, gets the restart.
func TestDarkWatchSteps(t *testing.T) {
	on := []bool{true}
	cases := []struct {
		name string
		c    darkCase
		want darkRun
	}{
		{"a working clock changes within the minute",
			darkCase{frames: []panelSample{sampleClock1, sampleClock2}, expected: on},
			darkRun{verdict: darkPainted, looks: 2}},
		{"black at 2 s, clock drawn by 65 s",
			darkCase{frames: []panelSample{sampleBlack, sampleClock1}, expected: on},
			darkRun{verdict: darkPainted, looks: 2}},
		{"STR's black release frame both times, the nudge brings the clock back",
			darkCase{frames: []panelSample{sampleBlack, sampleBlack, sampleClock1}, expected: on},
			darkRun{verdict: darkRecovered, nudges: 1, looks: 3}},
		{"BoseApp's 0x01 blank (#841 readout) counts as dark",
			darkCase{frames: []panelSample{sampleBose01, sampleBose01, sampleClock1}, expected: on},
			darkRun{verdict: darkRecovered, nudges: 1, looks: 3}},
		{"STR's 0x00, then BoseApp's 0x01 and nothing more",
			darkCase{frames: []panelSample{sampleBlack, sampleBose01, sampleBose01, sampleBose01}, expected: on, restartOK: true},
			darkRun{verdict: darkRestarted, nudges: 1, restarts: 1, looks: 4}},
		{"a frozen clock stands still across the minute",
			darkCase{frames: []panelSample{sampleClock1, sampleClock1, sampleClock2}, expected: on},
			darkRun{verdict: darkRecovered, nudges: 1, looks: 3}},
		{"frozen through the nudge and the wait: restart",
			darkCase{frames: []panelSample{sampleClock1, sampleClock1, sampleClock1, sampleClock1}, expected: on, restartOK: true},
			darkRun{verdict: darkRestarted, nudges: 1, restarts: 1, looks: 4}},
		{"stuck after the nudge, but drawing again before the restart",
			darkCase{frames: []panelSample{sampleBose01, sampleBose01, sampleBose01, sampleClock3}, expected: on},
			darkRun{verdict: darkPainted, nudges: 1, looks: 4}},
		{"stuck, but the speaker woke before the restart",
			darkCase{frames: []panelSample{sampleBose01, sampleBose01, sampleBose01, sampleBose01}, expected: []bool{true, false}},
			darkRun{verdict: darkNotClock, nudges: 1, looks: 4}},
		{"stuck, the restart hook declines",
			darkCase{frames: []panelSample{sampleBose01, sampleBose01, sampleBose01, sampleBose01}, expected: on},
			darkRun{verdict: darkStuck, nudges: 1, restarts: 1, looks: 4}},
		{"stuck, no restart hook wired",
			darkCase{frames: []panelSample{sampleBose01, sampleBose01, sampleBose01}, expected: on, noRestart: true},
			darkRun{verdict: darkStuck, nudges: 1, looks: 3}},
		{"the picture after the nudge changed",
			darkCase{frames: []panelSample{sampleClock1, sampleClock1, sampleClock4}, expected: on},
			darkRun{verdict: darkRecovered, nudges: 1, looks: 3}},
		{"dark, but no clock expected",
			darkCase{frames: []panelSample{sampleBlack, sampleBlack}, expected: []bool{false}},
			darkRun{verdict: darkNotClock, looks: 2}},
		{"dark, state unreadable",
			darkCase{frames: []panelSample{sampleBlack, sampleBlack}, expected: on, expectErr: errors.New("timeout")},
			darkRun{verdict: darkNotClock, looks: 2}},
		{"framebuffer unreadable",
			darkCase{frames: []panelSample{sampleBlack}, unreadable: true, expected: on},
			darkRun{verdict: darkUnreadable, looks: 1}},
		{"STR holds the panel again",
			darkCase{frames: []panelSample{sampleBlack}, ours: true, expected: on},
			darkRun{verdict: darkSuperseded}},
		{"a newer release took over",
			darkCase{frames: []panelSample{sampleBlack}, superseded: true, expected: on},
			darkRun{verdict: darkSuperseded}},
		{"not wired",
			darkCase{noHooks: true},
			darkRun{verdict: darkNoHooks}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runDarkWatch(tc.c); got != tc.want {
				t.Fatalf("got %+v; want %+v", got, tc.want)
			}
		})
	}
}

// The 0x01 frame read off a stuck ST20 (#841, 2026-11-08) and STR's own 0x00
// release frame are both dark; any picture, and a uniform frame brighter than
// STR's lowest grey, is not.
func TestFrameDarkFill(t *testing.T) {
	uniform := func(v byte) []byte {
		f := make([]byte, FrameSize)
		for i := range f {
			f[i] = v
		}
		return f
	}
	for _, v := range []byte{0x00, 0x01, 0x02, 0x11} {
		if fill, dark := frameDarkFill(uniform(v)); !dark || fill != v {
			t.Errorf("uniform 0x%02x: dark=%v fill=0x%02x, want dark", v, dark, fill)
		}
	}
	if _, dark := frameDarkFill(uniform(0x22)); dark {
		t.Error("a uniform 0x22 frame is a lit panel, not dark")
	}
	// one lit pixel anywhere, also one the strided pass skips
	for _, at := range []int{0, 1, 60, 61, FrameSize / 2, FrameSize - 1} {
		f := uniform(0x01)
		f[at] = 0x11
		if _, dark := frameDarkFill(f); dark {
			t.Errorf("a lit pixel at %d was missed", at)
		}
	}
	if _, dark := frameDarkFill(nil); dark {
		t.Error("an empty read is not dark")
	}
}

func TestSamplePanelFrameTellsPicturesApart(t *testing.T) {
	a := make([]byte, FrameSize)
	b := make([]byte, FrameSize)
	b[FrameSize/2] = 0xFF // one changed digit pixel
	sa, sb := samplePanelFrame(a), samplePanelFrame(b)
	if !sa.dark || sb.dark {
		t.Fatalf("dark flags: %v %v", sa.dark, sb.dark)
	}
	if sa.sum == sb.sum {
		t.Fatal("two different pictures gave the same sum")
	}
	if samplePanelFrame(b).sum != sb.sum {
		t.Fatal("the same picture gave two sums")
	}
	if !sb.stuckAgainst(&sb) || sb.stuckAgainst(&sa) || sb.stuckAgainst(nil) {
		t.Fatal("stuckAgainst compares pictures")
	}
}

func TestStandbyMayStartWatch(t *testing.T) {
	t0 := time.Date(2026, 11, 8, 18, 54, 1, 0, time.UTC)
	cases := []struct {
		name      string
		live, gen uint64
		after     time.Duration
		want      bool
	}{
		{"no watch running", 0, 7, 0, true},
		{"the running one was superseded", 6, 7, 0, true},
		{"syslog standby right after the gabbo one", 7, 7, 500 * time.Millisecond, false},
		{"a flap within the minute", 7, 7, 30 * time.Second, false},
		{"switched off again minutes later", 7, 7, 9 * time.Minute, true},
	}
	for _, tc := range cases {
		if got := standbyMayStartWatch(tc.live, tc.gen, t0, t0.Add(tc.after)); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestProcStatusFields(t *testing.T) {
	st, tr := procStatusFields("Name:\tDirectFBFlipTas\nState:\tt (tracing stop)\nTgid:\t812\nTracerPid:\t4242\n")
	if st != "t (tracing stop)" || tr != "4242" {
		t.Fatalf("state %q tracer %q", st, tr)
	}
	if st, tr := procStatusFields(""); st != "" || tr != "" {
		t.Fatalf("empty status gave %q %q", st, tr)
	}
}
