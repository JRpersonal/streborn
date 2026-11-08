package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

type stuckCase struct {
	uptime    time.Duration
	standby   bool
	stateErr  error
	last      time.Time // zero: no stamp
	stampErr  error
	rebootErr error
}

func runStuckRestart(c stuckCase) (started bool, stamped, reboots int) {
	now := time.Date(2026, 11, 8, 22, 0, 0, 0, time.UTC)
	r := stuckDisplayRestart{
		uptime:    func() time.Duration { return c.uptime },
		inStandby: func(context.Context) (bool, error) { return c.standby, c.stateErr },
		lastAt:    func() (time.Time, bool) { return c.last, !c.last.IsZero() },
		stamp: func(time.Time) error {
			stamped++
			return c.stampErr
		},
		reboot: func() error {
			reboots++
			return c.rebootErr
		},
		now:    func() time.Time { return now },
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return r.run(context.Background()), stamped, reboots
}

// #841 / #1784: a speaker whose display stopped drawing gets one restart
// while it sleeps, never a loop and never under someone's hands.
func TestStuckDisplayRestartRules(t *testing.T) {
	now := time.Date(2026, 11, 8, 22, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		c       stuckCase
		started bool
		stamped int
		reboots int
	}{
		{"sleeping, up for hours, never restarted", stuckCase{uptime: 51 * time.Hour, standby: true}, true, 1, 1},
		{"restarted for this 25 h ago", stuckCase{uptime: 3 * time.Hour, standby: true, last: now.Add(-25 * time.Hour)}, true, 1, 1},
		{"uptime unreadable", stuckCase{uptime: -1, standby: true}, true, 1, 1},
		{"booted ten minutes ago", stuckCase{uptime: 10 * time.Minute, standby: true}, false, 0, 0},
		{"restarted for this 3 h ago", stuckCase{uptime: 3 * time.Hour, standby: true, last: now.Add(-3 * time.Hour)}, false, 0, 0},
		{"stamp from a clock that ran ahead", stuckCase{uptime: 3 * time.Hour, standby: true, last: now.Add(5 * time.Hour)}, false, 0, 0},
		{"playing again", stuckCase{uptime: 3 * time.Hour, standby: false}, false, 0, 0},
		{"state unreadable", stuckCase{uptime: 3 * time.Hour, stateErr: errors.New("timeout")}, false, 0, 0},
		{"stamp cannot be written", stuckCase{uptime: 3 * time.Hour, standby: true, stampErr: errors.New("read-only")}, false, 1, 0},
		{"reboot fails", stuckCase{uptime: 3 * time.Hour, standby: true, rebootErr: errors.New("no reboot")}, false, 1, 1},
	}
	for _, tc := range cases {
		started, stamped, reboots := runStuckRestart(tc.c)
		if started != tc.started || stamped != tc.stamped || reboots != tc.reboots {
			t.Errorf("%s: started=%v stamped=%d reboots=%d; want %v %d %d",
				tc.name, started, stamped, reboots, tc.started, tc.stamped, tc.reboots)
		}
	}
}

func TestStuckDisplayStampRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "display-restart-at")
	if _, ok := readStuckDisplayStamp(path); ok {
		t.Fatal("no file must read as no stamp")
	}
	at := time.Unix(1794161817, 0)
	if err := writeStuckDisplayStamp(path, at); err != nil {
		t.Fatal(err)
	}
	got, ok := readStuckDisplayStamp(path)
	if !ok || !got.Equal(at) {
		t.Fatalf("read back %v %v, want %v", got, ok, at)
	}
}
