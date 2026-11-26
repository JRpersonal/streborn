package webui

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zones"
)

// newRejoinServer is a quiet master at 10.0.0.1 whose hold check runs right
// away (on a speaker it waits a few seconds). Every firmware and member call
// the rejoin paths make goes through the rejoin*Fn seams, which each test
// installs on this server alone, so nothing is shared or restored.
func newRejoinServer() *Server {
	fast := productionZoneTiming
	fast.defaultGroupHoldCheckAfter = 0
	return &Server{
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		boxHost:    "10.0.0.1",
		zoneTiming: &fast,
	}
}

func TestClassifyMemberForRejoin(t *testing.T) {
	t.Parallel()
	const master = "MASTER01"
	for _, tc := range []struct {
		name, src, status, zoneMaster string
		playKick                      bool
		want                          rejoinAction
	}{
		{"unreachable", "", "", "", false, rejoinSkipUnreachable},
		{"standby wakes", "STANDBY", "", "", false, rejoinWake},
		{"idle joins", "INVALID_SOURCE", "", "", false, rejoinJoin},
		{"stopped source joins", "LOCAL_INTERNET_RADIO", "STOP_STATE", "", false, rejoinJoin},
		{"paused source joins", "UPNP", "PAUSE_STATE", "", false, rejoinJoin},
		{"solo player stays out", "SPOTIFY", "PLAY_STATE", "", false, rejoinSkipSolo},
		{"buffering solo stays out", "LOCAL_INTERNET_RADIO", "BUFFERING_STATE", "", false, rejoinSkipSolo},
		{"already ours rejoins even while playing", "UPNP", "PLAY_STATE", master, false, rejoinJoin},
		{"other group stays out", "UPNP", "PLAY_STATE", "OTHER99", false, rejoinSkipOtherZone},
		{"other group even idle stays out", "INVALID_SOURCE", "", "OTHER99", false, rejoinSkipOtherZone},
		{"solo player joins on the master's play kick", "SPOTIFY", "PLAY_STATE", "", true, rejoinJoin},
		{"buffering solo joins on the play kick", "LOCAL_INTERNET_RADIO", "BUFFERING_STATE", "", true, rejoinJoin},
		{"other group stays out on the play kick", "UPNP", "PLAY_STATE", "OTHER99", true, rejoinSkipOtherZone},
		{"standby still wakes on the play kick", "STANDBY", "", "", true, rejoinWake},
		// The one-remote rule (2026-11-26): a member that started its own
		// station from the same infrared press as the main speaker is taken
		// along, as is one the stray signals switched to Bluetooth.
		{"member started by the same remote press joins on the play kick", "LOCAL_INTERNET_RADIO", "BUFFERING_STATE", "", true, rejoinJoin},
		{"member on Bluetooth joins on the play kick", "BLUETOOTH", "PLAY_STATE", "", true, rejoinJoin},
		{"member on Bluetooth stays out without a play kick", "BLUETOOTH", "PLAY_STATE", "", false, rejoinSkipSolo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyMemberForRejoin(tc.src, tc.status, tc.zoneMaster, master, tc.playKick); got != tc.want {
				t.Errorf("classify(%q,%q,%q,%v) = %v, want %v", tc.src, tc.status, tc.zoneMaster, tc.playKick, got, tc.want)
			}
		})
	}
}

// The play-triggered re-form must wake the standby member, take the solo
// player along (#1576), and assert the zone with exactly the qualifying members. All
// firmware/member traffic goes through the seams.
func TestFormDefaultGroupOnPlay(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	woken := map[string]bool{}
	var setMaster boxapi.ZoneMember
	var setSlaves []boxapi.ZoneMember
	setCalls := 0

	s := newRejoinServer()
	s.rejoinReadNowPlayingFn = func(_ context.Context, ip string) nowPlayingSnapshot {
		switch ip {
		case "10.0.0.2":
			return nowPlayingSnapshot{Source: "STANDBY"}
		case "10.0.0.3":
			return nowPlayingSnapshot{Source: "SPOTIFY", PlayStatus: "PLAY_STATE"}
		case "10.0.0.1": // the master itself: playing, so the re-form may run
			return nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", PlayStatus: "PLAY_STATE"}
		default:
			return nowPlayingSnapshot{Source: "INVALID_SOURCE"}
		}
	}
	s.rejoinReadZoneMasterFn = func(context.Context, string) string { return "" }
	s.rejoinWakeMemberFn = func(_ context.Context, ip string, _ *slog.Logger) {
		mu.Lock()
		woken[ip] = true
		mu.Unlock()
	}
	s.rejoinLiveZoneFn = func(context.Context, string) (boxapi.Zone, error) {
		mu.Lock()
		defer mu.Unlock()
		if setCalls == 0 {
			return boxapi.Zone{}, nil // no live zone yet
		}
		return boxapi.Zone{Master: setMaster.DeviceID, Members: setSlaves}, nil // the zone held
	}
	s.rejoinSetZoneFn = func(_ context.Context, _ string, master boxapi.ZoneMember, slaves []boxapi.ZoneMember) error {
		mu.Lock()
		setCalls++
		setMaster, setSlaves = master, slaves
		mu.Unlock()
		return nil
	}

	z := zones.Zone{
		Master:    "MASTER01",
		MasterIP:  "10.0.0.1",
		Permanent: true,
		Slaves: []zones.Member{
			{DeviceID: "SLEEPY", IP: "10.0.0.2"},
			{DeviceID: "SOLO", IP: "10.0.0.3"},
			{DeviceID: "IDLE", IP: "10.0.0.4"},
		},
	}

	// Opt-in: without Permanent the play trigger must do NOTHING, wake
	// included (Jens, 2026-08-26).
	optOut := z
	optOut.Permanent = false
	s.formDefaultGroupOnPlay(optOut)
	if len(woken) != 0 || setCalls != 0 {
		t.Fatalf("non-permanent group acted on play: woken=%v setCalls=%d", woken, setCalls)
	}

	s.formDefaultGroupOnPlay(z)

	if !woken["10.0.0.2"] {
		t.Error("standby member was not woken")
	}
	if woken["10.0.0.3"] || woken["10.0.0.4"] {
		t.Error("a non-standby member was woken")
	}
	if setCalls != 1 {
		t.Fatalf("setZone calls = %d, want 1", setCalls)
	}
	if setMaster.DeviceID != "MASTER01" {
		t.Errorf("master = %q", setMaster.DeviceID)
	}
	ids := map[string]bool{}
	for _, m := range setSlaves {
		ids[m.DeviceID] = true
	}
	// #1576: the master was started on purpose, so the member playing on its
	// own comes along too.
	if !ids["SLEEPY"] || !ids["IDLE"] || !ids["SOLO"] {
		t.Errorf("joined = %v, want SLEEPY+IDLE+SOLO", ids)
	}

	// Cooldown: a second kick right after must not drive the firmware again.
	s.formDefaultGroupOnPlay(z)
	if setCalls != 1 {
		t.Errorf("cooldown ignored: setZone calls = %d", setCalls)
	}
}

// A live zone that already carries every qualifying member leaves the
// firmware alone.
func TestFormDefaultGroupSkipsWhenComplete(t *testing.T) {
	t.Parallel()
	s := newRejoinServer()
	s.rejoinReadNowPlayingFn = func(context.Context, string) nowPlayingSnapshot {
		return nowPlayingSnapshot{Source: "UPNP", PlayStatus: "PLAY_STATE"}
	}
	s.rejoinReadZoneMasterFn = func(context.Context, string) string { return "MASTER01" }
	s.rejoinWakeMemberFn = func(context.Context, string, *slog.Logger) {}
	s.rejoinLiveZoneFn = func(context.Context, string) (boxapi.Zone, error) {
		return boxapi.Zone{Master: "MASTER01", Members: []boxapi.ZoneMember{{DeviceID: "S1"}}}, nil
	}
	called := false
	s.rejoinSetZoneFn = func(context.Context, string, boxapi.ZoneMember, []boxapi.ZoneMember) error {
		called = true
		return nil
	}
	s.lastDefaultFormAt = time.Time{}
	z := zones.Zone{Master: "MASTER01", MasterIP: "10.0.0.1", Permanent: true,
		Slaves: []zones.Member{{DeviceID: "S1", IP: "10.0.0.2"}}}
	s.formDefaultGroupOnPlay(z)
	if called {
		t.Error("setZone driven although the live zone already matches")
	}
}

// The master that is NOT playing must not wake anyone: a rebooted speaker
// flips STANDBY -> LOCAL_INTERNET_RADIO in STOP_STATE while it re-registers
// its presets, and that flip woke every member of a permanent group at 03:28
// after a fleet update (2026-09-06). The play kick may still arrive; the
// re-form has to check the master's own play state before acting.
func TestFormDefaultGroupOnPlayLeavesMembersAloneWhenMasterIsNotPlaying(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	woken := map[string]bool{}
	setCalls := 0
	s := newRejoinServer()
	s.rejoinReadNowPlayingFn = func(_ context.Context, ip string) nowPlayingSnapshot {
		switch ip {
		case "10.0.0.1":
			return nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", PlayStatus: "STOP_STATE"}
		default:
			return nowPlayingSnapshot{Source: "STANDBY"}
		}
	}
	s.rejoinReadZoneMasterFn = func(context.Context, string) string { return "" }
	s.rejoinWakeMemberFn = func(_ context.Context, ip string, _ *slog.Logger) {
		mu.Lock()
		woken[ip] = true
		mu.Unlock()
	}
	s.rejoinLiveZoneFn = func(context.Context, string) (boxapi.Zone, error) { return boxapi.Zone{}, nil }
	s.rejoinSetZoneFn = func(context.Context, string, boxapi.ZoneMember, []boxapi.ZoneMember) error {
		mu.Lock()
		setCalls++
		mu.Unlock()
		return nil
	}
	z := zones.Zone{Master: "MASTER01", MasterIP: "10.0.0.1", Permanent: true,
		Slaves: []zones.Member{{DeviceID: "SLEEPY", IP: "10.0.0.2"}}}
	s.formDefaultGroupOnPlay(z)
	if len(woken) != 0 || setCalls != 0 {
		t.Fatalf("master in STOP_STATE still acted: woken=%v setCalls=%d", woken, setCalls)
	}
}

// A start that first fails (the master briefly INVALID_SOURCE) and then plays a
// few seconds later must still form the group: the failed kick may not burn the
// cooldown the real kick needs (#1353). A third kick right after the formed one
// stays inside the cooldown.
func TestFormDefaultGroupOnPlayAfterFailedStart(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	masterReads := 0
	setCalls := 0
	s := newRejoinServer()
	s.rejoinReadNowPlayingFn = func(_ context.Context, ip string) nowPlayingSnapshot {
		if ip != "10.0.0.1" {
			return nowPlayingSnapshot{Source: "INVALID_SOURCE"}
		}
		mu.Lock()
		defer mu.Unlock()
		masterReads++
		if masterReads == 1 {
			return nowPlayingSnapshot{Source: "INVALID_SOURCE"} // the failed first start
		}
		return nowPlayingSnapshot{Source: "UPNP", PlayStatus: "PLAY_STATE"}
	}
	s.rejoinReadZoneMasterFn = func(context.Context, string) string { return "" }
	s.rejoinWakeMemberFn = func(context.Context, string, *slog.Logger) {}
	s.rejoinLiveZoneFn = func(context.Context, string) (boxapi.Zone, error) {
		mu.Lock()
		defer mu.Unlock()
		if setCalls == 0 {
			return boxapi.Zone{}, nil
		}
		return boxapi.Zone{Master: "MASTER01", Members: []boxapi.ZoneMember{{DeviceID: "IDLE"}}}, nil
	}
	s.rejoinSetZoneFn = func(context.Context, string, boxapi.ZoneMember, []boxapi.ZoneMember) error {
		mu.Lock()
		setCalls++
		mu.Unlock()
		return nil
	}
	z := zones.Zone{Master: "MASTER01", MasterIP: "10.0.0.1", Permanent: true,
		Slaves: []zones.Member{{DeviceID: "IDLE", IP: "10.0.0.4"}}}

	s.formDefaultGroupOnPlay(z) // master not playing yet: nothing happens
	if setCalls != 0 {
		t.Fatalf("setZone driven while the master was not playing: %d", setCalls)
	}
	s.formDefaultGroupOnPlay(z) // the retry plays: the group must form now
	if setCalls != 1 {
		t.Fatalf("setZone calls after the successful retry = %d, want 1", setCalls)
	}
	s.formDefaultGroupOnPlay(z) // inside the cooldown of the real form
	if setCalls != 1 {
		t.Errorf("cooldown ignored after a real form: setZone calls = %d", setCalls)
	}
}

// The periodic rejoin: a permanent group whose master plays takes a member
// back that rebooted (fresh agent uptime, standby) or sits idle, leaves a
// member alone that somebody switched off (standby, long uptime), and does
// nothing at all while the master is silent (2026-09-06).
func TestRejoinMissingMembersTakesRebootedMembersBackOnly(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	woken := map[string]bool{}
	var setSlaves []boxapi.ZoneMember
	setCalls := 0
	masterStatus := "PLAY_STATE"
	s := newRejoinServer()
	s.rejoinReadNowPlayingFn = func(_ context.Context, ip string) nowPlayingSnapshot {
		switch ip {
		case "10.0.0.1":
			return nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", PlayStatus: masterStatus}
		case "10.0.0.2", "10.0.0.3":
			return nowPlayingSnapshot{Source: "STANDBY"}
		case "10.0.0.4":
			return nowPlayingSnapshot{Source: "INVALID_SOURCE"}
		default:
			return nowPlayingSnapshot{}
		}
	}
	s.rejoinReadAgentUptimeFn = func(_ context.Context, ip string) int {
		if ip == "10.0.0.2" {
			return 120 // just rebooted
		}
		return 5000 // switched off an hour ago
	}
	s.rejoinReadZoneMasterFn = func(context.Context, string) string { return "" }
	s.rejoinWakeMemberFn = func(_ context.Context, ip string, _ *slog.Logger) { mu.Lock(); woken[ip] = true; mu.Unlock() }
	s.rejoinLiveZoneFn = func(context.Context, string) (boxapi.Zone, error) {
		return boxapi.Zone{Master: "MASTER01", Members: []boxapi.ZoneMember{{DeviceID: "KEPT", IP: "10.0.0.5"}}}, nil
	}
	s.rejoinSetZoneFn = func(_ context.Context, _ string, _ boxapi.ZoneMember, slaves []boxapi.ZoneMember) error {
		mu.Lock()
		setCalls++
		setSlaves = slaves
		mu.Unlock()
		return nil
	}
	z := zones.Zone{Master: "MASTER01", MasterIP: "10.0.0.1", Permanent: true, Slaves: []zones.Member{
		{DeviceID: "KEPT", IP: "10.0.0.5"},
		{DeviceID: "REBOOTED", IP: "10.0.0.2"},
		{DeviceID: "SWITCHEDOFF", IP: "10.0.0.3"},
		{DeviceID: "IDLE", IP: "10.0.0.4"},
	}}
	if !s.rejoinMissingMembers(context.Background(), z) {
		t.Fatal("nothing was taken back")
	}
	if !woken["10.0.0.2"] || woken["10.0.0.3"] {
		t.Errorf("woken = %v, want only the rebooted member", woken)
	}
	ids := map[string]bool{}
	for _, m := range setSlaves {
		ids[m.DeviceID] = true
	}
	if !ids["KEPT"] || !ids["REBOOTED"] || !ids["IDLE"] || ids["SWITCHEDOFF"] {
		t.Errorf("setZone members = %v, want KEPT+REBOOTED+IDLE without SWITCHEDOFF", ids)
	}
	// Silent master: nothing happens, nobody is woken.
	masterStatus = "STOP_STATE"
	woken = map[string]bool{}
	setCalls = 0
	if s.rejoinMissingMembers(context.Background(), z) || setCalls != 0 || len(woken) != 0 {
		t.Errorf("a silent master must not take anyone back: setCalls=%d woken=%v", setCalls, woken)
	}
	// Not permanent: never.
	masterStatus = "PLAY_STATE"
	z.Permanent = false
	if s.rejoinMissingMembers(context.Background(), z) {
		t.Error("a non-permanent group must not rejoin on the tick")
	}
}

// zoneLossFixture is a two-speaker permanent group whose lone follower just
// restarted for an update: the master's firmware ended the zone with it, the
// master is awake and idle, and the follower sits in standby with a freshly
// started agent (#1339).
type zoneLossFixture struct {
	mu           sync.Mutex
	setCalls     int
	setSlaves    []boxapi.ZoneMember
	woken        map[string]bool
	masterSource string
	memberUptime int
	liveMaster   string
}

func newZoneLossFixture(t *testing.T) (*Server, *zoneLossFixture, zones.Zone) {
	t.Helper()
	f := &zoneLossFixture{woken: map[string]bool{}, masterSource: "INVALID_SOURCE", memberUptime: 90}
	s := newRejoinServer()
	s.rejoinSelfDeviceIDFn = func(context.Context) string { return "MASTER01" }
	s.rejoinReadNowPlayingFn = func(_ context.Context, ip string) nowPlayingSnapshot {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch ip {
		case "10.0.0.1":
			return nowPlayingSnapshot{Source: f.masterSource, PlayStatus: "STOP_STATE"}
		case "10.0.0.2":
			return nowPlayingSnapshot{Source: "STANDBY"}
		default:
			return nowPlayingSnapshot{}
		}
	}
	s.rejoinReadAgentUptimeFn = func(context.Context, string) int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.memberUptime
	}
	s.rejoinReadZoneMasterFn = func(context.Context, string) string { return "" }
	s.rejoinWakeMemberFn = func(_ context.Context, ip string, _ *slog.Logger) {
		f.mu.Lock()
		f.woken[ip] = true
		f.mu.Unlock()
	}
	s.rejoinLiveZoneFn = func(context.Context, string) (boxapi.Zone, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return boxapi.Zone{Master: f.liveMaster}, nil
	}
	s.rejoinSetZoneFn = func(_ context.Context, _ string, _ boxapi.ZoneMember, slaves []boxapi.ZoneMember) error {
		f.mu.Lock()
		f.setCalls++
		f.setSlaves = slaves
		f.mu.Unlock()
		return nil
	}
	s.noteZoneDissolved()
	z := zones.Zone{Master: "MASTER01", MasterIP: "10.0.0.1", Permanent: true, Slaves: []zones.Member{
		{DeviceID: "FOLLOWER", IP: "10.0.0.2"},
	}}
	return s, f, z
}

// A two-speaker permanent group whose follower restarted for an update, master
// awake, zone gone: exactly one setZone, and a second tick adds none.
func TestReformAfterMemberRebootTakesRestartedFollowerBackOnce(t *testing.T) {
	t.Parallel()
	s, f, z := newZoneLossFixture(t)
	if !s.reformAfterMemberReboot(context.Background(), z) {
		t.Fatal("the restarted follower was not taken back")
	}
	if f.setCalls != 1 || len(f.setSlaves) != 1 || f.setSlaves[0].DeviceID != "FOLLOWER" {
		t.Fatalf("setCalls=%d slaves=%v, want exactly one setZone with FOLLOWER", f.setCalls, f.setSlaves)
	}
	if !f.woken["10.0.0.2"] {
		t.Error("the freshly restarted follower in standby was not woken for the join")
	}
	// The setZone's own teardown frame stamps a new dissolve; the next tick
	// must still leave it at that one attempt.
	s.noteZoneDissolved()
	if s.reformAfterMemberReboot(context.Background(), z) || f.setCalls != 1 {
		t.Errorf("second tick drove another setZone: setCalls=%d", f.setCalls)
	}
}

func TestReformAfterMemberRebootLeavesItAlone(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		setup func(s *Server, f *zoneLossFixture, z *zones.Zone)
	}{
		{"master in standby", func(_ *Server, f *zoneLossFixture, _ *zones.Zone) { f.masterSource = "STANDBY" }},
		{"not permanent", func(_ *Server, _ *zoneLossFixture, z *zones.Zone) { z.Permanent = false }},
		{"stereo pair", func(_ *Server, _ *zoneLossFixture, z *zones.Zone) { z.Stereo = true }},
		{"member switched off long ago", func(_ *Server, f *zoneLossFixture, _ *zones.Zone) { f.memberUptime = 5000 }},
		{"member agent not answering", func(_ *Server, f *zoneLossFixture, _ *zones.Zone) { f.memberUptime = -1 }},
		{"zone still live", func(_ *Server, f *zoneLossFixture, _ *zones.Zone) { f.liveMaster = "MASTER01" }},
		{"this speaker is a member", func(s *Server, _ *zoneLossFixture, _ *zones.Zone) {
			s.rejoinSelfDeviceIDFn = func(context.Context) string { return "FOLLOWER" }
		}},
		{"no dissolve observed", func(s *Server, _ *zoneLossFixture, _ *zones.Zone) {
			s.zoneLossMu.Lock()
			s.zoneDissolvedAt = time.Time{}
			s.zoneLossMu.Unlock()
		}},
		{"dissolve too long ago", func(s *Server, _ *zoneLossFixture, _ *zones.Zone) {
			s.zoneLossMu.Lock()
			s.zoneDissolvedAt = time.Now().Add(-zoneLossRejoinWindow - time.Minute)
			s.zoneLossMu.Unlock()
		}},
		{"group re-form hushed", func(s *Server, _ *zoneLossFixture, _ *zones.Zone) { s.hushGroupForm(time.Minute) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, f, z := newZoneLossFixture(t)
			tc.setup(s, f, &z)
			if s.reformAfterMemberReboot(context.Background(), z) || f.setCalls != 0 {
				t.Errorf("setZone driven: setCalls=%d", f.setCalls)
			}
			if f.woken["10.0.0.2"] {
				t.Error("a member was woken")
			}
		})
	}
}

// #1576: a stored member that is not in the live group is reported, matched by
// deviceID first and by address for a member stored without one.
func TestNotJoinedMembers(t *testing.T) {
	t.Parallel()
	live := []boxapi.ZoneMember{{DeviceID: "MASTER01", IP: "10.0.0.1"}, {DeviceID: "sleepy", IP: "10.0.0.2"}}
	stored := []rememberedMember{
		{DeviceID: "SLEEPY", IP: "10.0.0.9"},
		{DeviceID: "SOLO", IP: "10.0.0.3", Name: "Kitchen"},
		{IP: "10.0.0.1"},
	}
	got := notJoinedMembers(live, stored)
	if len(got) != 1 || got[0].DeviceID != "SOLO" || got[0].Name != "Kitchen" {
		t.Fatalf("notJoined = %+v, want only SOLO", got)
	}
	if notJoinedMembers(live, nil) != nil {
		t.Error("no stored members must report none")
	}
}

// Field case 2026-11-26, 11:24:18: the follower restarted for an update, its
// firmware already answered STANDBY and its agent did not answer yet
// (agentUptimeSec=-1; its listeners came up two seconds later). That is no
// verdict: nothing is formed and nobody is woken on the silence, the attempt
// for this dissolve is not used up, and the member's own start notice runs
// the round again at once, which then takes it back.
func TestReformAfterMemberRebootWaitsForAStartingAgent(t *testing.T) {
	t.Parallel()
	s, f, z := newZoneLossFixture(t)
	s.zones = zones.New()
	_ = s.zones.Set(z) // in memory; the store has no file in a test
	s.memberStartedKick = make(chan struct{}, 1)
	f.memberUptime = -1
	if s.reformAfterMemberReboot(context.Background(), z) || f.setCalls != 0 || f.woken["10.0.0.2"] {
		t.Fatalf("a member whose agent is still starting was acted on: setCalls=%d woken=%v", f.setCalls, f.woken)
	}
	s.zoneLossMu.Lock()
	tried := s.zoneLossRejoinTriedAt
	s.zoneLossMu.Unlock()
	if !tried.IsZero() {
		t.Fatal("the agent's silence used up the one attempt this dissolve gets")
	}

	// A speaker that is not a member of the group asks for nothing.
	if s.noteMemberStarted("10.0.0.77", "STRANGER") {
		t.Fatal("a speaker outside the group was taken for a member")
	}
	select {
	case <-s.memberStartedKick:
		t.Fatal("a stranger's start queued a group round")
	default:
	}

	// The member's agent is up and says so: one round is queued, and that
	// round (the tick's own) takes the member back.
	f.mu.Lock()
	f.memberUptime = 8
	f.mu.Unlock()
	if !s.noteMemberStarted("10.0.0.2", "") {
		t.Fatal("the stored member's start notice was not recognised")
	}
	select {
	case <-s.memberStartedKick:
	default:
		t.Fatal("the member's start notice queued no round")
	}
	if !s.reformAfterMemberReboot(context.Background(), z) {
		t.Fatal("the member was not taken back once its agent answered")
	}
	if f.setCalls != 1 || !f.woken["10.0.0.2"] {
		t.Fatalf("setCalls=%d woken=%v, want one setZone with the woken member", f.setCalls, f.woken)
	}
	// The same notice again moments later (a repeat, or a LAN client posting
	// in a loop) asks for no further round.
	if !s.noteMemberStarted("10.0.0.2", "") {
		t.Fatal("the repeated notice was not recognised as the member's")
	}
	select {
	case <-s.memberStartedKick:
		t.Fatal("a repeated start notice queued another round")
	default:
	}
}

// The same silence on the live-group path: a missing member in standby whose
// agent does not answer is left for its own start notice, not written off.
func TestRejoinMissingMembersLeavesAStartingAgentForItsNotice(t *testing.T) {
	t.Parallel()
	s := newRejoinServer()
	setCalls := 0
	s.rejoinReadNowPlayingFn = func(_ context.Context, ip string) nowPlayingSnapshot {
		if ip == "10.0.0.1" {
			return nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", PlayStatus: "PLAY_STATE"}
		}
		return nowPlayingSnapshot{Source: "STANDBY"}
	}
	s.rejoinReadAgentUptimeFn = func(context.Context, string) int { return -1 }
	s.rejoinReadZoneMasterFn = func(context.Context, string) string { return "" }
	s.rejoinWakeMemberFn = func(context.Context, string, *slog.Logger) { t.Error("a member whose agent does not answer was woken") }
	s.rejoinLiveZoneFn = func(context.Context, string) (boxapi.Zone, error) {
		return boxapi.Zone{Master: "MASTER01", Members: []boxapi.ZoneMember{{DeviceID: "KEPT", IP: "10.0.0.5"}}}, nil
	}
	s.rejoinSetZoneFn = func(context.Context, string, boxapi.ZoneMember, []boxapi.ZoneMember) error {
		setCalls++
		return nil
	}
	z := zones.Zone{Master: "MASTER01", MasterIP: "10.0.0.1", Permanent: true, Slaves: []zones.Member{
		{DeviceID: "KEPT", IP: "10.0.0.5"},
		{DeviceID: "STARTING", IP: "10.0.0.2"},
	}}
	if s.rejoinMissingMembers(context.Background(), z) || setCalls != 0 {
		t.Fatalf("setZone driven for a member whose agent does not answer: setCalls=%d", setCalls)
	}
}

// The start notice goes to every known speaker, restored (dimmed) entries
// included, and reports which of them named this speaker a member.
func TestAnnounceAgentStartedTellsEveryKnownSpeaker(t *testing.T) {
	t.Parallel()
	s := newRejoinServer()
	s.rejoinSelfDeviceIDFn = func(context.Context) string { return "MEMBER01" }
	s.cachedPeersFn = func() []PeerLink {
		return []PeerLink{{IP: "10.0.0.1"}, {IP: "10.0.0.3", Reachable: true}, {IP: "10.0.0.1"}, {IP: ""}}
	}
	var mu sync.Mutex
	asked := map[string]int{}
	s.memberStartedNoticeFn = func(_ context.Context, ip string, body []byte) (bool, bool) {
		mu.Lock()
		asked[ip]++
		mu.Unlock()
		if string(body) != `{"deviceID":"MEMBER01"}` {
			t.Errorf("notice body = %s", body)
		}
		return ip == "10.0.0.1", true
	}
	s.AnnounceAgentStarted()
	if asked["10.0.0.1"] != 1 || asked["10.0.0.3"] != 1 || len(asked) != 2 {
		t.Fatalf("asked = %v, want each known speaker once", asked)
	}
}

// Field case 2026-11-26, 11:36:42.7 to 11:36:49.8: a remote press started the
// main speaker, and the member that caught the same press joined seven seconds
// later, six of them the app kick's wait. The firmware's own play edge says
// the speaker plays already, so a native permanent group's round is sent after
// the short play-edge wait; a mirror group keeps the long one.
func TestPlayEdgeKickIsShortForANativePermanentGroup(t *testing.T) {
	t.Parallel()
	if productionZoneTiming.playEdgeKickDelay >= productionZoneTiming.mirrorKickDelay ||
		productionZoneTiming.playEdgeKickDelay > 2*time.Second {
		t.Fatalf("play-edge wait %s, want well under the app kick's %s",
			productionZoneTiming.playEdgeKickDelay, productionZoneTiming.mirrorKickDelay)
	}
	for _, tc := range []struct {
		name string
		zone zones.Zone
		fast bool
	}{
		{"native permanent group", zones.Zone{Master: "M", Permanent: true, Slaves: []zones.Member{{DeviceID: "A"}}}, true},
		{"mirror group", zones.Zone{Master: "M", Permanent: true, Mode: "mirror", Slaves: []zones.Member{{DeviceID: "A"}}}, false},
		{"not permanent", zones.Zone{Master: "M", Slaves: []zones.Member{{DeviceID: "A"}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			timing := productionZoneTiming
			timing.playEdgeKickDelay = 10 * time.Millisecond
			timing.mirrorKickDelay = time.Hour
			s := &Server{
				logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
				zones:      zones.New(),
				mirrorKick: make(chan struct{}, 1),
				zoneTiming: &timing,
			}
			_ = s.zones.Set(tc.zone) // in memory; the store has no file in a test
			s.KickDefaultGroup()
			select {
			case <-s.mirrorKick:
				if !tc.fast {
					t.Fatal("the round came after the short wait")
				}
			case <-time.After(500 * time.Millisecond):
				if tc.fast {
					t.Fatal("no round within the play-edge wait")
				}
			}
		})
	}
}

// The main speaker of a permanent group tells its members about a remote
// press of a key they would act on; a member's copy of the document, a
// group that is not permanent, and a key the members do nothing with send
// nothing.
func TestNotifyMembersOfRemoteKeyOnlyFromTheMainSpeaker(t *testing.T) {
	t.Parallel()
	z := zones.Zone{Master: "MASTER01", MasterIP: "10.0.0.1", Permanent: true, Slaves: []zones.Member{
		{DeviceID: "KUECHE", IP: "10.0.0.2"}, {DeviceID: "BAD", IP: "10.0.0.3"},
	}}
	for _, tc := range []struct {
		name string
		self string
		key  string
		edit func(*zones.Zone)
		want int
	}{
		{"main speaker, preset key", "MASTER01", "PRESET_2", nil, 2},
		{"main speaker, track key", "master01", "NEXT_TRACK", nil, 2},
		{"main speaker, volume key", "MASTER01", "VOLUME_UP", nil, 0},
		{"a member's copy", "KUECHE", "PRESET_2", nil, 0},
		{"not permanent", "MASTER01", "PRESET_2", func(z *zones.Zone) { z.Permanent = false }, 0},
		{"stereo pair", "MASTER01", "PRESET_2", func(z *zones.Zone) { z.Stereo = true }, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newRejoinServer()
			s.zones = zones.New()
			doc := z
			doc.Slaves = append([]zones.Member(nil), z.Slaves...)
			if tc.edit != nil {
				tc.edit(&doc)
			}
			_ = s.zones.Set(doc) // in memory; the store has no file in a test
			self := tc.self
			s.rejoinSelfDeviceIDFn = func(context.Context) string { return self }
			var mu sync.Mutex
			sent := 0
			key := tc.key
			s.groupKeyNoticeFn = func(_ context.Context, _ string, body []byte) bool {
				mu.Lock()
				sent++
				mu.Unlock()
				if want := `{"key":"` + key + `","master":"MASTER01"}`; string(body) != want {
					t.Errorf("notice body = %s, want %s", body, want)
				}
				return true
			}
			s.NotifyMembersOfRemoteKey(tc.key)
			if sent != tc.want {
				t.Fatalf("notices sent = %d, want %d", sent, tc.want)
			}
		})
	}
}

// A member compares its own press against the main speaker's notice, on its
// own clock, in either order: the sm2 analytics line of the member's press
// can trail the main speaker's notice (0.85 s in the field case), or lead it.
func TestMasterRemoteKeyNearIsOneWindowEitherWay(t *testing.T) {
	t.Parallel()
	s := newRejoinServer()
	if _, _, _, ok := s.MasterRemoteKeyNear(time.Now(), GroupPressWindow); ok {
		t.Fatal("a speaker that never got a notice reports a main-speaker press")
	}
	at := time.Now()
	s.noteMasterKey(masterKeyNote{at: at, key: "PRESET_2", master: "MASTER01", from: "10.0.0.1"})
	for _, tc := range []struct {
		name  string
		press time.Time
		want  bool
	}{
		{"member press 0.85 s after the notice", at.Add(850 * time.Millisecond), true},
		{"member press before the notice", at.Add(-1500 * time.Millisecond), true},
		{"member press minutes later", at.Add(3 * time.Minute), false},
		{"member press just outside", at.Add(GroupPressWindow + time.Millisecond), false},
	} {
		delta, key, from, ok := s.MasterRemoteKeyNear(tc.press, GroupPressWindow)
		if ok != tc.want {
			t.Errorf("%s: ok = %v (delta %s), want %v", tc.name, ok, delta, tc.want)
		}
		if ok && (key != "PRESET_2" || from != "10.0.0.1") {
			t.Errorf("%s: key=%q from=%q", tc.name, key, from)
		}
	}
}
