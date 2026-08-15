package webui

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/JRpersonal/streborn/internal/zones"
)

func serverWithZone(t *testing.T, z zones.Zone, stored bool) (*Server, *zones.Store) {
	t.Helper()
	st, err := zones.Load(filepath.Join(t.TempDir(), "zones.json"))
	if err != nil {
		t.Fatalf("zone store: %v", err)
	}
	if stored {
		if err := st.Set(z); err != nil {
			t.Fatalf("store: %v", err)
		}
	}
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), zones: st}
	return s, st
}

// The pair document is written BEFORE the firmware is asked, on purpose: a
// timed-out /addGroup can still have formed the pair. What was missing is the
// other half. A refused pairing left the document on NAND, so the speaker
// behaved as half of a pair for good, and power-on resume was disabled on it
// silently, because that check reads the store alone. The user had been told
// it failed, so they never pressed undo.
func TestARefusedPairingTakesItsDocumentBack(t *testing.T) {
	s, st := serverWithZone(t, zones.Zone{
		Master: "DEV-LEFT", MasterIP: "192.0.2.10", Stereo: true,
		Slaves: []zones.Member{{DeviceID: "DEV-RIGHT", IP: "192.0.2.20", Role: "right"}},
	}, true)

	s.dropStereoDocAfterFailure("addGroup refused")

	if z, ok := st.Get(); ok && z.Stereo {
		t.Errorf("the pair document survived a refused pairing: %+v", z)
	}
}

// A multiroom zone is not ours to take back here. Clearing it would dissolve a
// working group because a pairing attempt failed.
func TestAFailedPairingLeavesAMultiroomZoneAlone(t *testing.T) {
	zone := zones.Zone{
		Master: "DEV-SELF", MasterIP: "192.0.2.10",
		Slaves: []zones.Member{{DeviceID: "DEV-KITCHEN", IP: "192.0.2.20"}},
	}
	s, st := serverWithZone(t, zone, true)

	s.dropStereoDocAfterFailure("addGroup refused")

	z, ok := st.Get()
	if !ok || z.Stereo || len(z.Slaves) != 1 {
		t.Errorf("a multiroom zone must survive a failed pairing untouched: %+v ok=%v", z, ok)
	}
}

// Nothing stored is the normal case on a speaker that was never paired, and it
// must not turn into an error or a write.
func TestAFailedPairingWithNothingStoredIsQuiet(t *testing.T) {
	s, st := serverWithZone(t, zones.Zone{}, false)
	s.dropStereoDocAfterFailure("addGroup refused")
	if _, ok := st.Get(); ok {
		t.Error("nothing was stored, so nothing may appear")
	}
}
