package webui

import (
	"os"
	"strings"
	"testing"
)

// The radio power-cycle must stay behind the setup check. It sits above the
// hasTarget gate, so without the check it reaches every sm2 speaker STR is
// installed on, whether or not its owner ever used STR's Wi-Fi feature, and a
// setup AP is exactly what it reads as a broken chip.
func TestTheRadioRecoveryStaysBehindTheSetupCheck(t *testing.T) {
	b, err := os.ReadFile("wlanguard.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "s.recoverUnassociatedRadio(")
	if i < 0 {
		t.Fatal("recoverUnassociatedRadio is no longer called; if it was renamed, update this test rather than deleting it")
	}
	before := src[:i]
	if !strings.Contains(before, "s.ownerIsProvisioning(ctx)") {
		t.Error("the radio is power-cycled without asking whether the owner is provisioning the speaker")
	}
}
