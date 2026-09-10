package boxws

import (
	"encoding/xml"
	"testing"
)

// The box emits <balanceUpdated/> alongside source changes, and until it was
// typed every one went through the unrecognized-frame path: one field bundle
// counted 30 of them (#70, 2026-09-09), and the first of a shape is logged at
// Info, the level the NAND-mirrored log keeps.
//
// Recognising it is all that is wanted here. The frame carries no value, the
// balance itself is read over HTTP, and it must NOT count as explained user
// activity: like swUpdateStatusUpdated it rides along with source changes, so
// feeding it to the thumb heuristic would swallow real thumb presses. That is
// why it belongs in the documented-no-op case rather than getting a hook.
func TestBalanceUpdatedParsesIntoItsOwnField(t *testing.T) {
	for _, raw := range []string{
		`<updates deviceID="DEV1"><balanceUpdated></balanceUpdated></updates>`,
		`<updates deviceID="DEV1"><balanceUpdated/></updates>`,
	} {
		var f gabboFrame
		if err := xml.Unmarshal([]byte(raw), &f); err != nil {
			t.Fatalf("parse %s: %v", raw, err)
		}
		if f.BalanceUpdated == nil {
			t.Fatalf("balanceUpdated did not parse into its own field, so it still reads as an unknown shape: %s", raw)
		}
	}
}

// A frame STR has no field for must keep reaching the unrecognized path, or the
// typing above would be indistinguishable from a catch-all that hides new
// shapes the firmware starts sending.
func TestAnUnknownShapeStillHasNoBalanceField(t *testing.T) {
	var f gabboFrame
	if err := xml.Unmarshal([]byte(`<updates deviceID="DEV1"><somethingNew/></updates>`), &f); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.BalanceUpdated != nil {
		t.Fatal("an unrelated frame set BalanceUpdated")
	}
}
