package webui

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zones"
)

// The hold check runs right away in tests; on a speaker it waits a few seconds.
func init() { defaultGroupHoldCheckAfter = 0 }

// holdFixture drives formDefaultGroupOnPlay against a fake firmware whose zone
// falls apart after the first setZone (liveAfter decides what the hold check
// sees) and reports how often the firmware was told to form it.
func holdFixture(t *testing.T, masterPlaying bool, liveAfter func(calls int) boxapi.Zone) int {
	t.Helper()
	oldNP, oldZM, oldWake, oldLive, oldSet := rejoinReadNowPlaying, rejoinReadZoneMaster, rejoinWakeMember, rejoinLiveZone, rejoinSetZone
	t.Cleanup(func() {
		rejoinReadNowPlaying, rejoinReadZoneMaster, rejoinWakeMember, rejoinLiveZone, rejoinSetZone = oldNP, oldZM, oldWake, oldLive, oldSet
	})
	var mu sync.Mutex
	calls := 0
	formed := false
	rejoinReadNowPlaying = func(_ context.Context, ip string) nowPlayingSnapshot {
		if ip == "10.0.0.1" {
			mu.Lock()
			defer mu.Unlock()
			// The master plays when the re-form starts; whether it still plays at
			// the hold check is the case under test.
			if !formed || masterPlaying {
				return nowPlayingSnapshot{Source: "UPNP", PlayStatus: "PLAY_STATE"}
			}
			return nowPlayingSnapshot{Source: "STANDBY"}
		}
		return nowPlayingSnapshot{Source: "STANDBY"}
	}
	rejoinReadZoneMaster = func(context.Context, string) string { return "" }
	rejoinWakeMember = func(context.Context, string, *slog.Logger) {}
	rejoinLiveZone = func(context.Context, string) (boxapi.Zone, error) {
		mu.Lock()
		defer mu.Unlock()
		if calls == 0 {
			return boxapi.Zone{}, nil
		}
		return liveAfter(calls), nil
	}
	rejoinSetZone = func(context.Context, string, boxapi.ZoneMember, []boxapi.ZoneMember) error {
		mu.Lock()
		calls++
		formed = true
		mu.Unlock()
		return nil
	}
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), boxHost: "10.0.0.1"}
	s.formDefaultGroupOnPlay(zones.Zone{Master: "MASTER01", MasterIP: "10.0.0.1", Permanent: true,
		Slaves: []zones.Member{{DeviceID: "KITCHEN", IP: "10.0.0.2"}}})
	mu.Lock()
	defer mu.Unlock()
	return calls
}

var heldZone = boxapi.Zone{Master: "MASTER01", Members: []boxapi.ZoneMember{{DeviceID: "KITCHEN"}}}

// A woken member that falls out right after joining (its own power-on resume
// failed late, Küche 2026-10-17) is taken back once.
func TestDefaultGroupFormedOnceMoreWhenItFallsApart(t *testing.T) {
	calls := holdFixture(t, true, func(int) boxapi.Zone { return boxapi.Zone{} })
	if calls != 2 {
		t.Fatalf("setZone calls = %d, want 2 (form, then once more)", calls)
	}
}

// A zone that held is left alone.
func TestDefaultGroupHeldIsLeftAlone(t *testing.T) {
	if calls := holdFixture(t, true, func(int) boxapi.Zone { return heldZone }); calls != 1 {
		t.Fatalf("setZone calls = %d, want 1", calls)
	}
}

// When the master stopped playing in the meantime, the user has moved on.
func TestDefaultGroupHoldSkipsWhenMasterStopped(t *testing.T) {
	if calls := holdFixture(t, false, func(int) boxapi.Zone { return boxapi.Zone{} }); calls != 1 {
		t.Fatalf("setZone calls = %d, want 1", calls)
	}
}

func TestZoneCarries(t *testing.T) {
	j := []boxapi.ZoneMember{{DeviceID: "A"}, {DeviceID: "B"}}
	if !zoneCarries(boxapi.Zone{Master: "M", Members: []boxapi.ZoneMember{{DeviceID: "A"}, {DeviceID: "B"}, {DeviceID: "C"}}}, "M", j) {
		t.Error("a zone with every joiner must count as carried")
	}
	if zoneCarries(boxapi.Zone{Master: "M", Members: []boxapi.ZoneMember{{DeviceID: "A"}}}, "M", j) {
		t.Error("a missing joiner must not count as carried")
	}
	if zoneCarries(boxapi.Zone{Master: "X", Members: []boxapi.ZoneMember{{DeviceID: "A"}, {DeviceID: "B"}}}, "M", j) {
		t.Error("another master must not count as carried")
	}
}
