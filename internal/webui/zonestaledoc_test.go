package webui

// A group document that the speaker itself no longer confirms used to disable
// the power-on resume and the reconnect recovery for good, without a single
// error anywhere the user could see it.
//
// The field case (ST20, str-diagnostic, 2026-08-22): the firmware dropped the
// zone by itself at 14:08:20 ("box ws: zoneUpdated -> zone dissolved"), the
// phone's Speakers tab logged "the stored group is not on the speaker any more"
// fifteen times that evening, and at 20:55:26 the wake still logged "box is in a
// zone / stereo pair, not auto-resuming (self-wake guard)" while a valid
// last-play ("Radio Seefunk", 329 minutes old) was waiting to be resumed.
//
// These tests drive boxInZone through the fetchZone seam the other box tests in
// this package use, against a real zones.Store on a temp dir, so both halves of
// the verdict are checked: what boxInZone answers AND what is left on NAND.

import (
	"path/filepath"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zones"
)

// staleDocServer returns a quiet server on host with a real zone store holding
// z (when store is true).
func staleDocServer(t *testing.T, host string, z zones.Zone, store bool) (*Server, *zones.Store) {
	t.Helper()
	st, err := zones.Load(filepath.Join(t.TempDir(), "zones.json"))
	if err != nil {
		t.Fatalf("zone store: %v", err)
	}
	if store {
		if err := st.Set(z); err != nil {
			t.Fatalf("store the group: %v", err)
		}
	}
	s := quietServer(host)
	s.zones = st
	return s, st
}

func nativeDoc() zones.Zone {
	return zones.Zone{
		Master: "DEV-MASTER", MasterIP: "192.0.2.10",
		Slaves: []zones.Member{{DeviceID: "DEV-SLAVE", IP: "192.0.2.20"}},
	}
}

func storedGroup(t *testing.T, st *zones.Store) bool {
	t.Helper()
	_, ok := st.Get()
	return ok
}

// The bug itself: the speaker answers, twice, that it is in no zone, and the
// stored document is dropped so the resume works again.
func TestSecondEmptyZoneReadDropsTheStaleGroupDocument(t *testing.T) {
	s, st := staleDocServer(t, "192.0.2.10", nativeDoc(), true)
	// What the reporter's speaker answered all evening: no zone at all.
	withSpeakers(t, map[string]boxapi.Zone{"192.0.2.10": {}}, map[string]string{"192.0.2.10": "DEV-MASTER"})

	if !s.boxInZone() {
		t.Fatal("the FIRST empty read must change nothing: a zone still forming reads empty mid-handshake")
	}
	if !storedGroup(t, st) {
		t.Fatal("the document was dropped on a single observation")
	}
	if s.boxInZone() {
		t.Fatal("after the speaker denied the group twice, the resume must not stand down any more")
	}
	if storedGroup(t, st) {
		t.Fatal("the stale document is still on NAND, so every other reader keeps believing in the group")
	}
}

// A read that FAILS is not evidence. The reporter's own log has this two seconds
// after the agent started, while the box firmware was still refusing :8090:
// "reconnect recovery: box in a zone / stereo pair, standing down".
func TestAFailedZoneReadKeepsTheGroupDocument(t *testing.T) {
	s, st := staleDocServer(t, "192.0.2.10", nativeDoc(), true)
	withSpeakers(t, map[string]boxapi.Zone{}, nil) // nobody answers

	for i := 0; i < 3; i++ {
		if !s.boxInZone() {
			t.Fatal("an unreadable speaker must keep its group, or a read error becomes a resume that fights the master")
		}
	}
	if !storedGroup(t, st) {
		t.Fatal("an unreadable speaker lost its group document")
	}
}

// A speaker with no document and no answer stays standalone, exactly as before:
// a momentary read failure must not disable the resume for the majority.
func TestAFailedZoneReadWithoutADocumentStaysStandalone(t *testing.T) {
	s, _ := staleDocServer(t, "192.0.2.10", zones.Zone{}, false)
	withSpeakers(t, map[string]boxapi.Zone{}, nil)

	if s.boxInZone() {
		t.Fatal("a standalone speaker whose zone read failed must still resume")
	}
}

// A speaker that really is in a zone is left alone, and any earlier doubt is
// void. The second half is asserted through behaviour rather than by reading the
// mark: a speaker that has just confirmed its zone must be back to needing the
// FULL two observations, so the next empty read may not drop anything.
func TestALiveZoneKeepsTheGuardAndClearsTheDoubt(t *testing.T) {
	s, st := staleDocServer(t, "192.0.2.10", nativeDoc(), true)
	live := boxapi.Zone{Master: "DEV-MASTER", Members: []boxapi.ZoneMember{{DeviceID: "DEV-SLAVE", IP: "192.0.2.20"}}}
	answers := map[string]boxapi.Zone{"192.0.2.10": live}
	withSpeakers(t, answers, map[string]string{"192.0.2.10": "DEV-MASTER"})

	// One empty read first, so a doubt is on record before the zone is confirmed.
	answers["192.0.2.10"] = boxapi.Zone{}
	if !s.boxInZone() {
		t.Fatal("first observation must change nothing")
	}
	answers["192.0.2.10"] = live
	for i := 0; i < 3; i++ {
		if !s.boxInZone() {
			t.Fatal("a speaker whose firmware reports the zone must never auto-resume")
		}
	}
	if !storedGroup(t, st) {
		t.Fatal("a live group lost its document")
	}
	answers["192.0.2.10"] = boxapi.Zone{}
	if !s.boxInZone() || !storedGroup(t, st) {
		t.Fatal("the confirmed zone was dropped on a single empty read, so the earlier doubt outlived the confirmation")
	}
}

// The firmware emits a dissolve on its way THROUGH a group change: /setZone
// tears the previous zone down before building the new one, and a fresh zone
// self-dissolves ~300 ms after reporting ok when the master wakes into a stale
// UPnP item (#70). Those frames land after handleZoneForm has already persisted
// the NEW document, so a doubt left armed by them would let the first empty read
// anywhere delete a group that formed perfectly well.
func TestAZoneTheFirmwareConfirmsWithdrawsTheDissolvesDoubt(t *testing.T) {
	s, st := staleDocServer(t, "192.0.2.10", nativeDoc(), true)
	withSpeakers(t, map[string]boxapi.Zone{"192.0.2.10": {}}, map[string]string{"192.0.2.10": "DEV-MASTER"})

	s.NoteBoxZoneState("")           // the tear-down half of the change
	s.NoteBoxZoneState("DEV-MASTER") // the group the firmware then formed
	if !s.boxInZone() || !storedGroup(t, st) {
		t.Fatal("a group the firmware confirmed was dropped on its first empty read, because the mid-change dissolve was still held against it")
	}
}

// A stereo pair is NOT a firmware zone: it lives in /getGroup, and a healthy
// pair answers <zone /> on every chassis. Judging it by /getZone would delete
// the only record the pair has.
func TestAStereoPairSurvivesAnEmptyZoneRead(t *testing.T) {
	pair := zones.Zone{
		Master: "DEV-MASTER", MasterIP: "192.0.2.10", Stereo: true,
		Slaves: []zones.Member{{DeviceID: "DEV-SLAVE", IP: "192.0.2.20", Role: "RIGHT"}},
	}
	s, st := staleDocServer(t, "192.0.2.10", pair, true)
	withSpeakers(t, map[string]boxapi.Zone{"192.0.2.10": {}}, map[string]string{"192.0.2.10": "DEV-MASTER"})

	for i := 0; i < 3; i++ {
		if !s.boxInZone() {
			t.Fatal("a stereo pair must keep the self-wake guard: its partner can wake it")
		}
	}
	if !storedGroup(t, st) {
		t.Fatal("the pair document was deleted by an empty /getZone")
	}
}

// A mirror group is STR's own construct; the firmware never hears about it, so
// an empty /getZone says nothing about it either.
func TestAMirrorGroupSurvivesAnEmptyZoneRead(t *testing.T) {
	doc := nativeDoc()
	doc.Mode = "mirror"
	s, st := staleDocServer(t, "192.0.2.10", doc, true)
	withSpeakers(t, map[string]boxapi.Zone{"192.0.2.10": {}}, map[string]string{"192.0.2.10": "DEV-MASTER"})

	for i := 0; i < 3; i++ {
		if !s.boxInZone() {
			t.Fatal("a mirror group must keep the self-wake guard")
		}
	}
	if !storedGroup(t, st) {
		t.Fatal("the mirror group document was deleted by an empty /getZone, and nothing else records that group")
	}
}

// Unchanged: no document, the speaker says no zone, the speaker resumes.
func TestAStandaloneSpeakerStillResumes(t *testing.T) {
	s, _ := staleDocServer(t, "192.0.2.10", zones.Zone{}, false)
	withSpeakers(t, map[string]boxapi.Zone{"192.0.2.10": {}}, map[string]string{"192.0.2.10": "DEV-MASTER"})

	if s.boxInZone() {
		t.Fatal("a standalone speaker must resume on power-on")
	}
}

// The reporter's exact sequence: the phone's Speakers tab sees the contradiction
// first (fifteen times), then the speaker wakes. That wake must resume, and the
// document must be gone.
func TestTheDisplayPathsContradictionCountsForTheWake(t *testing.T) {
	s, st := staleDocServer(t, "192.0.2.10", nativeDoc(), true)
	withSpeakers(t, map[string]boxapi.Zone{"192.0.2.10": {}}, map[string]string{"192.0.2.10": "DEV-MASTER"})

	// 20:53:00 through 20:55:18 in the bundle: the group card resolves to
	// standalone while the document still insists.
	if s.storedGroupIsLive(ownZoneAnswer{read: true}) {
		t.Fatal("an empty firmware zone must make the stored group report standalone")
	}
	// 20:55:26: the wake.
	if s.boxInZone() {
		t.Fatal("the wake stood down on a group the speaker had already denied on the display path")
	}
	if storedGroup(t, st) {
		t.Fatal("the stale document survived the wake")
	}
}

// The firmware's own dissolve frame counts as the first observation, so a
// speaker whose owner never opens the Speakers tab does not lose an extra
// resume. It must not clear anything by itself: the live read still decides.
func TestAFirmwareDissolveCountsAsAnObservation(t *testing.T) {
	s, st := staleDocServer(t, "192.0.2.10", nativeDoc(), true)
	withSpeakers(t, map[string]boxapi.Zone{"192.0.2.10": {}}, map[string]string{"192.0.2.10": "DEV-MASTER"})

	s.NoteBoxZoneState("") // 14:08:20 in the bundle
	if !storedGroup(t, st) {
		t.Fatal("the frame alone dropped the document; only a live read may confirm it")
	}
	if s.boxInZone() {
		t.Fatal("the wake after a firmware dissolve plus an empty live read must resume")
	}
	if storedGroup(t, st) {
		t.Fatal("the document survived a firmware dissolve confirmed by a live read")
	}
}

// A doubt is about ONE group. Tear the group down, build a different one, and
// the fresh group gets the full two observations again, because its first empty
// read is exactly the mid-handshake race.
func TestADoubtIsNotSpentOnADifferentGroup(t *testing.T) {
	s, st := staleDocServer(t, "192.0.2.10", nativeDoc(), true)
	withSpeakers(t, map[string]boxapi.Zone{"192.0.2.10": {}}, map[string]string{"192.0.2.10": "DEV-MASTER"})

	if !s.boxInZone() {
		t.Fatal("first observation must change nothing")
	}
	// A different group, formed while the old doubt is still recorded.
	fresh := zones.Zone{
		Master: "DEV-MASTER", MasterIP: "192.0.2.10",
		Slaves: []zones.Member{{DeviceID: "DEV-OTHER", IP: "192.0.2.30"}},
	}
	if err := st.Set(fresh); err != nil {
		t.Fatalf("store the fresh group: %v", err)
	}
	if !s.boxInZone() || !storedGroup(t, st) {
		t.Fatal("a freshly formed group was dropped on its first empty read, which is the forming race")
	}
	if s.boxInZone() || storedGroup(t, st) {
		t.Fatal("the fresh group survived two denials from the speaker")
	}
}
