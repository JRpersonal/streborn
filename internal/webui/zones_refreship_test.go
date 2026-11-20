package webui

import (
	"context"
	"testing"

	"github.com/JRpersonal/streborn/internal/zones"
)

// #2006: the re-form on play uses the address the member has now, found by
// the deviceID its own firmware reports. The peer list's deviceID is the
// Wi-Fi chip's MAC on a two-chip chassis and must not be what decides
// (field test 2026-11-20 on a SoundTouch 10).
func TestRefreshGroupIPsFindsAMovedMemberByItsFirmwareID(t *testing.T) {
	s := newRejoinServer()
	s.zones = zones.New()
	z := zones.Zone{Master: "AAAA", MasterIP: "10.0.0.1", Permanent: true, Slaves: []zones.Member{
		{DeviceID: "BBBB", IP: "10.0.0.2"}, // moved to .22
		{DeviceID: "CCCC", IP: "10.0.0.3"}, // still at .3
	}}
	_ = s.zones.Set(z)
	firmware := map[string]string{"10.0.0.22": "bbbb", "10.0.0.3": "CCCC", "10.0.0.4": "DDDD"}
	old := groupMemberFirmwareID
	groupMemberFirmwareID = func(_ context.Context, ip string) string { return firmware[ip] }
	defer func() { groupMemberFirmwareID = old }()
	s.cachedPeersFn = func() []PeerLink {
		return []PeerLink{
			{DeviceID: "WLAN-MAC-B", IP: "10.0.0.22", Reachable: true},
			{DeviceID: "WLAN-MAC-C", IP: "10.0.0.3", Reachable: true},
			{DeviceID: "WLAN-MAC-D", IP: "10.0.0.4", Reachable: true},
			{DeviceID: "WLAN-MAC-X", IP: "10.0.0.9", Reachable: false},
		}
	}
	got := s.refreshGroupIPs(z)
	if got.Slaves[0].IP != "10.0.0.22" || got.Slaves[1].IP != "10.0.0.3" {
		t.Fatalf("members = %+v", got.Slaves)
	}
	stored, _ := s.zones.Get()
	if stored.Slaves[0].IP != "10.0.0.22" {
		t.Errorf("new address not stored: %+v", stored.Slaves)
	}
	if z.Slaves[0].IP != "10.0.0.2" {
		t.Errorf("caller's zone was modified in place: %+v", z.Slaves)
	}
}

// A member that still answers at its stored address costs one /info and
// nothing else changes.
func TestRefreshGroupIPsLeavesAMemberThatStillAnswers(t *testing.T) {
	s := newRejoinServer()
	z := zones.Zone{Master: "AAAA", Permanent: true, Slaves: []zones.Member{{DeviceID: "BBBB", IP: "10.0.0.2"}}}
	old := groupMemberFirmwareID
	groupMemberFirmwareID = func(_ context.Context, ip string) string {
		if ip == "10.0.0.2" {
			return "BBBB"
		}
		t.Errorf("asked %s although the member answered at its stored address", ip)
		return ""
	}
	defer func() { groupMemberFirmwareID = old }()
	s.cachedPeersFn = func() []PeerLink { return []PeerLink{{IP: "10.0.0.7", Reachable: true}} }
	if got := s.refreshGroupIPs(z); got.Slaves[0].IP != "10.0.0.2" {
		t.Fatalf("members = %+v", got.Slaves)
	}
}
