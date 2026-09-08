package webui

// Forming a stereo pair out of two speakers that are already in a multiroom
// group left that group standing inside the firmware.
//
// STR guarded the reverse direction only: a zone form is refused when a member
// is half of a pair (#792). The pair form was checked nowhere - not in this
// handler, not in the desktop, not in the frontend - so the firmware kept the
// zone behind the fresh pair: a third speaker went on following the pair's
// master and started refusing stations, and the agent's single-slot zone store
// overwrote the group document with the pair (field, 2026-09-07).
//
// The refusal is read through the same fetchZone seam the other box tests in
// this package use, so nothing here needs a speaker on :8090.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zones"
)

const (
	pairSelfHost    = "192.0.2.10"
	pairPartnerHost = "192.0.2.20"
)

// A live zone on the box that would become the LEFT channel.
func groupedZone() boxapi.Zone {
	return boxapi.Zone{
		Master:   "AABBCCDDEEFF",
		SenderIP: pairSelfHost,
		Members: []boxapi.ZoneMember{
			{DeviceID: "AABBCCDDEEFF", IP: pairSelfHost},
			{DeviceID: "112233445566", IP: "192.0.2.30"},
		},
	}
}

func TestPairRefusedWhenTheBoxReportsAZone(t *testing.T) {
	withSpeakers(t, map[string]boxapi.Zone{
		pairSelfHost:    groupedZone(),
		pairPartnerHost: {},
	}, nil)
	s := quietServer(pairSelfHost)

	got := s.zonedPairCandidates(context.Background(), pairPartnerHost)

	if len(got) != 1 || got[0] != pairSelfHost {
		t.Fatalf("the grouped speaker must be reported, got %v", got)
	}
}

func TestPairRefusedWhenThePARTNERReportsAZone(t *testing.T) {
	// The partner is the half the user did not think about: it carries the
	// group the pair would swallow just as well as this box does.
	withSpeakers(t, map[string]boxapi.Zone{
		pairSelfHost:    {},
		pairPartnerHost: groupedZone(),
	}, nil)
	s := quietServer(pairSelfHost)

	got := s.zonedPairCandidates(context.Background(), pairPartnerHost)

	if len(got) != 1 || got[0] != pairPartnerHost {
		t.Fatalf("the grouped partner must be reported, got %v", got)
	}
}

func TestPairAllowedWhenBothSpeakersAreStandalone(t *testing.T) {
	// This is also the shape a HEALTHY EXISTING PAIR reports: a stereo pair is
	// a firmware GROUP (/getGroup), and a paired speaker's /getZone answers no
	// master and no members, exactly like a standalone one. So re-forming or
	// renaming a pair must not be caught by this guard either.
	withSpeakers(t, map[string]boxapi.Zone{
		pairSelfHost:    {},
		pairPartnerHost: {},
	}, nil)
	s := quietServer(pairSelfHost)

	if got := s.zonedPairCandidates(context.Background(), pairPartnerHost); len(got) != 0 {
		t.Fatalf("two standalone speakers must pair, got %v", got)
	}
}

func TestPairAllowedWhenTheZoneReadFails(t *testing.T) {
	// Best effort, the same policy the desktop's own pair guard follows:
	// refusing a pairing because a speaker was busy for a moment would block a
	// legitimate action on no evidence at all.
	prev := fetchZone
	fetchZone = func(context.Context, string) (boxapi.Zone, error) {
		return boxapi.Zone{}, errors.New("speaker busy")
	}
	t.Cleanup(func() { fetchZone = prev })
	s := quietServer(pairSelfHost)

	if got := s.zonedPairCandidates(context.Background(), pairPartnerHost); len(got) != 0 {
		t.Fatalf("an unreadable zone must not refuse the pairing, got %v", got)
	}
}

// The refusal the app actually receives: HTTP 200 with ok:false and the reason,
// which is the shape the frontend's existing failure branch already displays.
// Driven with an empty partner address so the check runs before any firmware
// dial - the guard sits ahead of the wake on purpose, because waking resumes
// music and a refusal that arrives after that has already changed the thing it
// refused to change.
func TestPairRefusalAnswersTheAppWithTheReason(t *testing.T) {
	withSpeakers(t, map[string]boxapi.Zone{pairSelfHost: groupedZone()}, nil)
	s := quietServer(pairSelfHost)
	rec := httptest.NewRecorder()

	s.formStereoPair(rec, context.Background(), boxapi.New(pairSelfHost),
		boxapi.ZoneMember{DeviceID: "AABBCCDDEEFF", IP: pairSelfHost},
		[]boxapi.ZoneMember{{DeviceID: "112233445566"}}, "Living room")

	if rec.Code != 200 {
		t.Fatalf("the app must get a 200 it can read, got %d", rec.Code)
	}
	var out struct {
		OK     bool     `json:"ok"`
		Stereo bool     `json:"stereo"`
		InZone []string `json:"inZone"`
		Error  string   `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("answer is not JSON: %v (%s)", err, rec.Body.String())
	}
	if out.OK || !out.Stereo {
		t.Errorf("the refusal must read ok:false stereo:true, got %+v", out)
	}
	if len(out.InZone) != 1 || out.InZone[0] != pairSelfHost {
		t.Errorf("the refusal must name the grouped speaker, got %v", out.InZone)
	}
	if out.Error == "" {
		t.Error("the refusal must carry a reason the app can show")
	}
}

// storedServer is a speaker with a zone document on NAND, the way a formed
// group leaves it.
func storedServer(t *testing.T, z zones.Zone) *Server {
	t.Helper()
	st, err := zones.Load(filepath.Join(t.TempDir(), "zones.json"))
	if err != nil {
		t.Fatalf("zone store: %v", err)
	}
	if err := st.Set(z); err != nil {
		t.Fatalf("store the group: %v", err)
	}
	s := quietServer(pairSelfHost)
	s.zones = st
	return s
}

// A permanent group whose master is idle is STORED, not live: both speakers
// answer no master and no members, so the live guard waves the pairing through
// and the persist then replaces the group document with the pair. The user's
// only copy of that group is gone (the same class of loss as the wipe of
// 2026-09-03), so the stored document has to be consulted next to the live read.
func TestPairRefusedWhenAGroupIsStoredButNotLive(t *testing.T) {
	withSpeakers(t, map[string]boxapi.Zone{
		pairSelfHost:    {}, // idle: the permanent group is not formed right now
		pairPartnerHost: {},
	}, nil)
	s := storedServer(t, zones.Zone{
		Master: "AABBCCDDEEFF", MasterIP: pairSelfHost, Permanent: true,
		Slaves: []zones.Member{{DeviceID: "112233445566", IP: "192.0.2.30"}},
	})
	rec := httptest.NewRecorder()

	s.formStereoPair(rec, context.Background(), boxapi.New(pairSelfHost),
		boxapi.ZoneMember{DeviceID: "AABBCCDDEEFF", IP: pairSelfHost},
		[]boxapi.ZoneMember{{DeviceID: "999999999999"}}, "Living room")

	if rec.Code != 200 {
		t.Fatalf("the app must get a 200 it can read, got %d", rec.Code)
	}
	var out struct {
		OK          bool     `json:"ok"`
		Stereo      bool     `json:"stereo"`
		StoredGroup bool     `json:"storedGroup"`
		InZone      []string `json:"inZone"`
		Error       string   `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("answer is not JSON: %v (%s)", err, rec.Body.String())
	}
	if out.OK || !out.Stereo || !out.StoredGroup {
		t.Errorf("the refusal must read ok:false stereo:true storedGroup:true, got %+v", out)
	}
	if out.Error == "" {
		t.Error("the refusal must carry a reason the app can show")
	}
	// And the group document is still there: proving the refusal, not the
	// message, is what protects it.
	if z, ok := s.zones.Get(); !ok || z.Stereo || len(z.Slaves) != 1 {
		t.Fatalf("the stored group did not survive the refused pairing: %+v (present=%v)", z, ok)
	}
}

// A stale TEMPORARY group document must not block a pairing. A temporary group
// lives only as long as its live zone, so the firmware's answer is the truth
// about it and the live guard above already refuses while it stands. What is
// left in the store after that zone ended is invisible everywhere - it is not
// live, the frontend picker does not list it, and the user has nothing to
// dissolve - so refusing on it means those two speakers can never be paired
// again, with no way to clear the block from the app.
func TestPairAllowedWhenTheStoredGroupIsAStaleTemporaryOne(t *testing.T) {
	withSpeakers(t, map[string]boxapi.Zone{
		pairSelfHost:    {}, // the zone this document describes is long gone
		pairPartnerHost: {},
	}, nil)
	s := storedServer(t, zones.Zone{
		Master: "AABBCCDDEEFF", MasterIP: pairSelfHost, // Permanent deliberately unset
		Slaves: []zones.Member{{DeviceID: "112233445566", IP: "192.0.2.30"}},
	})

	if refused, z := s.storedGroupBlocksPair(); refused {
		t.Fatalf("a stale temporary group document must not block a pairing: %+v", z)
	}
}

// ...and a temporary group that is REALLY there is still refused, by the live
// read rather than by the stored document. Same store, same speakers; the only
// difference is that the firmware confirms the zone.
func TestPairRefusedWhenTheTemporaryGroupIsActuallyLive(t *testing.T) {
	withSpeakers(t, map[string]boxapi.Zone{
		pairSelfHost:    groupedZone(),
		pairPartnerHost: {},
	}, nil)
	s := storedServer(t, zones.Zone{
		Master: "AABBCCDDEEFF", MasterIP: pairSelfHost,
		Slaves: []zones.Member{{DeviceID: "112233445566", IP: "192.0.2.30"}},
	})

	if got := s.zonedPairCandidates(context.Background(), pairPartnerHost); len(got) != 1 {
		t.Fatalf("the live zone must still refuse the pairing, got %v", got)
	}
}

// A stored PAIR is not a group: re-pairing the same two speakers and renaming
// an existing pair both come through this handler, and both must stay possible.
func TestPairAllowedWhenTheStoredDocumentIsAPair(t *testing.T) {
	withSpeakers(t, map[string]boxapi.Zone{
		pairSelfHost:    {},
		pairPartnerHost: {},
	}, nil)
	s := storedServer(t, zones.Zone{
		Master: "AABBCCDDEEFF", MasterIP: pairSelfHost, Stereo: true, Name: "Living room",
		Slaves: []zones.Member{{DeviceID: "112233445566", IP: pairPartnerHost}},
	})

	if refused, _ := s.storedGroupBlocksPair(); refused {
		t.Fatal("a stored stereo pair must not block re-pairing or renaming")
	}
}

// A master-only document (a group whose members were all removed) is no group
// either: there is nothing to lose, so nothing to refuse.
func TestPairAllowedWhenTheStoredDocumentHasNoMembers(t *testing.T) {
	s := storedServer(t, zones.Zone{Master: "AABBCCDDEEFF", MasterIP: pairSelfHost, Permanent: true})

	if refused, _ := s.storedGroupBlocksPair(); refused {
		t.Fatal("a memberless document must not block a pairing")
	}
}

// No store at all (a speaker that never formed a group) must not refuse either.
func TestPairAllowedWithNoZoneStore(t *testing.T) {
	if refused, _ := quietServer(pairSelfHost).storedGroupBlocksPair(); refused {
		t.Fatal("a speaker with no zone store must not refuse a pairing")
	}
}
