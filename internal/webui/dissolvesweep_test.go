package webui

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zones"
)

// withDissolveSweepSeams makes the sweep passes instant and lets a test decide
// what the master's firmware zone reads on each pass.
func withDissolveSweepSeams(t *testing.T, zoneReads []boxapi.Zone, zoneErr error) *[]time.Duration {
	t.Helper()
	prevZone, prevSleep := firmwareZoneFn, dissolveSweepSleep
	var mu sync.Mutex
	slept := &[]time.Duration{}
	call := 0
	firmwareZoneFn = func(_ context.Context, _ string) (boxapi.Zone, error) {
		mu.Lock()
		defer mu.Unlock()
		if zoneErr != nil {
			return boxapi.Zone{}, zoneErr
		}
		if call < len(zoneReads) {
			z := zoneReads[call]
			call++
			return z, nil
		}
		return boxapi.Zone{}, nil
	}
	dissolveSweepSleep = func(d time.Duration) {
		mu.Lock()
		*slept = append(*slept, d)
		mu.Unlock()
	}
	t.Cleanup(func() { firmwareZoneFn, dissolveSweepSleep = prevZone, prevSleep })
	return slept
}

func newSweepServer(t *testing.T, z zones.Zone) *Server {
	t.Helper()
	st, err := zones.Load(filepath.Join(t.TempDir(), "zones.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Set(z); err != nil {
		t.Fatal(err)
	}
	return &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), boxHost: "192.0.2.10", zones: st}
}

var sweepDoc = zones.Zone{Master: "DEV-M", MasterIP: "192.0.2.10", Slaves: []zones.Member{{DeviceID: "DEV-A", IP: "192.0.2.54"}}}

// The reported case: the firmware dropped the follower, the master moved on to
// the next track, the follower still plays the earlier track from the same
// server. After the delay, with the zone still gone, it is stopped.
func TestFirmwareDissolveSweepStopsAFollowerStillOnTheProgramme(t *testing.T) {
	const server = "http://192.0.2.17:10243/lib/"
	follower := newFakeSpeaker("STORED_MUSIC", "PLAY_STATE", server+"track-1.mp3")
	defer follower.srv.Close()
	withFakeFleet(t, map[string]*fakeSpeaker{"192.0.2.54": follower})
	withDissolveSweepSeams(t, nil, nil) // every read: no zone

	s := newSweepServer(t, sweepDoc)
	s.runStragglerSweepAfterFirmwareDissolve(zoneDocFingerprint(sweepDoc), server+"track-2.mp3", sweepDoc.Slaves)

	if got := follower.stops(); got != 1 {
		t.Errorf("stop keys sent = %d, want 1", got)
	}
}

// The firmware also announces a dissolve on its way through a /setZone rebuild.
// When the pass finds the zone live again, nothing is touched.
func TestFirmwareDissolveSweepStandsDownWhenTheZoneIsBack(t *testing.T) {
	const server = "http://192.0.2.17:10243/lib/"
	follower := newFakeSpeaker("STORED_MUSIC", "PLAY_STATE", server+"track-1.mp3")
	defer follower.srv.Close()
	withFakeFleet(t, map[string]*fakeSpeaker{"192.0.2.54": follower})
	withDissolveSweepSeams(t, []boxapi.Zone{{Master: "DEV-M", Members: []boxapi.ZoneMember{{DeviceID: "DEV-A", IP: "192.0.2.54"}}}}, nil)

	s := newSweepServer(t, sweepDoc)
	s.runStragglerSweepAfterFirmwareDissolve(zoneDocFingerprint(sweepDoc), server+"track-2.mp3", sweepDoc.Slaves)

	if got := follower.stops(); got != 0 {
		t.Errorf("a member of a live zone was stopped %d time(s)", got)
	}
}

// A follower unreachable on the first pass (the Wi-Fi drop that cost it the
// zone) is caught by a later pass once it answers again.
func TestFirmwareDissolveSweepRetriesAnUnreachableFollower(t *testing.T) {
	const server = "http://192.0.2.17:10243/lib/"
	follower := newFakeSpeaker("STORED_MUSIC", "PLAY_STATE", server+"track-1.mp3")
	defer follower.srv.Close()
	fleet := map[string]*fakeSpeaker{}
	withFakeFleet(t, fleet) // empty fleet: nobody answers yet
	slept := withDissolveSweepSeams(t, nil, nil)
	// The follower "comes back" after the first sleep.
	prev := dissolveSweepSleep
	dissolveSweepSleep = func(d time.Duration) {
		prev(d)
		fleet["192.0.2.54"] = follower
	}

	s := newSweepServer(t, sweepDoc)
	s.runStragglerSweepAfterFirmwareDissolve(zoneDocFingerprint(sweepDoc), server+"track-2.mp3", sweepDoc.Slaves)

	if got := follower.stops(); got != 1 {
		t.Errorf("stop keys sent = %d, want 1", got)
	}
	if len(*slept) < 1 {
		t.Errorf("expected at least one delayed pass, slept %v", *slept)
	}
}

// A different group formed meanwhile takes care of itself; the old sweep stands down.
func TestFirmwareDissolveSweepStandsDownForANewGroup(t *testing.T) {
	const server = "http://192.0.2.17:10243/lib/"
	follower := newFakeSpeaker("STORED_MUSIC", "PLAY_STATE", server+"track-1.mp3")
	defer follower.srv.Close()
	withFakeFleet(t, map[string]*fakeSpeaker{"192.0.2.54": follower})
	withDissolveSweepSeams(t, nil, nil)

	s := newSweepServer(t, sweepDoc)
	other := sweepDoc
	other.Slaves = []zones.Member{{DeviceID: "DEV-B", IP: "192.0.2.55"}}
	if err := s.zones.Set(other); err != nil {
		t.Fatal(err)
	}
	s.runStragglerSweepAfterFirmwareDissolve(zoneDocFingerprint(sweepDoc), server+"track-2.mp3", sweepDoc.Slaves)

	if got := follower.stops(); got != 0 {
		t.Errorf("a member outside the current group was stopped %d time(s)", got)
	}
}

// An unreadable master zone is not "gone": no pass sweeps on it.
func TestFirmwareDissolveSweepSkipsWhenTheZoneIsUnreadable(t *testing.T) {
	const server = "http://192.0.2.17:10243/lib/"
	follower := newFakeSpeaker("STORED_MUSIC", "PLAY_STATE", server+"track-1.mp3")
	defer follower.srv.Close()
	withFakeFleet(t, map[string]*fakeSpeaker{"192.0.2.54": follower})
	withDissolveSweepSeams(t, nil, errors.New("timeout"))

	s := newSweepServer(t, sweepDoc)
	s.runStragglerSweepAfterFirmwareDissolve(zoneDocFingerprint(sweepDoc), server+"track-2.mp3", sweepDoc.Slaves)

	if got := follower.stops(); got != 0 {
		t.Errorf("a follower was stopped %d time(s) on an unreadable zone", got)
	}
}

// Repeated dissolve frames for one collapse schedule one sweep.
func TestFirmwareDissolveScheduleIsIdempotentWhileBusy(t *testing.T) {
	s := newSweepServer(t, sweepDoc)
	s.dissolveSweepBusy.Store(true)
	s.scheduleStragglerSweepAfterFirmwareDissolve(sweepDoc) // must return at once, no goroutine
	if !s.dissolveSweepBusy.Load() {
		t.Error("busy flag was cleared by a rejected schedule")
	}
}
