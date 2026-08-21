package webui

import "testing"

// The whole safety posture of the boot guard is this one function, so every
// verdict is pinned here. Read the cases as the rules they encode:
//
//   - a speaker that did not associate at all belongs to run.sh's rescue
//     watchdog; two things pushing an offline box is how you strand it;
//   - a speaker whose intended network is not in its OWN site survey must be
//     left alone, which is what keeps "old network switched off" and "moved
//     house" working;
//   - a speaker that has failed too often stops being dragged around, whatever
//     else is true.
func TestGuardDecision(t *testing.T) {
	const want = "HomeNet"

	cases := []struct {
		name        string
		assoc       bool
		cur         string
		visible     bool
		bootsFailed int
		wantAction  guardAction
		wantReason  string
	}{
		{
			name:  "on the intended network, nothing to do",
			assoc: true, cur: want, visible: true,
			wantAction: guardNone, wantReason: guardReasonOnTarget,
		},
		{
			name:  "came up on the old network and the intended one is in range",
			assoc: true, cur: "OldNet", visible: true,
			wantAction: guardReapply, wantReason: guardReasonWrongNet,
		},
		{
			name:  "came up elsewhere but the intended network is not in range",
			assoc: true, cur: "OldNet", visible: false,
			wantAction: guardStandDown, wantReason: guardReasonNotInRange,
		},
		{
			name:  "no association at all: the boot rescue owns this box",
			assoc: false, cur: "", visible: true,
			wantAction: guardStandDown, wantReason: guardReasonNoAssoc,
		},
		{
			name:  "failure budget exhausted",
			assoc: true, cur: "OldNet", visible: true, bootsFailed: maxFailedBoots,
			wantAction: guardGiveUp, wantReason: guardReasonBudget,
		},
		{
			// The budget outranks everything, including a boot that would
			// otherwise have been fine. It still reports give-up rather than
			// silently correcting, so the log says why nothing happens.
			name:  "budget exhausted outranks even a matching boot",
			assoc: true, cur: want, visible: true, bootsFailed: maxFailedBoots + 3,
			wantAction: guardGiveUp, wantReason: guardReasonBudget,
		},
		{
			// One below the budget still acts: the budget is a ceiling, not a
			// countdown that stops early.
			name:  "last attempt before the budget runs out still corrects",
			assoc: true, cur: "OldNet", visible: true, bootsFailed: maxFailedBoots - 1,
			wantAction: guardReapply, wantReason: guardReasonWrongNet,
		},
		{
			// An unreadable or empty site survey arrives here as "not visible",
			// which must stand down rather than act blind.
			name:  "unreadable survey is treated as not in range",
			assoc: true, cur: "OldNet", visible: false, bootsFailed: 2,
			wantAction: guardStandDown, wantReason: guardReasonNotInRange,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			action, reason := guardDecision(c.assoc, c.cur, want, c.visible, c.bootsFailed)
			if action != c.wantAction || reason != c.wantReason {
				t.Errorf("guardDecision(assoc=%v, cur=%q, want=%q, visible=%v, bootsFailed=%d) = (%v, %q), want (%v, %q)",
					c.assoc, c.cur, want, c.visible, c.bootsFailed,
					action, reason, c.wantAction, c.wantReason)
			}
		})
	}
}

// The verdict strings land in the log and in the intent record, so a bundle is
// read by them. An action that renders as an integer is a bundle nobody can
// decide.
func TestGuardActionString(t *testing.T) {
	for action, want := range map[guardAction]string{
		guardNone:      "none",
		guardReapply:   "reapply",
		guardStandDown: "stand-down",
		guardGiveUp:    "give-up",
		guardAction(9): "unknown",
	} {
		if got := action.String(); got != want {
			t.Errorf("guardAction(%d).String() = %q, want %q", action, got, want)
		}
	}
}

func TestWPAApplyResultString(t *testing.T) {
	for res, want := range map[wpaApplyResult]string{
		wpaConfirmed:       "confirmed",
		wpaNotAssociated:   "not-associated",
		wpaCannotApply:     "cannot-apply",
		wpaApplyResult(42): "unknown",
	} {
		if got := res.String(); got != want {
			t.Errorf("wpaApplyResult(%d).String() = %q, want %q", res, got, want)
		}
	}
}

// Pruning the running supplicant is the one step here that can lock a speaker
// out of every network it knows, so the decision is pinned separately from the
// wpa_cli calls that execute it.
func TestWPANetworksToRemove(t *testing.T) {
	nets := []wlanNetwork{
		{ID: 0, SSID: "OldNet"},
		{ID: 1, SSID: "Guest"},
		{ID: 2, SSID: "HomeNet", Current: true},
		{ID: 3, SSID: "OldNet"}, // duplicates happen: NetManager re-injects
	}

	got := wpaNetworksToRemove(nets, "HomeNet")
	wantIDs := []string{"0", "1", "3"}
	if len(got) != len(wantIDs) {
		t.Fatalf("wpaNetworksToRemove = %v, want %v", got, wantIDs)
	}
	for i, id := range wantIDs {
		if got[i] != id {
			t.Errorf("wpaNetworksToRemove[%d] = %q, want %q", i, got[i], id)
		}
	}

	// The refusals. Removing everything leaves the speaker with no network at
	// all, and a speaker with no network falls back to the Bose setup AP and
	// disappears from the LAN.
	if got := wpaNetworksToRemove(nets, "NetworkThatIsNotThere"); got != nil {
		t.Errorf("pruning toward an absent SSID must change nothing, got %v", got)
	}
	if got := wpaNetworksToRemove(nets, ""); got != nil {
		t.Errorf("pruning toward an empty SSID must change nothing, got %v", got)
	}
	if got := wpaNetworksToRemove(nil, "HomeNet"); got != nil {
		t.Errorf("pruning an empty list must change nothing, got %v", got)
	}
	// Nothing to do when the target is the only network.
	if got := wpaNetworksToRemove([]wlanNetwork{{ID: 7, SSID: "HomeNet"}}, "HomeNet"); got != nil {
		t.Errorf("a supplicant that already holds only the target must be left alone, got %v", got)
	}
}

// The list parse feeds both the diagnostic bundle and the pruning decision, so
// a header line read as a network would be a network removed by mistake.
func TestParseWPANetworkList(t *testing.T) {
	raw := "network id / ssid / bssid / flags\n" +
		"0\tOldNet\tany\t\n" +
		"1\tHomeNet\tany\t[CURRENT]\n" +
		"2\tGuest\tany\t[DISABLED]\n" +
		"Selected interface 'wlan0'\n" +
		"\n"

	nets := parseWPANetworkList(raw)
	if len(nets) != 3 {
		t.Fatalf("parsed %d networks, want 3: %+v", len(nets), nets)
	}
	if nets[1].SSID != "HomeNet" || !nets[1].Current {
		t.Errorf("the [CURRENT] network was not identified: %+v", nets[1])
	}
	if nets[0].Current || nets[2].Current {
		t.Errorf("only one network may be current: %+v", nets)
	}
	if nets[2].Flags != "[DISABLED]" {
		t.Errorf("flags lost: %+v", nets[2])
	}
}

// Every network in a bundle needs its scrub-proof identity, or the bundle
// shows four blanks again. This covers both lists the diagnostic section
// carries: the running supplicant and the firmware's own store.
func TestNetworkTagging(t *testing.T) {
	nets := []wlanNetwork{{ID: 0, SSID: "OldNet"}, {ID: 1, SSID: "HomeNet"}, {ID: 2, SSID: ""}}
	tagNetworks(nets)
	for _, n := range nets {
		if n.SSIDTag == "" {
			t.Errorf("network %d carries no tag: %+v", n.ID, n)
		}
		if n.SSIDTag != ssidTag(n.SSID) {
			t.Errorf("network %d tag %q does not match ssidTag(%q)", n.ID, n.SSIDTag, n.SSID)
		}
	}
	if nets[0].SSIDTag == nets[1].SSIDTag {
		t.Error("two different networks were tagged the same")
	}

	tags := ssidTagsOf(nets)
	if len(tags) != len(nets) {
		t.Fatalf("ssidTagsOf returned %d tags for %d networks", len(tags), len(nets))
	}
	for i, tag := range tags {
		if tag != nets[i].SSIDTag {
			t.Errorf("tag %d = %q, want %q", i, tag, nets[i].SSIDTag)
		}
	}
	// An empty list has to render as an empty list, not as null, so
	// "runningNets=0 runningTags=[]" reads as a measurement rather than as a
	// missing field.
	if got := ssidTagsOf(nil); got == nil || len(got) != 0 {
		t.Errorf("ssidTagsOf(nil) = %v, want an empty list", got)
	}
}
