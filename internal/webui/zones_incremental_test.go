package webui

import (
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// Adding a speaker to a playing group used to run a full /setZone re-form per
// tap, each one restarting the master's stream (three audible gaps in 20 s,
// live 2026-08-21). zoneDiff is the split that lets an existing zone take new
// members incrementally instead.

func TestZoneDiffAddOnly(t *testing.T) {
	live := boxapi.Zone{Master: "MASTER", Members: []boxapi.ZoneMember{
		{DeviceID: "AAA", IP: "192.0.2.11"},
	}}
	want := []boxapi.ZoneMember{
		{DeviceID: "AAA", IP: "192.0.2.11"},
		{DeviceID: "BBB", IP: "192.0.2.12"},
	}
	toAdd, toRemove := zoneDiff(live, want)
	if len(toAdd) != 1 || toAdd[0].DeviceID != "BBB" {
		t.Fatalf("toAdd = %+v, want only BBB", toAdd)
	}
	if len(toRemove) != 0 {
		t.Fatalf("toRemove = %+v, want none", toRemove)
	}
}

func TestZoneDiffRemoveOnly(t *testing.T) {
	live := boxapi.Zone{Master: "MASTER", Members: []boxapi.ZoneMember{
		{DeviceID: "AAA", IP: "192.0.2.11"},
		{DeviceID: "BBB", IP: "192.0.2.12"},
	}}
	want := []boxapi.ZoneMember{{DeviceID: "AAA", IP: "192.0.2.11"}}
	toAdd, toRemove := zoneDiff(live, want)
	if len(toAdd) != 0 {
		t.Fatalf("toAdd = %+v, want none", toAdd)
	}
	if len(toRemove) != 1 || toRemove[0].DeviceID != "BBB" {
		t.Fatalf("toRemove = %+v, want only BBB", toRemove)
	}
}

func TestZoneDiffMixed(t *testing.T) {
	live := boxapi.Zone{Master: "MASTER", Members: []boxapi.ZoneMember{
		{DeviceID: "OLD", IP: "192.0.2.11"},
	}}
	want := []boxapi.ZoneMember{{DeviceID: "NEW", IP: "192.0.2.12"}}
	toAdd, toRemove := zoneDiff(live, want)
	if len(toAdd) != 1 || toAdd[0].DeviceID != "NEW" || len(toRemove) != 1 || toRemove[0].DeviceID != "OLD" {
		t.Fatalf("toAdd=%+v toRemove=%+v, want NEW added and OLD removed", toAdd, toRemove)
	}
}

// A two-chip chassis (Portable, ST20 BCO) is listed by the firmware under its
// SCM deviceID while discovery hands the caller its wlan0 MAC. The IP is the
// stable key: a live member requested again under the other MAC must be
// neither re-added nor removed.
func TestZoneDiffMatchesOnIPWhenDeviceIDsDiffer(t *testing.T) {
	live := boxapi.Zone{Master: "MASTER", Members: []boxapi.ZoneMember{
		{DeviceID: "SCMMAC", IP: "192.0.2.11"},
	}}
	want := []boxapi.ZoneMember{{DeviceID: "WLANMAC", IP: "192.0.2.11"}}
	toAdd, toRemove := zoneDiff(live, want)
	if len(toAdd) != 0 || len(toRemove) != 0 {
		t.Fatalf("toAdd=%+v toRemove=%+v, want the aliased member recognized as unchanged", toAdd, toRemove)
	}
}

// A firmware member row can miss the ipaddress attribute; the deviceID match
// must then hold the member in the zone.
func TestZoneDiffFallsBackToDeviceIDWithoutIP(t *testing.T) {
	live := boxapi.Zone{Master: "MASTER", Members: []boxapi.ZoneMember{
		{DeviceID: "AAA", IP: ""},
	}}
	want := []boxapi.ZoneMember{{DeviceID: "aaa", IP: "192.0.2.11"}}
	toAdd, toRemove := zoneDiff(live, want)
	if len(toAdd) != 0 || len(toRemove) != 0 {
		t.Fatalf("toAdd=%+v toRemove=%+v, want case-insensitive deviceID match to hold", toAdd, toRemove)
	}
}

func TestZoneDiffEmptyLiveZoneAddsEverything(t *testing.T) {
	want := []boxapi.ZoneMember{
		{DeviceID: "AAA", IP: "192.0.2.11"},
		{DeviceID: "BBB", IP: "192.0.2.12"},
	}
	toAdd, toRemove := zoneDiff(boxapi.Zone{}, want)
	if len(toAdd) != 2 || len(toRemove) != 0 {
		t.Fatalf("toAdd=%+v toRemove=%+v, want all added, none removed", toAdd, toRemove)
	}
}

// The stream-survival gate: after an INCREMENTAL join the master that is still
// audibly playing must NOT get the re-push (the existing members keep hearing
// the surviving stream, so that push IS the audible gap); an idle box falls
// through to the push, the historical safe behavior for the fresh-zone 1036
// case.
func TestResumeAfterZoneFormSkipsWhenStreamSurvivedAnIncrementalJoin(t *testing.T) {
	s, rec := newPlayTestServer(t)
	s.playStateFn = func() (bool, bool) { return false, true } // playing
	s.resumeAfterZoneForm(zoneResume{
		push:                   lastPlayInfo{boxURL: "http://192.0.2.1:8888/stream/1", title: "x"},
		survivorReachesMembers: true,
	})
	if rec.count() != 0 {
		t.Fatalf("stream survived an incremental join but the resume pushed anyway: %v", rec.list())
	}
}

// On a FRESH full form the members have nothing yet, so a master stream that
// survived the change must be re-pushed anyway to reach them. The v0.9.56 skip
// fired here too, and a user who grouped mid-stream got playing master plus
// silent members until a manual play (Martin, 2026-08-24).
func TestResumeAfterZoneFormPushesTheSurvivorOnAFreshForm(t *testing.T) {
	s, rec := newPlayTestServer(t)
	s.playStateFn = func() (bool, bool) { return false, true } // still playing
	lp := lastPlayInfo{boxURL: "http://192.0.2.1:8888/stream/1", title: "x"}
	s.lastPlayMu.Lock()
	cp := lp
	s.lastPlay = &cp
	s.lastPlayMu.Unlock()
	s.resumeAfterZoneForm(zoneResume{push: lp, ref: &cp, survivorReachesMembers: false})
	if !rec.has("SetAVTransportURI") {
		t.Fatalf("fresh form with a surviving master stream, but no re-push reached the members: %v", rec.list())
	}
}

func TestResumeAfterZoneFormPushesWhenBoxWentSilent(t *testing.T) {
	s, rec := newPlayTestServer(t)
	s.playStateFn = func() (bool, bool) { return false, false } // idle
	lp := lastPlayInfo{boxURL: "http://192.0.2.1:8888/stream/1", title: "x"}
	s.lastPlayMu.Lock()
	cp := lp
	s.lastPlay = &cp // the capture in handleZoneForm is a copy of lastPlay
	s.lastPlayMu.Unlock()
	s.resumeAfterZoneForm(zoneResume{push: lp, ref: &cp, survivorReachesMembers: true})
	if !rec.has("SetAVTransportURI") {
		t.Fatalf("box went silent but no re-push was sent: %v", rec.list())
	}
}
