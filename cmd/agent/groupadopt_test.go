package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/marge"
)

func TestToFirmwareGroupKeepsEveryField(t *testing.T) {
	fg := toFirmwareGroup(boxapi.Group{
		ID: "g1", Name: "Wohnzimmer", MasterDeviceID: "AAA", Status: "GROUP_OK",
		Members: []boxapi.ZoneMember{
			{DeviceID: "AAA", Role: "LEFT", IP: "192.0.2.10"},
			{DeviceID: "BBB", Role: "RIGHT", IP: "192.0.2.11"},
		},
	})
	if fg.ID != "g1" || fg.Name != "Wohnzimmer" || fg.MasterDeviceID != "AAA" || fg.Status != "GROUP_OK" {
		t.Fatalf("group fields lost: %+v", fg)
	}
	if len(fg.Roles) != 2 || fg.Roles[1].DeviceID != "BBB" || fg.Roles[1].Role != "RIGHT" || fg.Roles[1].IP != "192.0.2.11" {
		t.Fatalf("roles lost: %+v", fg.Roles)
	}
}

func TestFirmwareGroupProbeNeedsAHost(t *testing.T) {
	if firmwareGroupProbe("") != nil {
		t.Fatal("no box host must mean no probe")
	}
}

// restoredPairServer is a marge server that restored a canonical pair record
// from disk, the state dropPhantomRestoredPair exists for. The document
// carries no addresses, so the restore starts no partner probe.
func restoredPairServer(t *testing.T) *marge.Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	path := filepath.Join(t.TempDir(), "marge-group.json")
	seed := marge.New(logger, marge.WithGroupPath(path))
	if err := seed.SetCanonicalGroup(marge.CanonicalGroupXML("Stereo pair", "AAAABBBBCCCC", "", "DDDDEEEEFFFF", "")); err != nil {
		t.Fatalf("seeding the pair record: %v", err)
	}
	m := marge.New(logger, marge.WithGroupPath(path))
	if !m.GroupRestoredUnconfirmed() {
		t.Fatal("the pair record was not restored")
	}
	return m
}

func runPhantomCheck(m *marge.Server, model string, modelErr error, group boxapi.Group, groupErr error) (groupReads int) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dropPhantomRestoredPair(context.Background(), m,
		func(context.Context) (string, error) { return model, modelErr },
		func(context.Context) (boxapi.Group, error) { groupReads++; return group, groupErr },
		time.Millisecond, logger)
	return groupReads
}

// #1964: a SoundTouch 30 kept a restored record naming a SoundTouch 10 as its
// master. Its /getGroup hangs, so the "firmware reports no pair" rule never
// fired, every play was refused as a pair follower and the app started the
// music on the SoundTouch 10 instead.
func TestRestoredPairOnAModelThatCannotPairIsDropped(t *testing.T) {
	m := restoredPairServer(t)
	reads := runPhantomCheck(m, "SoundTouch 30", nil, boxapi.Group{}, context.DeadlineExceeded)
	if _, _, ok := m.GroupPair(); ok {
		t.Fatal("a SoundTouch 30 still holds a stereo pair record")
	}
	if reads != 0 {
		t.Fatalf("the hanging /getGroup was read %d times on a model that cannot pair", reads)
	}
}

// The model rule must not cost a real pair: a SoundTouch 10 whose firmware is
// slow to answer keeps its record.
func TestRestoredPairOnASoundTouch10SurvivesASilentFirmware(t *testing.T) {
	m := restoredPairServer(t)
	runPhantomCheck(m, "SoundTouch 10", nil, boxapi.Group{}, context.DeadlineExceeded)
	if _, _, ok := m.GroupPair(); !ok {
		t.Fatal("a SoundTouch 10 lost its pair record while its firmware was not answering")
	}
}

func TestRestoredPairTheFirmwareStillHoldsIsKept(t *testing.T) {
	m := restoredPairServer(t)
	runPhantomCheck(m, "SoundTouch 10", nil, boxapi.Group{ID: "g1"}, nil)
	if _, _, ok := m.GroupPair(); !ok {
		t.Fatal("the pair record was dropped although the firmware still reports the pair")
	}
}

// The factory-reset rule, unchanged, also while the model is not known.
func TestRestoredPairTheFirmwareNoLongerHoldsIsDropped(t *testing.T) {
	for _, tc := range []struct {
		model string
		err   error
	}{{"SoundTouch 10", nil}, {"", nil}, {"", context.DeadlineExceeded}} {
		m := restoredPairServer(t)
		if reads := runPhantomCheck(m, tc.model, tc.err, boxapi.Group{}, nil); reads != 2 {
			t.Errorf("model %q: %d empty reads before the verdict, want 2", tc.model, reads)
		}
		if _, _, ok := m.GroupPair(); ok {
			t.Errorf("model %q: the record survived two empty /getGroup reads", tc.model)
		}
	}
}
