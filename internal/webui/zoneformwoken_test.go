package webui

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

func TestIsSelfResume(t *testing.T) {
	playing := nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", PlayStatus: "PLAY_STATE"}
	cases := []struct {
		name        string
		woken, user bool
		np          nowPlayingSnapshot
		want        bool
	}{
		{"woken for the group and playing", true, false, playing, true},
		{"woken and buffering its last station", true, false, nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", PlayStatus: "BUFFERING_STATE"}, true},
		{"not woken by STR: the user's music", false, false, playing, false},
		{"user started something after the wake", true, true, playing, false},
		{"woken and idle", true, false, nowPlayingSnapshot{Source: "INVALID_SOURCE", PlayStatus: "STOP_STATE"}, false},
		{"woken but back in standby", true, false, nowPlayingSnapshot{Source: "STANDBY"}, false},
		{"unreadable", true, false, nowPlayingSnapshot{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSelfResume(tc.woken, tc.user, tc.np); got != tc.want {
				t.Fatalf("isSelfResume = %v, want %v", got, tc.want)
			}
		})
	}
}

// A member the app just woke for the group is playing its own power-on resume;
// the master must not take that over (the #954 takeover is for a member the
// user was actually listening to).
func TestMembersNotWokenForGroup(t *testing.T) {
	members := []boxapi.ZoneMember{{DeviceID: "a", IP: "192.0.2.2"}, {DeviceID: "b", IP: "192.0.2.3"}}
	got := membersNotWokenForGroup(members, wokenMemberSet([]string{" 192.0.2.2 ", ""}))
	if len(got) != 1 || got[0].IP != "192.0.2.3" {
		t.Fatalf("got %+v, want only the member that was already awake", got)
	}
	if got := membersNotWokenForGroup(members, wokenMemberSet(nil)); len(got) != 2 {
		t.Fatalf("an old app that sends no list must keep every member, got %+v", got)
	}
}

func selfResumeTestServer(t *testing.T, np nowPlayingSnapshot) (*Server, *int) {
	t.Helper()
	prevRead, prevStop := groupWakeNowPlaying, groupWakeStop
	t.Cleanup(func() { groupWakeNowPlaying, groupWakeStop = prevRead, prevStop })
	groupWakeNowPlaying = func(context.Context, string) nowPlayingSnapshot { return np }
	stops := 0
	groupWakeStop = func(context.Context, string) error { stops++; return nil }
	return &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), boxHost: "192.0.2.10"}, &stops
}

// Fleet run 2026-10-04: two idle SoundTouch 10s grouped, and the master started
// its last station. Whatever it plays inside the group-wake window is stopped.
func TestFormingFromIdleStopsTheMastersOwnResume(t *testing.T) {
	s, stops := selfResumeTestServer(t, nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", PlayStatus: "PLAY_STATE"})
	s.noteQuietWakeEpisode()
	if !s.stopGroupWakeSelfResume(context.Background(), "test") || *stops != 1 {
		t.Fatalf("stopped=%d, want one STOP for the master's own resume", *stops)
	}
}

func TestMasterTheUserWasListeningToKeepsPlaying(t *testing.T) {
	playing := nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", PlayStatus: "PLAY_STATE"}

	// No group wake: the master was awake and playing before the group.
	s, stops := selfResumeTestServer(t, playing)
	if s.stopGroupWakeSelfResume(context.Background(), "test") || *stops != 0 {
		t.Fatal("stopped a master STR never woke")
	}

	// Woken for the group, then the user started a station through STR.
	s, stops = selfResumeTestServer(t, playing)
	s.noteQuietWakeEpisode()
	time.Sleep(5 * time.Millisecond)
	s.setLastPlay("http://127.0.0.1:8888/stream/1", "Station", "", "")
	if s.stopGroupWakeSelfResume(context.Background(), "test") || *stops != 0 {
		t.Fatal("stopped music the user started after the wake")
	}
}

// The form path has to ask before it captures anything to restart, once more
// after its own wake, and once after forming; and it hands memberResumeForZone
// only the members that were not just woken.
func TestZoneFormNeverCarriesAGroupWakeResume(t *testing.T) {
	src, err := os.ReadFile("zones_stereo.go")
	if err != nil {
		t.Fatal(err)
	}
	body := funcBody(t, string(src), "handleZoneForm")
	if body == "" {
		t.Fatal("handleZoneForm not found")
	}
	before := strings.Index(body, `s.stopGroupWakeSelfResume(ctx, "before forming")`)
	capture := strings.Index(body, "masterResumeForZone(np, masterRef)")
	if before < 0 || capture < 0 || before > capture {
		t.Fatal("the master's own resume must be stopped before the resume capture can take it for the user's music")
	}
	for _, want := range []string{
		`s.stopGroupWakeSelfResume(ctx, "after the wake")`,
		`s.stopGroupWakeSelfResume(lctx, "after forming")`,
		"membersNotWokenForGroup(slaves, wokenMemberSet(req.WokenFromStandby))",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("handleZoneForm lost %q", want)
		}
	}
}
