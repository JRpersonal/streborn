package webui

import (
	"os"
	"strings"
	"testing"
)

// zonesSourceFiles are the files zones_stereo.go was split into. The tests
// that pin the SHAPE of the group and pair code (which call comes before
// which, that a guard is still there) read them as one text, so a function
// moving between these files does not break a test that is about its body.
var zonesSourceFiles = []string{
	"zones_stereo.go",
	"zones_get.go",
	"zones_balance.go",
	"zones_form.go",
	"zones_form_native.go",
	"zones_afterform.go",
	"zones_followers.go",
	"zones_stereo_pair.go",
	"zones_reconcile.go",
	"zones_dissolve.go",
}

// stereoPairPhases are the phases of formStereoPair, in the order it runs
// them.
var stereoPairPhases = []string{
	"stereoPairStart",
	"stereoPairResolvePartner",
	"stereoPairGuards",
	"stereoPairCapture",
	"stereoPairWake",
	"stereoPairPersist",
	"stereoPairAddGroup",
	"stereoPairUnread",
	"stereoPairConfirm",
}

// stereoPairSource returns the bodies of formStereoPair's phases as one
// text, in running order, for the tests that pin which step comes before
// which across the whole pairing.
func stereoPairSource(t *testing.T, src string) string {
	t.Helper()
	body := ""
	for _, fn := range stereoPairPhases {
		part := funcBody(t, src, fn)
		if part == "" {
			t.Fatalf("%s not found; if a phase of formStereoPair was renamed, update stereoPairPhases", fn)
		}
		body += part + "\n"
	}
	return body
}

// The phase bodies are read in the order of stereoPairPhases, so that list
// has to be the order formStereoPair really runs them in.
func TestStereoPairPhasesAreListedInRunningOrder(t *testing.T) {
	t.Parallel()
	body := funcBody(t, zonesSource(t), "formStereoPair")
	if body == "" {
		t.Fatal("formStereoPair not found")
	}
	last := -1
	for _, fn := range stereoPairPhases {
		at := strings.Index(body, "s."+fn+"(")
		if at < 0 {
			t.Fatalf("formStereoPair does not call %s", fn)
		}
		if at < last {
			t.Errorf("%s is called earlier than stereoPairPhases lists it", fn)
		}
		last = at
	}
}

// zonesSource returns the multiroom and stereo-pair sources as one text, with
// line endings normalised.
func zonesSource(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, name := range zonesSourceFiles {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		b.WriteString(strings.ReplaceAll(string(src), "\r\n", "\n"))
		b.WriteString("\n")
	}
	return b.String()
}
