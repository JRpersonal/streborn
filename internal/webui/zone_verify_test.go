package webui

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// Fast polling bounds so the never-joins cases finish in milliseconds.
var testVerifyTiming = followerVerifyTiming{
	perFollowerBudget: 150 * time.Millisecond,
	pollInterval:      10 * time.Millisecond,
	perCallTimeout:    50 * time.Millisecond,
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// verifyFollowersJoined is the single authority for "did the zone actually
// form" (#70): it decides the missing/unverifiable sets behind the ok flag the
// app shows. These tests pin its decision table via the followerZoneFetch seam.
func TestVerifyFollowersJoined(t *testing.T) {
	const master = "MASTER00DEVICE"

	t.Run("all followers join immediately", func(t *testing.T) {
		slaves := []boxapi.ZoneMember{
			{DeviceID: "follower-1", IP: "192.0.2.11"},
			{DeviceID: "follower-2", IP: "192.0.2.12"},
		}
		fetch := func(_ context.Context, _ string) (boxapi.Zone, error) {
			return boxapi.Zone{Master: master}, nil
		}
		missing, unverifiable := verifyFollowersJoinedTimed(context.Background(), discardLogger(), master, slaves, fetch, testVerifyTiming)
		if len(missing) != 0 || len(unverifiable) != 0 {
			t.Errorf("missing = %v, unverifiable = %v, want both empty", missing, unverifiable)
		}
	})

	t.Run("follower lags then joins within the budget", func(t *testing.T) {
		// A slave's own /getZone lags forming by ~100ms to seconds; the verify
		// must poll rather than judge from the first read.
		calls := 0
		fetch := func(_ context.Context, _ string) (boxapi.Zone, error) {
			calls++
			if calls < 3 {
				return boxapi.Zone{}, nil // still standalone
			}
			return boxapi.Zone{Master: master}, nil
		}
		slaves := []boxapi.ZoneMember{{DeviceID: "follower-1", IP: "192.0.2.11"}}
		missing, unverifiable := verifyFollowersJoinedTimed(context.Background(), discardLogger(), master, slaves, fetch, testVerifyTiming)
		if len(missing) != 0 || len(unverifiable) != 0 {
			t.Errorf("missing = %v, unverifiable = %v, want both empty", missing, unverifiable)
		}
		if calls < 3 {
			t.Errorf("fetch called %d times, want >= 3 (the verify must poll)", calls)
		}
	})

	t.Run("follower that never joins is missing", func(t *testing.T) {
		fetch := func(_ context.Context, _ string) (boxapi.Zone, error) {
			return boxapi.Zone{Master: "SOME-OTHER-MASTER"}, nil
		}
		slaves := []boxapi.ZoneMember{{DeviceID: "follower-1", IP: "192.0.2.11"}}
		missing, unverifiable := verifyFollowersJoinedTimed(context.Background(), discardLogger(), master, slaves, fetch, testVerifyTiming)
		if len(missing) != 1 || missing[0] != "follower-1" {
			t.Errorf("missing = %v, want [follower-1]", missing)
		}
		if len(unverifiable) != 0 {
			t.Errorf("unverifiable = %v, want empty", unverifiable)
		}
	})

	t.Run("unreachable follower self-report is missing", func(t *testing.T) {
		// The follower's :8090 never answers: it cannot be confirmed, and a
		// box that is unreachable most likely did not join either - report it
		// missing so the app flags it instead of claiming success (#70).
		fetch := func(_ context.Context, _ string) (boxapi.Zone, error) {
			return boxapi.Zone{}, errors.New("connection refused")
		}
		slaves := []boxapi.ZoneMember{{DeviceID: "follower-1", IP: "192.0.2.11"}}
		missing, _ := verifyFollowersJoinedTimed(context.Background(), discardLogger(), master, slaves, fetch, testVerifyTiming)
		if len(missing) != 1 || missing[0] != "follower-1" {
			t.Errorf("missing = %v, want [follower-1]", missing)
		}
	})

	t.Run("follower without an IP is unverifiable, never fetched", func(t *testing.T) {
		fetch := func(_ context.Context, ip string) (boxapi.Zone, error) {
			t.Errorf("fetch called for a follower with no IP (ip=%q)", ip)
			return boxapi.Zone{}, nil
		}
		slaves := []boxapi.ZoneMember{{DeviceID: "follower-1", IP: ""}}
		missing, unverifiable := verifyFollowersJoinedTimed(context.Background(), discardLogger(), master, slaves, fetch, testVerifyTiming)
		if len(unverifiable) != 1 || unverifiable[0] != "follower-1" {
			t.Errorf("unverifiable = %v, want [follower-1]", unverifiable)
		}
		if len(missing) != 0 {
			t.Errorf("missing = %v, want empty (no IP is not proof of failure)", missing)
		}
	})

	t.Run("master ID match is case-insensitive", func(t *testing.T) {
		// deviceIDs arrive mixed-case: the firmware self-report may differ in
		// case from the app-supplied master ID.
		fetch := func(_ context.Context, _ string) (boxapi.Zone, error) {
			return boxapi.Zone{Master: "master00device"}, nil
		}
		slaves := []boxapi.ZoneMember{{DeviceID: "follower-1", IP: "192.0.2.11"}}
		missing, _ := verifyFollowersJoinedTimed(context.Background(), discardLogger(), master, slaves, fetch, testVerifyTiming)
		if len(missing) != 0 {
			t.Errorf("missing = %v, want empty (lower-case self-report must match)", missing)
		}
	})

	t.Run("cancelled context stops polling promptly", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		calls := 0
		fetch := func(_ context.Context, _ string) (boxapi.Zone, error) {
			calls++
			return boxapi.Zone{}, nil
		}
		slaves := []boxapi.ZoneMember{
			{DeviceID: "follower-1", IP: "192.0.2.11"},
			{DeviceID: "follower-2", IP: "192.0.2.12"},
		}
		missing, _ := verifyFollowersJoinedTimed(ctx, discardLogger(), master, slaves, fetch, testVerifyTiming)
		if len(missing) != 2 {
			t.Errorf("missing = %v, want both followers (nothing was confirmed)", missing)
		}
		if calls > 2 {
			t.Errorf("fetch called %d times on a cancelled context, want at most one per follower", calls)
		}
	})
}
