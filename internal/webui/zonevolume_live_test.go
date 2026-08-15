package webui

import (
	"context"
	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zones"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The phone's group card is built from the stored zone document. When a group
// disappears without STR writing that file (a failed re-form, a firmware that
// dropped the zone, a factory reset) the file keeps insisting and the card is
// drawn for a group that no longer exists. Pressing play then sends audio into
// a zone the firmware never had, which from the sofa looks like a broken
// speaker.
//
// Observed on three of the maintainer's speakers 2026-08-09: the living room
// claimed to lead a group with the bathroom, the portable claimed to lead one
// with the living room, the bathroom claimed nothing, and the living room's own
// firmware answered <zone /> to all of it.
func TestStoredGroupIsCheckedAgainstTheSpeaker(t *testing.T) {
	src, err := readSourceFile("zonevolume.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	if !contains(src, "GetZone(ctx)") {
		t.Error("nothing asks the speaker whether the stored group is still there")
	}
	if !contains(src, `live.Master == "" && len(live.Members) == 0`) {
		t.Error("an empty firmware zone does not make the stored group report standalone")
	}
	// It must stay a read: a write here would reset the deep-standby countdown,
	// and this runs whenever the phone opens its Speakers tab.
	for _, w := range []string{"SetZone(", "RemoveZoneSlave(", "AddZoneSlave("} {
		if contains(src, w) {
			t.Errorf("the zone-volume file performs %s; the cross-check must never write to the speaker", w)
		}
	}
}

func readSourceFile(name string) (string, error) {
	b, err := os.ReadFile(name)
	return string(b), err
}

func contains(hay, needle string) bool { return strings.Contains(hay, needle) }

// A stereo pair must never be judged by /getZone. It is a firmware group made
// with /addGroup, and /getZone answers <zone /> for a perfectly healthy pair.
// Applying the liveness check to one reported a working pair as standalone six
// seconds after it was created, caught live on two SoundTouch 10s 2026-08-09:
//
//	19:44:48  stereo: paired id=str-grp-... members=2
//	19:44:54  zone: the stored group is not on the speaker any more
//
// Asserted through the resolver rather than by grepping the source: the check
// has moved once already, and a test that reads the code cannot tell a
// refactor from a regression.
func TestStereoPairIsNotJudgedByGetZone(t *testing.T) {
	dir := t.TempDir()
	st, err := zones.Load(filepath.Join(dir, "zones.json"))
	if err != nil {
		t.Fatalf("zone store: %v", err)
	}
	if err := st.Set(zones.Zone{Master: "DEV-SELF", MasterIP: "192.0.2.10", Stereo: true,
		Slaves: []zones.Member{{DeviceID: "DEV-RIGHT", IP: "192.0.2.20", Role: "right"}}}); err != nil {
		t.Fatalf("store the pair: %v", err)
	}
	// What a healthy pair's firmware answers: nothing at all about a zone.
	withSpeakers(t, map[string]boxapi.Zone{"192.0.2.10": {}}, map[string]string{"192.0.2.10": "DEV-SELF"})

	s := quietServer("192.0.2.10")
	s.zones = st
	members, grouped, stereo := s.groupView(context.Background())
	if !grouped || !stereo {
		t.Fatalf("a healthy stereo pair read as grouped=%v stereo=%v, and an empty /getZone must not decide that", grouped, stereo)
	}
	if len(members) != 2 {
		t.Errorf("want both halves of the pair, got %d: %+v", len(members), members)
	}
}
