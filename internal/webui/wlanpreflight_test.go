package webui

import "testing"

// wlanSurveyVerdict is the decision the switch pre-flight makes from a site
// survey. It is split out so the three cases can be stated plainly: the
// difference between "not there" and "could not look" is what a user's evening
// depends on.
func wlanSurveyVerdict(ssids []string, err error, target string) string {
	switch {
	case err != nil:
		return "proceed" // survey failed
	case len(ssids) == 0:
		return "proceed" // nothing reported: absence of evidence
	default:
		for _, s := range ssids {
			if s == target {
				return "proceed"
			}
		}
		return "refuse"
	}
}

// A speaker on ethernet with no Wi-Fi configured surveys and finds nothing.
// Refusing on that told a user his network was invisible and blamed 2.4 GHz,
// while listing no networks at all, when he was correcting a mistyped password
// on a cabled ST20 (mail, 2026-08-15).
func TestAnEmptySurveyDoesNotRefuseTheSwitch(t *testing.T) {
	if got := wlanSurveyVerdict(nil, nil, "Vodafone-0674"); got != "proceed" {
		t.Errorf("empty survey = %q, want proceed: nothing reported is not proof the network is missing", got)
	}
	if got := wlanSurveyVerdict([]string{}, nil, "Vodafone-0674"); got != "proceed" {
		t.Errorf("empty slice = %q, want proceed", got)
	}
}

// A survey that DID see networks and not this one is real evidence, and the
// refusal there is what stops a user stranding a speaker on a network it
// cannot reach.
func TestASurveyThatSawOthersStillRefuses(t *testing.T) {
	seen := []string{"Nachbar-WLAN", "FRITZ!Box 7590"}
	if got := wlanSurveyVerdict(seen, nil, "Vodafone-0674"); got != "refuse" {
		t.Errorf("verdict = %q, want refuse: the speaker looked and this network was not there", got)
	}
	if got := wlanSurveyVerdict(seen, nil, "FRITZ!Box 7590"); got != "proceed" {
		t.Errorf("verdict = %q, want proceed for a network the speaker can see", got)
	}
}
