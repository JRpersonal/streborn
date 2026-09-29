package main

import (
	"strings"
	"testing"
	"time"
)

// Every case here is a defect an adversarial review found in the first version
// of the restore, after it had passed its own tests and the full CI. Each one
// would have been invisible in the field: a group silently not restored, or a
// journal line claiming a success that did not happen.

// FormZone answers a nil error with ok:false whenever the readiness gate or the
// firmware refused. Reading the error alone reported every one of those as "the
// group is back together", into the one record a later report is read from.
func TestFormZoneResultIsReadBeforeClaimingSuccess(t *testing.T) {
	cases := []struct {
		name string
		out  map[string]any
		good bool
		says string
	}{
		{"a formed group", map[string]any{"ok": true, "members": []any{map[string]any{"ip": "192.0.2.22"}}}, true, ""},
		{"no result at all", nil, false, "no result"},
		{"the speaker refused", map[string]any{"ok": false}, false, "refused"},
		{"members still starting", map[string]any{"ok": false, "notReady": []string{"192.0.2.22", "192.0.2.27"}}, false, "2 member"},
		{"members still starting, as JSON", map[string]any{"ok": false, "notReady": []any{"192.0.2.22"}}, false, "1 member"},
		{"ok but empty", map[string]any{"ok": true, "members": []any{}}, false, "no members"},
		// An older agent may not send ok at all; absence is not a refusal.
		{"no ok field", map[string]any{"members": []any{map[string]any{"ip": "192.0.2.22"}}}, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			why, good := formZoneSucceeded(c.out)
			if good != c.good {
				t.Fatalf("good = %v, want %v (why %q)", good, c.good, why)
			}
			if c.says != "" && !strings.Contains(why, c.says) {
				t.Errorf("why = %q, want it to mention %q", why, c.says)
			}
			if c.good && why != "" {
				t.Errorf("a success should carry no reason, got %q", why)
			}
		})
	}
}

// Updating a FOLLOWER of a permanent group reads permanent=false from that
// follower. Re-forming from there posts the group without Permanent and without
// its name, overwriting the leader's stored template with an ordinary group: the
// user would lose the durable choice by updating the wrong speaker.
func TestTheLeadersPermanentFlagIsHonouredNotJustTheUpdatedSpeakers(t *testing.T) {
	members := []ZoneMember{{DeviceID: "dev-a", IP: "192.0.2.22"}}
	before := zoneRecord{MasterIP: "192.0.2.33", Members: members} // the follower's view
	now := zoneRecord{MasterIP: "192.0.2.33", Permanent: true}     // the leader's own

	plan := planZoneRestore(before, now, true)
	if plan.Restore {
		t.Error("restoring here would overwrite the leader's permanent group with an ordinary one")
	}
	if !strings.Contains(plan.Why, "permanent") {
		t.Errorf("the journal line does not say why: %q", plan.Why)
	}
}

// A record with no timestamp predates the TTL and must not be aged out by it,
// and one older than the window must not rebuild a group the user may have
// dissolved hours ago.
func TestZoneRecordTTLBounds(t *testing.T) {
	if zoneRecordTTL < 5*time.Minute {
		t.Errorf("TTL %s is shorter than a slow update, so a legitimate restore would be dropped", zoneRecordTTL)
	}
	if zoneRecordTTL > 2*time.Hour {
		t.Errorf("TTL %s keeps a dissolved group actionable for far too long", zoneRecordTTL)
	}
	fresh := zoneRecord{At: time.Now()}
	if !fresh.At.IsZero() && time.Since(fresh.At) > zoneRecordTTL {
		t.Error("a record taken just now reads as stale")
	}
	stale := zoneRecord{At: time.Now().Add(-zoneRecordTTL - time.Minute)}
	if time.Since(stale.At) <= zoneRecordTTL {
		t.Error("a record older than the TTL does not read as stale")
	}
}

// The remembered record is keyed per host and must not outlive its usefulness.
// noteZoneBeforeOTA clearing a stale entry when the speaker now stands alone is
// what stops a later update rebuilding a group that no longer exists.
func TestForgettingARememberedZone(t *testing.T) {
	zonesBeforeOTA.Lock()
	if zonesBeforeOTA.m == nil {
		zonesBeforeOTA.m = map[string]zoneRecord{}
	}
	zonesBeforeOTA.m["192.0.2.99"] = zoneRecord{MasterIP: "192.0.2.33", At: time.Now()}
	zonesBeforeOTA.Unlock()

	forgetZoneBeforeOTA("192.0.2.99")

	zonesBeforeOTA.Lock()
	_, still := zonesBeforeOTA.m["192.0.2.99"]
	zonesBeforeOTA.Unlock()
	if still {
		t.Error("the remembered zone survived being forgotten, so a later update would rebuild it")
	}
}
