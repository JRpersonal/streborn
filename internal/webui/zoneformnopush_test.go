package webui

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// Forming a TWO-speaker group briefly interrupted the music, while adding the
// 3rd and 4th did not. Measured on a SoundTouch 10 on 2026-09-24: the master
// reported PLAY_STATE 1.5 s after the form, STR pushed the stream anyway, and
// the speaker was silent for about 2.5 s.
//
// The push was made unconditional on a fresh form because skipping it had once
// left freshly joined members silent while the master played on (Martin,
// 2026-08-24). From the master alone those two states are identical, so the
// members are asked now. Every wrong "yes" here is Martin's bug again, which is
// why the predicate is pessimistic about everything it cannot confirm.

func memberState(source, playStatus, location string) nowPlayingSnapshot {
	return nowPlayingSnapshot{Source: source, PlayStatus: playStatus, Location: location}
}

// withMemberStates replaces the member read seam for one test.
func withMemberStates(t *testing.T, byIP map[string]nowPlayingSnapshot) {
	t.Helper()
	prev := memberNowPlaying
	memberNowPlaying = func(_ context.Context, ip string) nowPlayingSnapshot {
		return byIP[ip]
	}
	t.Cleanup(func() { memberNowPlaying = prev })
}

func carryServer(t *testing.T) *Server {
	t.Helper()
	s, _ := newPlayTestServer(t)
	s.boxHost = "192.0.2.1"
	return s
}

const groupLoc = "http://192.0.2.1:8888/stream/1"

func TestEveryMemberCarrying_SkipsThePush(t *testing.T) {
	s := carryServer(t)
	withMemberStates(t, map[string]nowPlayingSnapshot{
		"192.0.2.2": memberState("GROUP_SLAVE", "PLAY_STATE", ""),
		"192.0.2.3": memberState("UPNP", "PLAY_STATE", groupLoc),
	})
	ok, why := s.everyMemberCarriesTheGroup(context.Background(),
		[]boxapi.ZoneMember{{IP: "192.0.2.2"}, {IP: "192.0.2.3"}}, groupLoc)
	if !ok {
		t.Fatalf("both members are playing the group's stream but the push would still fire: %s", why)
	}
}

// Martin's case, and the one that must never regress: the master plays, the
// freshly joined member does not.
func TestASilentFreshMemberStillGetsThePush(t *testing.T) {
	s := carryServer(t)
	withMemberStates(t, map[string]nowPlayingSnapshot{
		"192.0.2.2": memberState("INVALID_SOURCE", "STOP_STATE", ""),
	})
	if ok, _ := s.everyMemberCarriesTheGroup(context.Background(),
		[]boxapi.ZoneMember{{IP: "192.0.2.2"}}, groupLoc); ok {
		t.Fatal("a silent freshly joined member was read as served; Martin's bug is back")
	}
}

// A follower can report GROUP_SLAVE while sitting in STOP_STATE. That is
// exactly a silent member, and the source name alone must not excuse it.
func TestAGroupSlaveThatIsNotPlayingIsNotServed(t *testing.T) {
	s := carryServer(t)
	withMemberStates(t, map[string]nowPlayingSnapshot{
		"192.0.2.2": memberState("GROUP_SLAVE", "STOP_STATE", ""),
	})
	if ok, _ := s.everyMemberCarriesTheGroup(context.Background(),
		[]boxapi.ZoneMember{{IP: "192.0.2.2"}}, groupLoc); ok {
		t.Fatal("GROUP_SLAVE in STOP_STATE counted as carrying the group")
	}
}

// One good member does not vouch for a bad one.
func TestOneMemberNotCarryingIsEnoughToPush(t *testing.T) {
	s := carryServer(t)
	withMemberStates(t, map[string]nowPlayingSnapshot{
		"192.0.2.2": memberState("GROUP_SLAVE", "PLAY_STATE", ""),
		"192.0.2.3": memberState("BLUETOOTH", "PLAY_STATE", "bt://x"),
	})
	if ok, _ := s.everyMemberCarriesTheGroup(context.Background(),
		[]boxapi.ZoneMember{{IP: "192.0.2.2"}, {IP: "192.0.2.3"}}, groupLoc); ok {
		t.Fatal("a member playing its own source counted as carrying the group")
	}
}

// Unreadable is not "fine". The historical safe behaviour is to push.
func TestAnUnreadableMemberPushes(t *testing.T) {
	s := carryServer(t)
	withMemberStates(t, map[string]nowPlayingSnapshot{}) // every read comes back empty
	if ok, _ := s.everyMemberCarriesTheGroup(context.Background(),
		[]boxapi.ZoneMember{{IP: "192.0.2.2"}}, groupLoc); ok {
		t.Fatal("an unreadable member counted as carrying the group")
	}
}

// A caller that does not know who joined cannot claim they are served. This is
// also what keeps TestResumeAfterZoneFormPushesTheSurvivorOnAFreshForm honest:
// it passes no members, so it must still push.
func TestNoMembersMeansPush(t *testing.T) {
	s := carryServer(t)
	if ok, _ := s.everyMemberCarriesTheGroup(context.Background(), nil, groupLoc); ok {
		t.Fatal("an empty member list counted as served")
	}
}

// The master itself is in the member list on some paths; asking it about itself
// proves nothing, and if it were the only entry there is nobody to vouch.
func TestOnlyTheMasterItselfMeansPush(t *testing.T) {
	s := carryServer(t)
	withMemberStates(t, map[string]nowPlayingSnapshot{})
	if ok, _ := s.everyMemberCarriesTheGroup(context.Background(),
		[]boxapi.ZoneMember{{IP: "192.0.2.1"}}, groupLoc); ok {
		t.Fatal("a list containing only the master counted as served")
	}
}

// The location the box reports and the URL STR would push are the SAME station
// in two different encodings: a native start reports an orion-encoded location
// while lastPlay holds the loopback proxy URL. Matching on host:port is what
// bridges them, and it was the trap that would have made a string compare
// useless (live capture, 2026-09-24).
func TestTheProxyHostBridgesTheTwoEncodings(t *testing.T) {
	s := carryServer(t)
	withMemberStates(t, map[string]nowPlayingSnapshot{
		"192.0.2.2": memberState("UPNP", "PLAY_STATE", "http://192.0.2.1:8888/stream/raw?u=abc"),
	})
	if ok, why := s.everyMemberCarriesTheGroup(context.Background(),
		[]boxapi.ZoneMember{{IP: "192.0.2.2"}}, groupLoc); !ok {
		t.Fatalf("a member on the master's proxy host was not recognised: %s", why)
	}
}

// A member with no address cannot be asked, so it is doubt, and doubt means
// push. It used to be skipped, which let one verified member vouch for a second
// nobody had checked: exactly the silent speaker this function exists to catch.
func TestAMemberWithNoAddressCountsAsDoubt(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), boxHost: "192.0.2.1"}
	members := []boxapi.ZoneMember{
		{DeviceID: "MASTER", IP: "192.0.2.1"},
		{DeviceID: "NOADDR", IP: "   "},
	}
	served, why := s.everyMemberCarriesTheGroup(context.Background(), members, "http://192.0.2.1:8888/stream/1")
	if served {
		t.Fatalf("a member nobody could ask was reported as served: %s", why)
	}
	if !strings.Contains(why, "NOADDR") {
		t.Errorf("the reason does not name the member: %q", why)
	}
}
