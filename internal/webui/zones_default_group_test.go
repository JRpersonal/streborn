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
