package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
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

// The dark-panel check (#841) only nudges when the firmware should be drawing
// its clock: in standby with the clock switched on. A clock the owner turned
// off, or a speaker without one, is black in standby by design.
func TestClockExpected(t *testing.T) {
	cfg := func(enable string, err error) func(context.Context) (boxapi.ClockConfig, error) {
		return func(context.Context) (boxapi.ClockConfig, error) {
			return boxapi.ClockConfig{UserEnable: enable}, err
		}
	}
	standby := func(s bool, err error) func(context.Context) (bool, error) {
		return func(context.Context) (bool, error) { return s, err }
	}
	cases := []struct {
		name    string
		standby func(context.Context) (bool, error)
		cfg     func(context.Context) (boxapi.ClockConfig, error)
		want    bool
		wantErr bool
	}{
		{"standby, clock on", standby(true, nil), cfg("true", nil), true, false},
		{"standby, clock off", standby(true, nil), cfg("false", nil), false, false},
		{"standby, no clock on this speaker", standby(true, nil), cfg("", boxapi.ErrNoClockConfig), false, false},
		{"standby, clock unreadable", standby(true, nil), cfg("", errors.New("timeout")), false, true},
		{"playing", standby(false, nil), cfg("true", nil), false, false},
		{"state unreadable", standby(false, errors.New("timeout")), cfg("true", nil), false, true},
	}
	for _, tc := range cases {
		got, err := clockExpected(context.Background(), tc.standby, tc.cfg)
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("%s: got %v, err %v", tc.name, got, err)
		}
	}
}
