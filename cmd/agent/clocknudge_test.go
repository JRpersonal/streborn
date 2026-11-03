package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

type nudgeCase struct {
	ready      bool
	has, hasOK bool
	standby    bool
	readErr    error
	sendErr    error
}

func runNudge(c nudgeCase) (sent bool, standbyCalls int) {
	n := clockNudge{
		displayReady: func() bool { return c.ready },
		hasDisplay:   func(context.Context) (bool, bool) { return c.has, c.hasOK },
		inStandby:    func(context.Context) (bool, error) { return c.standby, c.readErr },
		standby: func(context.Context) error {
			standbyCalls++
			return c.sendErr
		},
		sleep:     func(context.Context, time.Duration) bool { return true },
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		readyWait: 0,
		settle:    0,
	}
	return n.run(context.Background()), standbyCalls
}

func TestClockNudgeSendsOneStandbyToASleepingSpeaker(t *testing.T) {
	sent, calls := runNudge(nudgeCase{ready: true, has: true, hasOK: true, standby: true})
	if !sent || calls != 1 {
		t.Fatalf("sleeping speaker with a display: sent=%v calls=%d, want one standby", sent, calls)
	}
	// A speaker that does not say whether it has a display still gets it: the
	// request is harmless on a sleeping speaker either way.
	if sent, calls := runNudge(nudgeCase{ready: true, standby: true}); !sent || calls != 1 {
		t.Fatalf("unknown display: sent=%v calls=%d, want one standby", sent, calls)
	}
}

func TestClockNudgeNeverTouchesAPlayingOrUnknownSpeaker(t *testing.T) {
	cases := map[string]nudgeCase{
		"playing":               {ready: true, has: true, hasOK: true, standby: false},
		"state unreadable":      {ready: true, has: true, hasOK: true, readErr: errors.New("timeout")},
		"no display":            {ready: true, has: false, hasOK: true, standby: true},
		"display never came up": {ready: false, has: true, hasOK: true, standby: true},
	}
	for name, c := range cases {
		if sent, calls := runNudge(c); sent || calls != 0 {
			t.Errorf("%s: sent=%v calls=%d, want no standby", name, sent, calls)
		}
	}
}

func TestClockNudgeReportsAFailedRequest(t *testing.T) {
	sent, calls := runNudge(nudgeCase{ready: true, has: true, hasOK: true, standby: true, sendErr: errors.New("500")})
	if sent || calls != 1 {
		t.Fatalf("failed request: sent=%v calls=%d, want one attempt reported as not sent", sent, calls)
	}
}

func TestClockNudgeRetriesASlowStateRead(t *testing.T) {
	reads, calls := 0, 0
	n := clockNudge{
		displayReady: func() bool { return true },
		hasDisplay:   func(context.Context) (bool, bool) { return false, false },
		inStandby: func(context.Context) (bool, error) {
			reads++
			if reads < 3 {
				return false, errors.New("timeout")
			}
			return true, nil
		},
		standby:    func(context.Context) error { calls++; return nil },
		sleep:      func(context.Context, time.Duration) bool { return true },
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		stateTries: 4,
	}
	if !n.run(context.Background()) || reads != 3 || calls != 1 {
		t.Fatalf("slow state read: reads=%d standby calls=%d, want 3 reads and one standby", reads, calls)
	}
}
