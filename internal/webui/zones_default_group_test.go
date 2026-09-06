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

func TestClassifyMemberForRejoin(t *testing.T) {
	const master = "MASTER01"
	for _, tc := range []struct {
		name, src, status, zoneMaster string
		want                          rejoinAction
	}{
		{"unreachable", "", "", "", rejoinSkipUnreachable},
		{"standby wakes", "STANDBY", "", "", rejoinWake},
		{"idle joins", "INVALID_SOURCE", "", "", rejoinJoin},
		{"stopped source joins", "LOCAL_INTERNET_RADIO", "STOP_STATE", "", rejoinJoin},
		{"paused source joins", "UPNP", "PAUSE_STATE", "", rejoinJoin},
		{"solo player stays out", "SPOTIFY", "PLAY_STATE", "", rejoinSkipSolo},
		{"buffering solo stays out", "LOCAL_INTERNET_RADIO", "BUFFERING_STATE", "", rejoinSkipSolo},
		{"already ours rejoins even while playing", "UPNP", "PLAY_STATE", master, rejoinJoin},
		{"other group stays out", "UPNP", "PLAY_STATE", "OTHER99", rejoinSkipOtherZone},
		{"other group even idle stays out", "INVALID_SOURCE", "", "OTHER99", rejoinSkipOtherZone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyMemberForRejoin(tc.src, tc.status, tc.zoneMaster, master); got != tc.want {
				t.Errorf("classify(%q,%q,%q) = %v, want %v", tc.src, tc.status, tc.zoneMaster, got, tc.want)
			}
		})
	}
}

// The play-triggered re-form must wake the standby member, keep the solo
// player out, and assert the zone with exactly the qualifying members. All
// firmware/member traffic goes through the seams.
func TestFormDefaultGroupOnPlay(t *testing.T) {
	oldNP, oldZM, oldWake, oldLive, oldSet := rejoinReadNowPlaying, rejoinReadZoneMaster, rejoinWakeMember, rejoinLiveZone, rejoinSetZone
	defer func() {
		rejoinReadNowPlaying, rejoinReadZoneMaster, rejoinWakeMember, rejoinLiveZone, rejoinSetZone = oldNP, oldZM, oldWake, oldLive, oldSet
	}()

	var mu sync.Mutex
	woken := map[string]bool{}
	var setMaster boxapi.ZoneMember
	var setSlaves []boxapi.ZoneMember
	setCalls := 0

	rejoinReadNowPlaying = func(_ context.Context, ip string) nowPlayingSnapshot {
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
	rejoinReadZoneMaster = func(context.Context, string) string { return "" }
	rejoinWakeMember = func(_ context.Context, ip string, _ *slog.Logger) {
		mu.Lock()
		woken[ip] = true
		mu.Unlock()
	}
	rejoinLiveZone = func(context.Context, string) (boxapi.Zone, error) {
		return boxapi.Zone{}, nil // no live zone yet
	}
	rejoinSetZone = func(_ context.Context, _ string, master boxapi.ZoneMember, slaves []boxapi.ZoneMember) error {
		mu.Lock()
		setCalls++
		setMaster, setSlaves = master, slaves
		mu.Unlock()
		return nil
	}

	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), boxHost: "10.0.0.1"}
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
	if !ids["SLEEPY"] || !ids["IDLE"] || ids["SOLO"] {
		t.Errorf("joined = %v, want SLEEPY+IDLE without SOLO", ids)
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
	oldNP, oldZM, oldWake, oldLive, oldSet := rejoinReadNowPlaying, rejoinReadZoneMaster, rejoinWakeMember, rejoinLiveZone, rejoinSetZone
	defer func() {
		rejoinReadNowPlaying, rejoinReadZoneMaster, rejoinWakeMember, rejoinLiveZone, rejoinSetZone = oldNP, oldZM, oldWake, oldLive, oldSet
	}()
	rejoinReadNowPlaying = func(context.Context, string) nowPlayingSnapshot {
		return nowPlayingSnapshot{Source: "UPNP", PlayStatus: "PLAY_STATE"}
	}
	rejoinReadZoneMaster = func(context.Context, string) string { return "MASTER01" }
	rejoinWakeMember = func(context.Context, string, *slog.Logger) {}
	rejoinLiveZone = func(context.Context, string) (boxapi.Zone, error) {
		return boxapi.Zone{Master: "MASTER01", Members: []boxapi.ZoneMember{{DeviceID: "S1"}}}, nil
	}
	called := false
	rejoinSetZone = func(context.Context, string, boxapi.ZoneMember, []boxapi.ZoneMember) error {
		called = true
		return nil
	}
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), boxHost: "10.0.0.1"}
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
	oldNP, oldZM, oldWake, oldLive, oldSet := rejoinReadNowPlaying, rejoinReadZoneMaster, rejoinWakeMember, rejoinLiveZone, rejoinSetZone
	defer func() {
		rejoinReadNowPlaying, rejoinReadZoneMaster, rejoinWakeMember, rejoinLiveZone, rejoinSetZone = oldNP, oldZM, oldWake, oldLive, oldSet
	}()
	var mu sync.Mutex
	woken := map[string]bool{}
	setCalls := 0
	rejoinReadNowPlaying = func(_ context.Context, ip string) nowPlayingSnapshot {
		switch ip {
		case "10.0.0.1":
			return nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", PlayStatus: "STOP_STATE"}
		default:
			return nowPlayingSnapshot{Source: "STANDBY"}
		}
	}
	rejoinReadZoneMaster = func(context.Context, string) string { return "" }
	rejoinWakeMember = func(_ context.Context, ip string, _ *slog.Logger) {
		mu.Lock()
		woken[ip] = true
		mu.Unlock()
	}
	rejoinLiveZone = func(context.Context, string) (boxapi.Zone, error) { return boxapi.Zone{}, nil }
	rejoinSetZone = func(context.Context, string, boxapi.ZoneMember, []boxapi.ZoneMember) error {
		mu.Lock()
		setCalls++
		mu.Unlock()
		return nil
	}
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), boxHost: "10.0.0.1"}
	z := zones.Zone{Master: "MASTER01", MasterIP: "10.0.0.1", Permanent: true,
		Slaves: []zones.Member{{DeviceID: "SLEEPY", IP: "10.0.0.2"}}}
	s.formDefaultGroupOnPlay(z)
	if len(woken) != 0 || setCalls != 0 {
		t.Fatalf("master in STOP_STATE still acted: woken=%v setCalls=%d", woken, setCalls)
	}
}

// The periodic rejoin: a permanent group whose master plays takes a member
// back that rebooted (fresh agent uptime, standby) or sits idle, leaves a
// member alone that somebody switched off (standby, long uptime), and does
// nothing at all while the master is silent (2026-09-06).
func TestRejoinMissingMembersTakesRebootedMembersBackOnly(t *testing.T) {
	oldNP, oldZM, oldWake, oldLive, oldSet, oldUp := rejoinReadNowPlaying, rejoinReadZoneMaster, rejoinWakeMember, rejoinLiveZone, rejoinSetZone, rejoinReadAgentUptime
	defer func() {
		rejoinReadNowPlaying, rejoinReadZoneMaster, rejoinWakeMember, rejoinLiveZone, rejoinSetZone, rejoinReadAgentUptime = oldNP, oldZM, oldWake, oldLive, oldSet, oldUp
	}()
	var mu sync.Mutex
	woken := map[string]bool{}
	var setSlaves []boxapi.ZoneMember
	setCalls := 0
	masterStatus := "PLAY_STATE"
	rejoinReadNowPlaying = func(_ context.Context, ip string) nowPlayingSnapshot {
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
	rejoinReadAgentUptime = func(_ context.Context, ip string) int {
		if ip == "10.0.0.2" {
			return 120 // just rebooted
		}
		return 5000 // switched off an hour ago
	}
	rejoinReadZoneMaster = func(context.Context, string) string { return "" }
	rejoinWakeMember = func(_ context.Context, ip string, _ *slog.Logger) { mu.Lock(); woken[ip] = true; mu.Unlock() }
	rejoinLiveZone = func(context.Context, string) (boxapi.Zone, error) {
		return boxapi.Zone{Master: "MASTER01", Members: []boxapi.ZoneMember{{DeviceID: "KEPT", IP: "10.0.0.5"}}}, nil
	}
	rejoinSetZone = func(_ context.Context, _ string, _ boxapi.ZoneMember, slaves []boxapi.ZoneMember) error {
		mu.Lock()
		setCalls++
		setSlaves = slaves
		mu.Unlock()
		return nil
	}
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), boxHost: "10.0.0.1"}
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
