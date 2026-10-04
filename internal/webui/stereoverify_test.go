package webui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

const (
	vMaster  = "AAAAAAAAAAAA"
	vPartner = "BBBBBBBBBBBB"
)

var fastVerify = stereoVerifyTiming{budget: 30 * time.Millisecond, gap: 5 * time.Millisecond}

func pairGroup(status string) boxapi.Group {
	return boxapi.Group{ID: "g", MasterDeviceID: vMaster, Status: status, Members: []boxapi.ZoneMember{
		{DeviceID: vMaster, Role: "LEFT"}, {DeviceID: vPartner, Role: "RIGHT"},
	}}
}

func fetchFrom(master, partner func() (boxapi.Group, error)) groupFetch {
	return func(_ context.Context, host string) (boxapi.Group, error) {
		if host == "master" {
			return master()
		}
		return partner()
	}
}

func ok(g boxapi.Group) func() (boxapi.Group, error) {
	return func() (boxapi.Group, error) { return g, nil }
}

func verify(f groupFetch) stereoVerdict {
	return verifyStereoPair(context.Background(), f, "master", "partner", vMaster, vPartner, fastVerify)
}

func TestHealthyPairOnBothHalvesIsConfirmed(t *testing.T) {
	v := verify(fetchFrom(ok(pairGroup("GROUP_OK")), ok(pairGroup("GROUP_OK"))))
	if !v.OK || !v.PartnerVerified {
		t.Fatalf("healthy pair must be confirmed and verified: %+v", v)
	}
}

// The field case (two SoundTouch 10s, 2026-10-04): master reads two members
// with GROUP_ERROR, partner stored nothing. This was reported as ok:true.
func TestMasterGroupErrorIsAFailedPairing(t *testing.T) {
	v := verify(fetchFrom(ok(pairGroup("GROUP_ERROR")), ok(boxapi.Group{})))
	if v.OK || v.Reason != "partnerUnreachable" {
		t.Fatalf("GROUP_ERROR on the master must fail as partnerUnreachable: %+v", v)
	}
}

func TestPartnerThatStoredNothingFailsThePairing(t *testing.T) {
	v := verify(fetchFrom(ok(pairGroup("GROUP_OK")), ok(boxapi.Group{})))
	if v.OK || v.Reason != "partnerDidNotStore" {
		t.Fatalf("an empty partner must fail as partnerDidNotStore: %+v", v)
	}
}

// A partner whose firmware cannot be read is not held against a healthy
// master: the verdict says it could not verify instead of guessing.
func TestUnreadablePartnerWithHealthyMasterIsNotAFailure(t *testing.T) {
	v := verify(fetchFrom(ok(pairGroup("GROUP_OK")), func() (boxapi.Group, error) { return boxapi.Group{}, errors.New("timeout") }))
	if !v.OK || v.PartnerVerified {
		t.Fatalf("want ok with PartnerVerified=false: %+v", v)
	}
}

// The firmware's pairing takes a while: a pair that completes during the
// window is confirmed, not failed on the first read.
func TestPairThatCompletesDuringTheWindowIsConfirmed(t *testing.T) {
	reads := 0
	partner := func() (boxapi.Group, error) {
		reads++
		if reads < 3 {
			return boxapi.Group{}, nil
		}
		return pairGroup("GROUP_OK"), nil
	}
	v := verifyStereoPair(context.Background(), fetchFrom(ok(pairGroup("GROUP_OK")), partner),
		"master", "partner", vMaster, vPartner, stereoVerifyTiming{budget: 200 * time.Millisecond, gap: 5 * time.Millisecond})
	if !v.OK || !v.PartnerVerified {
		t.Fatalf("late partner must be confirmed: %+v (reads=%d)", v, reads)
	}
}

func TestPairNamesNeedsBothSpeakers(t *testing.T) {
	g := pairGroup("")
	if !pairNames(g, vMaster, vPartner) {
		t.Fatal("pair with both ids must match")
	}
	g.Members[1].DeviceID = "CCCCCCCCCCCC"
	if pairNames(g, vMaster, vPartner) {
		t.Fatal("pair with a third speaker must not match")
	}
}

func TestFailureTextNamesTheCause(t *testing.T) {
	if stereoFailureText("partnerUnreachable") == stereoFailureText("partnerDidNotStore") {
		t.Fatal("the two failure causes need different messages")
	}
}
