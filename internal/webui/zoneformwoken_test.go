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
	before := strings.Index(body, `s.masterPlaysUsersMusic(ctx, "before forming"`)
	capture := strings.Index(body, "masterResumeForZone(np, masterRef)")
	if before < 0 || capture < 0 || before > capture {
		t.Fatal("the master's own resume must be stopped before the resume capture can take it for the user's music")
	}
	for _, want := range []string{
		`s.stopGroupWakeSelfResume(ctx, "after the wake")`,
		`s.scheduleLateSelfResumeCheck("after forming")`,
		"membersNotWokenForGroup(slaves, wokenMemberSet(req.WokenFromStandby))",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("handleZoneForm lost %q", want)
		}
	}
}

// The stereo path had none of that (#1074): a bare boxPlayState read took the
// woken master's own resume for the user's music, nothing looked again after
// the wake or after pairing, and the Spotify engine was free to fill the fresh
// pair. It has to run the same three checks as the group path, in the same
// order, and hold the auto-attach across the /addGroup round trip.
func TestStereoPairNeverCarriesAGroupWakeResume(t *testing.T) {
	src, err := os.ReadFile("zones_stereo.go")
	if err != nil {
		t.Fatal(err)
	}
	body := funcBody(t, string(src), "formStereoPair")
	if body == "" {
		t.Fatal("formStereoPair not found")
	}
	before := strings.Index(body, `s.masterPlaysUsersMusic(ctx, "before pairing"`)
	capture := strings.Index(body, "masterResumeForZone(np, masterRef)")
	wake := strings.Index(body, "s.quietWake(ctx)")
	after := strings.Index(body, `s.stopGroupWakeSelfResume(ctx, "after the wake")`)
	hold := strings.Index(body, `s.holdSpotifyAutoAttach(stereoPairAutoAttachHold, "before addGroup")`)
	addGroup := strings.Index(body, "c.AddGroup(")
	switch {
	case before < 0 || capture < 0 || before > capture:
		t.Error("the master's own resume must be ruled out before the resume capture")
	case wake < 0 || before > wake:
		t.Error("the auto-attach hold and the busy test must come before the wake, which is when the engine struck in #1074")
	case after < 0 || after < wake || after > addGroup:
		t.Error("the self-resume must be looked at again after the wake and before /addGroup")
	case hold < 0 || hold > addGroup:
		t.Error("the auto-attach hold must be renewed before /addGroup")
	}
	if n := strings.Count(body, `s.scheduleLateSelfResumeCheck("after pairing")`); n != 2 {
		t.Errorf("both success paths (confirmed and unread pair) need the late self-resume look, found %d", n)
	}
	if strings.Contains(body, "if _, busy := s.boxPlayState(); busy") {
		t.Error("formStereoPair is back on the bare busy check that carried a self-resume into the pair")
	}
}

// selfResumeFormServer is a master in the given play state, with the Spotify
// auto-attach hold recorded instead of reaching an engine.
func selfResumeFormServer(t *testing.T, standby, busy bool, np nowPlayingSnapshot) (*Server, *int, *[]time.Duration) {
	t.Helper()
	s, stops := selfResumeTestServer(t, np)
	s.playStateFn = func() (bool, bool) { return standby, busy }
	var holds []time.Duration
	s.spotifySuppressActivate = func(d time.Duration) { holds = append(holds, d) }
	return s, stops, &holds
}

// #1074: two idle speakers paired, the pairing woke the master, and it came up
// playing (its own resume, or the Spotify engine pulling it onto the album the
// user had played before standby). That is not the user's music: it is
// stopped, the pair has nothing to carry, and the auto-attach is held off.
func TestIdlePairStaysSilentWhenTheMasterSelfResumesAfterTheWake(t *testing.T) {
	s, stops, holds := selfResumeFormServer(t, false, true, nowPlayingSnapshot{Source: "UPNP", PlayStatus: "PLAY_STATE", Location: "http://127.0.0.1:8888/spotify.ogg"})
	s.noteQuietWakeEpisode()

	if s.masterPlaysUsersMusic(context.Background(), "before pairing", stereoPairAutoAttachHold) {
		t.Fatal("a master playing its own resume after STR woke it was taken for the user's music")
	}
	if *stops != 1 {
		t.Fatalf("stops = %d, want one STOP for the self-resume", *stops)
	}
	if len(*holds) != 1 || (*holds)[0] != stereoPairAutoAttachHold {
		t.Fatalf("holds = %v, want the Spotify auto-attach held for %s", *holds, stereoPairAutoAttachHold)
	}
}

// A master asleep at form time has nothing to carry either, and that is
// exactly the moment the engine struck in #1074, so the hold is armed before
// the wake. Nothing is stopped: there is nothing playing yet.
func TestStandbyMasterHoldsTheAutoAttachBeforeTheWake(t *testing.T) {
	s, stops, holds := selfResumeFormServer(t, true, false, nowPlayingSnapshot{Source: "STANDBY"})

	if s.masterPlaysUsersMusic(context.Background(), "before pairing", stereoPairAutoAttachHold) {
		t.Fatal("a sleeping master reported as playing the user's music")
	}
	if *stops != 0 {
		t.Fatalf("stops = %d, a sleeping master has nothing to stop", *stops)
	}
	if len(*holds) != 1 {
		t.Fatalf("holds = %v, want the auto-attach held across the pairing", *holds)
	}
}

// The other half of the bargain: a pair (or group) formed while the master is
// playing music the user started must still carry it. No STOP, no hold on
// the Spotify engine, and the caller goes on to capture the stream.
func TestGenuinelyPlayingMasterStillCarriesItsMusic(t *testing.T) {
	playing := nowPlayingSnapshot{Source: "UPNP", PlayStatus: "PLAY_STATE", Location: "http://127.0.0.1:8888/spotify.ogg"}

	// Never woken by STR: the user was listening before the pairing.
	s, stops, holds := selfResumeFormServer(t, false, true, playing)
	if !s.masterPlaysUsersMusic(context.Background(), "before pairing", stereoPairAutoAttachHold) {
		t.Fatal("the user's own music was not recognized as such")
	}
	if *stops != 0 || len(*holds) != 0 {
		t.Fatalf("stops=%d holds=%v, the user's music must be left alone", *stops, *holds)
	}

	// Woken for a group, then the user started something through STR.
	s, stops, holds = selfResumeFormServer(t, false, true, playing)
	s.noteQuietWakeEpisode()
	time.Sleep(5 * time.Millisecond)
	s.setLastPlay("http://127.0.0.1:8888/stream/1", "Station", "", "")
	if !s.masterPlaysUsersMusic(context.Background(), "before pairing", stereoPairAutoAttachHold) {
		t.Fatal("music the user started after the wake was taken for a self-resume")
	}
	if *stops != 0 || len(*holds) != 0 {
		t.Fatalf("stops=%d holds=%v, the user's music must be left alone", *stops, *holds)
	}

	// And the capture that follows still carries that stream to the pair.
	resume, blocked, _ := masterResumeForZone(playing, nil)
	if resume == nil || blocked || resume.boxURL != playing.Location {
		t.Fatalf("resume = %+v blocked=%v, want the master's stream carried over", resume, blocked)
	}
}

// The late look after a silent form holds the auto-attach across its own wait,
// and a server without Spotify must not trip over the missing hook.
func TestLateSelfResumeCheckHoldsTheAutoAttach(t *testing.T) {
	s, _, holds := selfResumeFormServer(t, false, false, nowPlayingSnapshot{})
	s.scheduleLateSelfResumeCheck("after pairing")
	if len(*holds) != 1 || (*holds)[0] != lateSelfResumeHold {
		t.Fatalf("holds = %v, want %s", *holds, lateSelfResumeHold)
	}
	s.spotifySuppressActivate = nil
	s.holdSpotifyAutoAttach(time.Second, "no spotify")
}
