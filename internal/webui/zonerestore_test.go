package webui

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zones"
)

func restoreServer(t *testing.T, withStore bool) (*Server, *zones.Store) {
	t.Helper()
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if !withStore {
		return s, nil
	}
	store, err := zones.Load(filepath.Join(t.TempDir(), "zones.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.zones = store
	return s, store
}

// Reported 2026-08-16 from an eleven-speaker fleet: a group of two was playing,
// a third speaker was added, and 24 s later there was no group at all. The
// master's own :8090 had stopped answering during the drive and the firmware
// dissolved the pair. Adding a speaker must not be able to cost the group that
// was already working.
func TestAFailedFormPutsTheWorkingGroupBack(t *testing.T) {
	prev := boxapi.Zone{
		Master:  "DEV#MASTER",
		Members: []boxapi.ZoneMember{{DeviceID: "DEV#ONE", IP: "192.0.2.11"}},
	}
	s, store := restoreServer(t, true)
	if err := store.Set(zones.Zone{Master: "DEV#MASTER", Slaves: []zones.Member{{DeviceID: "DEV#ONE", IP: "192.0.2.11"}}}); err != nil {
		t.Fatal(err)
	}
	prevDoc, had := store.Get()

	var formed []boxapi.ZoneMember
	// The firmware dissolved the group, which is the reported case.
	gone := func(context.Context) (boxapi.Zone, error) { return boxapi.Zone{}, nil }
	record := func(_ context.Context, _ boxapi.ZoneMember, m []boxapi.ZoneMember) error {
		formed = append(formed, m...)
		return nil
	}

	s.restorePreviousZoneVia(context.Background(), gone, record,
		boxapi.ZoneMember{DeviceID: "DEV#MASTER"}, prevDoc, had, prev)

	if len(formed) != 1 || formed[0].DeviceID != "DEV#ONE" {
		t.Fatalf("the group that was playing was not formed again: %+v", formed)
	}
	got, ok := store.Get()
	if !ok || len(got.Slaves) != 1 || got.Slaves[0].DeviceID != "DEV#ONE" {
		t.Errorf("stored document was not put back: %+v", got)
	}
}

// When there was no group before, the document must not be left describing the
// one that failed: a stored group nobody formed is what the reconcile loop
// would later drive toward.
func TestAFailedFirstFormLeavesNoGroupOnRecord(t *testing.T) {
	s, store := restoreServer(t, true)
	if err := store.Set(zones.Zone{Master: "DEV#MASTER", Slaves: []zones.Member{{DeviceID: "DEV#NEW"}}}); err != nil {
		t.Fatal(err)
	}
	s.restorePreviousZoneVia(context.Background(),
		func(context.Context) (boxapi.Zone, error) { return boxapi.Zone{}, nil },
		func(context.Context, boxapi.ZoneMember, []boxapi.ZoneMember) error { return nil },
		boxapi.ZoneMember{DeviceID: "DEV#MASTER"}, zones.Zone{}, false, boxapi.Zone{})

	if z, ok := store.Get(); ok && len(z.Slaves) > 0 {
		t.Errorf("a group that never formed stayed on record: %+v", z)
	}
}

// The opposite case, equally important: when the firmware still has the group,
// re-forming it would tear down a working zone to rebuild it.
func TestAFailedFormLeavesAnIntactGroupAlone(t *testing.T) {
	s, _ := restoreServer(t, false)
	intact := boxapi.Zone{Master: "DEV#MASTER", Members: []boxapi.ZoneMember{{DeviceID: "DEV#ONE"}}}
	calls := 0
	s.restorePreviousZoneVia(context.Background(),
		func(context.Context) (boxapi.Zone, error) { return intact, nil },
		func(context.Context, boxapi.ZoneMember, []boxapi.ZoneMember) error { calls++; return nil },
		boxapi.ZoneMember{DeviceID: "DEV#MASTER"}, zones.Zone{}, false, intact)
	if calls != 0 {
		t.Errorf("an intact group was re-formed anyway (%d calls)", calls)
	}
}

// A follower must not rebuild the group around itself: that is a different
// group than the one the user lost.
func TestOnlyTheSpeakerThatLedTheGroupRebuildsIt(t *testing.T) {
	s, _ := restoreServer(t, false)
	calls := 0
	s.restorePreviousZoneVia(context.Background(),
		func(context.Context) (boxapi.Zone, error) { return boxapi.Zone{}, nil },
		func(context.Context, boxapi.ZoneMember, []boxapi.ZoneMember) error { calls++; return nil },
		boxapi.ZoneMember{DeviceID: "DEV#FOLLOWER"}, zones.Zone{}, false,
		boxapi.Zone{Master: "DEV#SOMEONEELSE", Members: []boxapi.ZoneMember{{DeviceID: "DEV#ONE"}}})
	if calls != 0 {
		t.Errorf("a follower formed a group of its own (%d calls)", calls)
	}
}

// A speaker that is wedged gets exactly one attempt. Hammering a box that
// stopped answering is what turns one bad minute into several.
func TestTheRestoreIsAttemptedOnce(t *testing.T) {
	s, _ := restoreServer(t, false)
	calls := 0
	s.restorePreviousZoneVia(context.Background(),
		func(context.Context) (boxapi.Zone, error) { return boxapi.Zone{}, errors.New("no answer") },
		func(context.Context, boxapi.ZoneMember, []boxapi.ZoneMember) error {
			calls++
			return errors.New("no answer")
		},
		boxapi.ZoneMember{DeviceID: "DEV#MASTER"}, zones.Zone{}, false,
		boxapi.Zone{Master: "DEV#MASTER", Members: []boxapi.ZoneMember{{DeviceID: "DEV#ONE"}}})
	if calls != 1 {
		t.Errorf("expected exactly one attempt, got %d", calls)
	}
}
