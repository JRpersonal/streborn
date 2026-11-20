package zones

import (
	"path/filepath"
	"testing"
)

// #2006: a member the router gave a new address stayed "unreachable" for every
// later re-form, because the stored hint was never brought up to date.
func TestRefreshIPsUpdatesMovedMembersOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zones.json")
	s, _ := Load(path)
	z := Zone{Master: "AAAA", MasterIP: "192.0.2.10", Permanent: true, Slaves: []Member{
		{DeviceID: "BBBB", IP: "192.0.2.11"},
		{DeviceID: "CCCC", IP: "192.0.2.12"},
	}}
	if err := s.Set(z); err != nil {
		t.Fatal(err)
	}
	n, err := s.RefreshIPs(map[string]string{"bbbb": "192.0.2.21", "AAAA": "192.0.2.99", "DDDD": "192.0.2.40"})
	if err != nil || n != 1 {
		t.Fatalf("RefreshIPs = %d, %v; want 1 hint updated", n, err)
	}
	s2, _ := Load(path)
	got, _ := s2.Get()
	if got.Slaves[0].IP != "192.0.2.21" || got.Slaves[1].IP != "192.0.2.12" {
		t.Errorf("members after refresh = %+v", got.Slaves)
	}
	if got.MasterIP != "192.0.2.10" || !got.Permanent || len(got.Slaves) != 2 {
		t.Errorf("refresh changed more than the member hints: %+v", got)
	}
	if n, _ := s.RefreshIPs(map[string]string{"BBBB": "192.0.2.21"}); n != 0 {
		t.Errorf("an unchanged address counted as a change (%d)", n)
	}
}

func TestRefreshIPsReachesASetAsidePermanentGroup(t *testing.T) {
	s, _ := Load(filepath.Join(t.TempDir(), "zones.json"))
	perm := Zone{Master: "AAAA", Permanent: true, Slaves: []Member{{DeviceID: "BBBB", IP: "192.0.2.11"}}}
	temp := Zone{Master: "AAAA", Slaves: []Member{{DeviceID: "CCCC", IP: "192.0.2.12"}}}
	_ = s.Set(perm)
	_ = s.Set(temp)
	if n, _ := s.RefreshIPs(map[string]string{"BBBB": "192.0.2.21"}); n != 1 {
		t.Fatalf("set-aside permanent group not refreshed (%d)", n)
	}
	_ = s.Clear()
	back, _ := s.Get()
	if !back.Permanent || back.Slaves[0].IP != "192.0.2.21" {
		t.Errorf("permanent group after Clear = %+v", back)
	}
}
