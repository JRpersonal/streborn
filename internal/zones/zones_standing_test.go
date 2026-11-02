package zones

import (
	"path/filepath"
	"testing"
)

func permanentKitchen() Zone {
	return Zone{Master: "AAAA", Permanent: true, Slaves: []Member{{DeviceID: "DINING"}}}
}

func downstairs() Zone {
	return Zone{Master: "AAAA", Name: "Downstairs", Slaves: []Member{{DeviceID: "LOUNGE"}, {DeviceID: "HALL"}}}
}

// The reported case (2026-11-02): a group key formed a different group on the
// permanent group's main speaker, and the permanent group was gone for good.
func TestATemporaryGroupDoesNotLoseThePermanentOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zones.json")
	s, _ := Load(path)
	if err := s.Set(permanentKitchen()); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(downstairs()); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(); got.Name != "Downstairs" {
		t.Fatalf("the temporary group must be the current one, got %+v", got)
	}
	if st, ok := s.Standing(); !ok || !st.Permanent || st.Slaves[0].DeviceID != "DINING" {
		t.Fatalf("the permanent group must be set aside, got %+v ok=%v", st, ok)
	}

	// It survives an agent restart.
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Standing(); !ok {
		t.Fatal("the set-aside permanent group was not persisted")
	}

	// Ending the temporary group brings the permanent one back.
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get()
	if !ok || !got.Permanent || got.Slaves[0].DeviceID != "DINING" {
		t.Fatalf("the permanent group must be back after the temporary one ended, got %+v ok=%v", got, ok)
	}
	if _, ok := s.Standing(); ok {
		t.Fatal("nothing may stay set aside once the permanent group is back")
	}

	// And ending THAT leaves the box standalone, as before.
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get(); ok {
		t.Fatal("clearing the permanent group must leave the box standalone")
	}
}

// Speakers added by hand form one group after another; the first permanent
// group stays set aside through all of them.
func TestSeveralTemporaryGroupsKeepTheFirstPermanentOne(t *testing.T) {
	s, _ := Load(filepath.Join(t.TempDir(), "zones.json"))
	_ = s.Set(permanentKitchen())
	_ = s.Set(downstairs())
	bigger := downstairs()
	bigger.Slaves = append(bigger.Slaves, Member{DeviceID: "OFFICE"})
	_ = s.Set(bigger)
	_ = s.Clear()
	if got, _ := s.Get(); !got.Permanent || len(got.Slaves) != 1 {
		t.Fatalf("the permanent group must come back, got %+v", got)
	}
}

// A new permanent group is the user changing their standing group, so it
// replaces the old one and nothing is kept aside.
func TestANewPermanentGroupReplacesTheOldOne(t *testing.T) {
	s, _ := Load(filepath.Join(t.TempDir(), "zones.json"))
	_ = s.Set(permanentKitchen())
	_ = s.Set(downstairs())
	np := downstairs()
	np.Permanent = true
	_ = s.Set(np)
	if _, ok := s.Standing(); ok {
		t.Fatal("a new permanent group must not keep the old one aside")
	}
	_ = s.Clear()
	if _, ok := s.Get(); ok {
		t.Fatal("clearing the only permanent group must leave the box standalone")
	}
}

func TestForgetDropsTheSetAsideGroupToo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zones.json")
	s, _ := Load(path)
	_ = s.Set(permanentKitchen())
	_ = s.Set(downstairs())
	if err := s.Forget(); err != nil {
		t.Fatal(err)
	}
	s, _ = Load(path)
	if _, ok := s.Get(); ok {
		t.Fatal("Forget must leave no group")
	}
	if _, ok := s.Standing(); ok {
		t.Fatal("Forget must leave nothing set aside")
	}
}

// An agent from before this change reads the same file: the current group's
// fields stay at the top level.
func TestTheFileKeepsTheCurrentGroupAtTheTopLevel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zones.json")
	s, _ := Load(path)
	_ = s.Set(permanentKitchen())
	_ = s.Set(downstairs())
	s2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s2.Get(); got.Name != "Downstairs" || got.Master != "AAAA" {
		t.Fatalf("current group not at the top level: %+v", got)
	}
}
