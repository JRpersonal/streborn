package webui

import (
	"testing"

	"github.com/JRpersonal/streborn/internal/zones"
)

// #2006: the re-form on play uses the address the speaker list knows now,
// not the one stored when the group was set up, and only from a peer that
// answered the last sweep.
func TestRefreshGroupIPsUsesTheCurrentPeerAddress(t *testing.T) {
	s := newRejoinServer()
	s.zones = zones.New()
	z := zones.Zone{Master: "AAAA", MasterIP: "10.0.0.1", Permanent: true, Slaves: []zones.Member{
		{DeviceID: "BBBB", IP: "10.0.0.2"},
		{DeviceID: "CCCC", IP: "10.0.0.3"},
	}}
	_ = s.zones.Set(z)
	s.cachedPeersFn = func() []PeerLink {
		return []PeerLink{
			{DeviceID: "BBBB", IP: "10.0.0.22", Reachable: true},
			{DeviceID: "CCCC", IP: "10.0.0.33", Reachable: false}, // stale entry: ignored
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
