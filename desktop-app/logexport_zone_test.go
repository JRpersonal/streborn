package main

import (
	"strings"
	"testing"
)

// The zone JSON stores speaker and pair names under plain "name" keys, which
// the friendlyName-keyed scrub skips by design; a real household speaker name
// shipped in the clear that way (bundle 2026-08-29, remembered[].name). The
// structured pass must hash every zone name and keep the rest intact.
func TestAnonymizeZoneJSONHashesSpeakerNames(t *testing.T) {
	in := `{"grouped":false,"stereo":{"name":"Praxis Paar","master":"AABBCCDDEEFF"},` +
		`"remembered":[{"deviceID":"112233445566","ip":"192.168.178.56","name":"Bose Praxis Rechts"}]}`
	out := anonymizeZoneJSON(in)
	for _, leak := range []string{"Praxis Paar", "Bose Praxis Rechts", "192.168.178.56"} {
		if strings.Contains(out, leak) {
			t.Errorf("zone anonymizer leaked %q in %s", leak, out)
		}
	}
	if !strings.Contains(out, "NAME#") {
		t.Errorf("zone names not hashed: %s", out)
	}
	if !strings.Contains(out, "grouped") {
		t.Errorf("structure lost: %s", out)
	}
	// Broken JSON must still get the text pass, never panic.
	if got := anonymizeZoneJSON("{not json"); got == "" {
		t.Error("fallback for unparseable zone JSON returned empty")
	}
}
