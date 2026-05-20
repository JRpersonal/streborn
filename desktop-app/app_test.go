// Regression tests for BoxInfo enrichment from the Bose /info XML.

package main

import "testing"

// sampleBoseInfoXML is the exact response shape we receive from
// /info on :8090 of a stock SoundTouch 10. Pulled live from a real
// box, then trimmed: serial / device-id fields are kept since the
// extractor's whole job is finding them, but they are anonymized
// per the public-hygiene memory (no real serials in tree).
const sampleBoseInfoXML = `<?xml version="1.0" encoding="UTF-8" ?><info deviceID="AABBCCDDEEFF"><name>Living Room</name><type>SoundTouch 10</type><margeAccountUUID>stick@local</margeAccountUUID><components><component><componentCategory>SCM</componentCategory><softwareVersion>27.0.6.46330.5043500 epdbuild.trunk.hepdswbld04.2022-08-04T11:20:29</softwareVersion><serialNumber>SCMSERIAL000000000000</serialNumber></component><component><componentCategory>PackagedProduct</componentCategory><softwareVersion>27.0.6.46330.5043500 epdbuild.trunk.hepdswbld04.2022-08-04T11:20:29</softwareVersion><serialNumber>069236P60560580AE</serialNumber></component></components><networkInfo type="SCM"><macAddress>AABBCCDDEEFF</macAddress><ipAddress>192.0.2.66</ipAddress></networkInfo><moduleType>sm2</moduleType><variant>rhino</variant></info>`

// TestExtractPackagedProductSerialPicksTheRightComponent guards
// against the most likely bug in the extractor: returning the SCM
// component's serial (the mainboard ID) instead of the
// PackagedProduct serial (the sticker on the bottom of the
// speaker). Users with two identical ST20s have to compare the
// stickers, so the wrong serial would make the Setup target picker
// useless for differentiation.
func TestExtractPackagedProductSerialPicksTheRightComponent(t *testing.T) {
	got := extractPackagedProductSerial(sampleBoseInfoXML)
	if got != "069236P60560580AE" {
		t.Errorf("extractPackagedProductSerial returned %q, want the PackagedProduct serial 069236P60560580AE "+
			"(returning SCMSERIAL... means the parser is grabbing the wrong component)", got)
	}
}

// TestExtractPackagedProductSerialHandlesMissingBlock checks the
// safety net for firmware variants that omit the PackagedProduct
// component entirely (some early ST20 firmwares only had SCM in
// /info). Empty return is fine; a panic or the wrong serial would
// not be.
func TestExtractPackagedProductSerialHandlesMissingBlock(t *testing.T) {
	noPP := `<info deviceID="X"><components><component><componentCategory>SCM</componentCategory><serialNumber>SCMONLY</serialNumber></component></components></info>`
	got := extractPackagedProductSerial(noPP)
	if got != "" {
		t.Errorf("extractPackagedProductSerial returned %q on XML with no PackagedProduct component, want empty", got)
	}
}

// TestExtractPackagedProductSerialHandlesEmptyOrJunk verifies the
// parser does not panic on degenerate input. The /info XML can be
// truncated by a slow box or an HTTP timeout midway through the
// response.
func TestExtractPackagedProductSerialHandlesEmptyOrJunk(t *testing.T) {
	cases := []string{
		"",
		"not xml at all",
		"<info><components></components></info>",
		// Cut off in the middle of the PackagedProduct block.
		`<components><component><componentCategory>PackagedProduct</componentCategory><serialNumber>069236`,
	}
	for _, in := range cases {
		_ = extractPackagedProductSerial(in) // just must not panic
	}
}
