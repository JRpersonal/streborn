package webui

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// The budget covers the whole form, not just the /setZone drive: waking the
// master, reading the live zone, removing dropped members, forming, and the
// confirming read. A flat ten seconds was therefore a ceiling on group size.
//
// Measured on a twelve-speaker fleet, 2026-08-08, one speaker added at a time:
// up to five slaves formed in 4 to 8 s, six took 22 s, and seven failed five
// times in a row with "setZone: context deadline exceeded". The same fleet had
// formed a group of twelve earlier the same afternoon, so nothing was wrong
// with the eighth speaker.
func TestZoneFormBudgetGrowsWithTheGroup(t *testing.T) {
	// The sizes that used to work must not become slower to fail.
	if got := zoneFormBudget(0); got < 10*time.Second {
		t.Errorf("zoneFormBudget(0) = %v, want at least the old 10s", got)
	}

	// The size that failed in the field must now have room. Six slaves were
	// measured at 22 s, so seven needs comfortably more than that.
	if got := zoneFormBudget(7); got <= 22*time.Second {
		t.Errorf("zoneFormBudget(7) = %v, want more than the 22s six slaves measured", got)
	}

	// Strictly increasing up to the ceiling, so a bigger group never gets less
	// time than a smaller one.
	prev := time.Duration(0)
	for n := 0; n <= 20; n++ {
		got := zoneFormBudget(n)
		if got < prev {
			t.Errorf("zoneFormBudget(%d) = %v is less than for %d slaves (%v)", n, got, n-1, prev)
		}
		prev = got
	}
}

// The agent must never answer after the desktop app has stopped listening: the
// app would report a failure for a group the firmware went on to build, which
// is the confusing half of this bug rather than the slow half. The app's own
// budget for this call is 45 s.
func TestZoneFormBudgetStaysUnderTheAppsTimeout(t *testing.T) {
	const appTimeout = 45 * time.Second
	for _, n := range []int{0, 1, 6, 7, 11, 50, 1000} {
		if got := zoneFormBudget(n); got >= appTimeout {
			t.Errorf("zoneFormBudget(%d) = %v, want comfortably under the app's %v", n, got, appTimeout)
		}
	}
}

// Verification polls every follower at once. Sequentially it could not finish:
// each follower had a 4 s budget of its own inside a form budget that stops at
// 38 s, so eleven followers needed up to 44 s and the ones at the end of the
// list were reported "missing" when the context died, although they had joined.
//
// Measured on a twelve-speaker fleet 2026-08-09: ok=true with every member
// listed, verified=3 on one attempt and 7 minutes later, a different set each
// time, and 11 of 11 the previous day when /setZone left more budget.
func TestFollowerVerificationRunsConcurrently(t *testing.T) {
	const followers = 11
	slaves := make([]boxapi.ZoneMember, followers)
	for i := range slaves {
		slaves[i] = boxapi.ZoneMember{DeviceID: fmt.Sprintf("DEV%02d", i), IP: fmt.Sprintf("192.0.2.%d", i+10)}
	}
	// Every follower takes almost its whole budget to confirm. Done one after
	// another that is 11 x 300ms; done together it is one 300ms wait.
	timing := followerVerifyTiming{
		perFollowerBudget: 400 * time.Millisecond,
		pollInterval:      50 * time.Millisecond,
		perCallTimeout:    400 * time.Millisecond,
	}
	fetch := func(ctx context.Context, ip string) (boxapi.Zone, error) {
		select {
		case <-time.After(300 * time.Millisecond):
		case <-ctx.Done():
			return boxapi.Zone{}, ctx.Err()
		}
		return boxapi.Zone{Master: "MASTER"}, nil
	}

	// A budget that comfortably fits one follower and could never fit eleven
	// in sequence: this is the field condition in miniature.
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	start := time.Now()
	missing, unverifiable := verifyFollowersJoinedTimed(ctx, slog.New(slog.DiscardHandler), "MASTER", slaves, fetch, timing)
	elapsed := time.Since(start)

	if len(missing) != 0 {
		t.Errorf("%d of %d followers reported missing although every one confirmed: %v", len(missing), followers, missing)
	}
	if len(unverifiable) != 0 {
		t.Errorf("unexpected unverifiable: %v", unverifiable)
	}
	if elapsed > time.Second {
		t.Errorf("verification took %v for %d followers, which means they were still polled one after another", elapsed, followers)
	}
}

// A follower with no address cannot be polled and must be reported separately
// rather than counted as missing, and the order must follow the request so the
// output does not shuffle between runs.
func TestFollowerVerificationKeepsOrderAndSeparatesUnverifiable(t *testing.T) {
	slaves := []boxapi.ZoneMember{
		{DeviceID: "A", IP: "192.0.2.10"},
		{DeviceID: "B"}, // no address
		{DeviceID: "C", IP: "192.0.2.12"},
		{DeviceID: "D", IP: "192.0.2.13"},
	}
	timing := followerVerifyTiming{perFollowerBudget: 60 * time.Millisecond, pollInterval: 10 * time.Millisecond, perCallTimeout: 60 * time.Millisecond}
	fetch := func(_ context.Context, ip string) (boxapi.Zone, error) {
		if ip == "192.0.2.12" {
			return boxapi.Zone{Master: "SOMEONE_ELSE"}, nil // joined the wrong master
		}
		return boxapi.Zone{Master: "MASTER"}, nil
	}
	missing, unverifiable := verifyFollowersJoinedTimed(context.Background(), slog.New(slog.DiscardHandler), "MASTER", slaves, fetch, timing)
	if len(unverifiable) != 1 || unverifiable[0] != "B" {
		t.Errorf("unverifiable = %v, want just the follower with no address", unverifiable)
	}
	if len(missing) != 1 || missing[0] != "C" {
		t.Errorf("missing = %v, want just the follower that named another master", missing)
	}
}
