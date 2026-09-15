package webui

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// The escalation exists because a master can take every teardown, answer 2xx,
// and keep the group. Juergen's SoundTouch 30 did that nine times in a row.
// Pinned here: the follower-side route is actually reached, and it is reached
// only after the cheaper one has been tried.

func withFollowerLeave(t *testing.T, fn func(ctx context.Context, followerIP string, master, follower boxapi.ZoneMember) error) *[]string {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	prev := leaveZoneAtFollowerFn
	leaveZoneAtFollowerFn = func(ctx context.Context, ip string, master, follower boxapi.ZoneMember) error {
		mu.Lock()
		seen = append(seen, ip)
		mu.Unlock()
		if fn != nil {
			return fn(ctx, ip, master, follower)
		}
		return nil
	}
	t.Cleanup(func() { leaveZoneAtFollowerFn = prev })
	return &seen
}

func TestMemberIDsNamesEverySpeakerOnce(t *testing.T) {
	ms := []boxapi.ZoneMember{
		{DeviceID: "AAA", IP: "192.0.2.2"},
		{IP: "192.0.2.3"}, // no device id: the address is the name
	}
	if got, want := memberIDs(ms), "AAA,192.0.2.3"; got != want {
		t.Errorf("memberIDs = %q, want %q", got, want)
	}
	if memberIDs(nil) != "" {
		t.Error("an empty group must name nothing")
	}
}

// An already-empty group must not start the escalation at all: the whole point
// is that it only runs when the master kept the members.
func TestNoEscalationWhenTheGroupIsAlreadyGone(t *testing.T) {
	seen := withFollowerLeave(t, nil)
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	left, unverified := s.escalateDissolve(boxapi.New("127.0.0.1:1"), boxapi.ZoneMember{DeviceID: "M"}, nil)
	if len(left) != 0 || unverified != "" {
		t.Errorf("left=%v unverified=%q, want nothing to do", left, unverified)
	}
	if len(*seen) != 0 {
		t.Errorf("the follower route was used on an empty group: %v", *seen)
	}
}

// An unreadable zone is not an empty one. The dissolve must come back
// "unverified" rather than claim the group is gone, which is the same rule the
// batch loop already follows.
func TestAnUnreadableZoneIsReportedAsUnverified(t *testing.T) {
	seen := withFollowerLeave(t, nil)
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	// Nothing listening on this port, so every read and write fails fast.
	c := boxapi.New("127.0.0.1:1")
	left, unverified := s.escalateDissolve(c, boxapi.ZoneMember{DeviceID: "M"},
		[]boxapi.ZoneMember{{DeviceID: "A", IP: "192.0.2.2"}})
	if unverified == "" {
		t.Error("an unreadable zone was reported as a clean dissolve")
	}
	if len(left) == 0 {
		t.Error("the members were dropped although nothing confirmed they left")
	}
	// It still tried the follower itself before giving up: that is the route
	// the user-driven dissolve never had.
	if len(*seen) != 1 || (*seen)[0] != "192.0.2.2" {
		t.Errorf("follower route calls = %v, want one call at the follower's own address", *seen)
	}
}
