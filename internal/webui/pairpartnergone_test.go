package webui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// A speaker that is half of a stereo pair whose partner is gone is healthy,
// reachable and answers everything, and still cannot play a single thing: the
// firmware refuses every source activation while a pair is incomplete and drops
// straight back to standby five seconds later. Without this field the app can
// only say "the speaker is not responding", which sends the owner looking at his
// speaker, his network and his presets. Measured 2026-09-29: the partner had
// been off the network since 11 September and the owner pressed over eighty
// times.
//
// The shape is asserted on the document itself rather than through the handler,
// which needs a live box on :8090. A handler test here would pass on its error
// path without ever reaching the field, which is worse than no test.
func TestTheZoneDocumentCarriesAMissingPairPartner(t *testing.T) {
	doc := zoneAnswer{
		Zone:              boxapi.Zone{Members: []boxapi.ZoneMember{}},
		PairPartnerGone:   "192.0.2.146",
		PairPartnerGoneID: "DEV#f910b818",
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if got, _ := out["pairPartnerGone"].(string); got != "192.0.2.146" {
		t.Errorf("pairPartnerGone = %q, want the partner address; without it the app cannot name the real reason", got)
	}
	if got, _ := out["pairPartnerGoneId"].(string); got != "DEV#f910b818" {
		t.Errorf("pairPartnerGoneId = %q, want the partner id", got)
	}
}

// The overwhelming majority of speakers are not in a pair at all. The field has
// to be ABSENT for them, not present and empty, or every caller needs a
// truthiness check it should not need.
func TestAHealthySpeakerCarriesNoMissingPartner(t *testing.T) {
	b, err := json.Marshal(zoneAnswer{Zone: boxapi.Zone{Members: []boxapi.ZoneMember{}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "pairPartnerGone") {
		t.Errorf("the field is present for a speaker with no missing partner: %s", b)
	}
}
