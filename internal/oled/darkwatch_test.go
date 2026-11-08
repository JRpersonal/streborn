package oled

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

type darkCase struct {
	frames     []bool // dark at each look
	unreadable bool
	ours       bool
	superseded bool
	expected   bool
	expectErr  error
	noHooks    bool
}

func runDarkWatch(c darkCase) (v darkVerdict, recovers int) {
	looks := 0
	hooks := DarkPanelHooks{
		ClockExpected: func(context.Context) (bool, error) { return c.expected, c.expectErr },
		Recover:       func(context.Context) bool { recovers++; return true },
	}
	if c.noHooks {
		hooks = DarkPanelHooks{}
	}
	w := darkWatch{
		hooks: hooks,
		frameDark: func() (bool, bool) {
			d := c.frames[looks]
			looks++
			return d, !c.unreadable
		},
		panelOurs:  func() bool { return c.ours },
		threadInfo: func() []any { return []any{"state", "S (sleeping)"} },
		current:    func() bool { return !c.superseded },
		sleep:      func(context.Context, time.Duration) bool { return true },
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		first:      darkFirstLook,
		second:     darkSecondLook,
	}
	return w.run(context.Background(), "source STANDBY"), recovers
}

// #841: the standby clock stayed blank after the track overlay released the
// display. Only a panel black at both looks, while the firmware should be
// drawing its clock, gets the nudge; everything else is left alone.
func TestDarkWatchNudgesOnlyAStandbyClockThatStaysBlack(t *testing.T) {
	cases := []struct {
		name string
		c    darkCase
		want darkVerdict
		nudg int
	}{
		{"black twice in standby with the clock on", darkCase{frames: []bool{true, true}, expected: true}, darkRecovered, 1},
		{"painted at the first look", darkCase{frames: []bool{false}}, darkPainted, 0},
		{"painted by the second look", darkCase{frames: []bool{true, false}, expected: true}, darkPainted, 0},
		{"black, but no clock expected", darkCase{frames: []bool{true, true}, expected: false}, darkNotClock, 0},
		{"black, state unreadable", darkCase{frames: []bool{true, true}, expectErr: errors.New("timeout")}, darkNotClock, 0},
		{"framebuffer unreadable", darkCase{frames: []bool{true}, unreadable: true}, darkUnreadable, 0},
		{"STR holds the panel again", darkCase{frames: []bool{true}, ours: true}, darkSuperseded, 0},
		{"a newer release took over", darkCase{frames: []bool{true}, superseded: true}, darkSuperseded, 0},
		{"not wired", darkCase{noHooks: true}, darkNoHooks, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, n := runDarkWatch(tc.c)
			if v != tc.want || n != tc.nudg {
				t.Fatalf("verdict %q, nudges %d; want %q, %d", v, n, tc.want, tc.nudg)
			}
		})
	}
}

func TestFrameIsDark(t *testing.T) {
	f := make([]byte, FrameSize)
	if !frameIsDark(f) {
		t.Fatal("a zero frame is dark")
	}
	f[FrameSize-1] = 0x11
	if frameIsDark(f) {
		t.Fatal("one lit pixel is not dark")
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
