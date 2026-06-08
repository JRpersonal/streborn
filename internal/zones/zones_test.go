package zones

import (
	"path/filepath"
	"testing"
)

func TestLoadMissingIsStandalone(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "zones.json"))
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if _, ok := s.Get(); ok {
		t.Errorf("expected standalone for missing file")
	}
}

func TestSetGetRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zones.json")
	s, _ := Load(path)
	z := Zone{
		Master:   "AAAA",
		MasterIP: "192.0.2.10",
		Slaves:   []Member{{DeviceID: "BBBB", IP: "192.0.2.11", Role: "NORMAL"}},
		Name:     "Living room",
	}
	if err := s.Set(z); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// Reload from disk to prove persistence.
	s2, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := s2.Get()
	if !ok {
		t.Fatalf("expected a persisted zone")
	}
	if got.Master != "AAAA" || got.MasterIP != "192.0.2.10" || len(got.Slaves) != 1 || got.Slaves[0].DeviceID != "BBBB" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if !got.IsMaster("AAAA") || got.IsMaster("BBBB") {
		t.Errorf("IsMaster wrong: %+v", got)
	}
}

func TestClearReturnsStandalone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zones.json")
	s, _ := Load(path)
	_ = s.Set(Zone{Master: "AAAA", Slaves: []Member{{DeviceID: "BBBB"}}})
	if err := s.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, ok := s.Get(); ok {
		t.Errorf("expected standalone after Clear")
	}
	// Empty object on disk must reload as standalone, not an error.
	s2, err := Load(path)
	if err != nil {
		t.Fatalf("reload after clear: %v", err)
	}
	if _, ok := s2.Get(); ok {
		t.Errorf("expected standalone after reload of cleared store")
	}
}
