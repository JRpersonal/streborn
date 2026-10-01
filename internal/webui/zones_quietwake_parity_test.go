package webui

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Both ways of forming a group have to wake a sleeping master QUIETLY.
//
// A plain wake lets the firmware resume the master's last source, so the act of
// creating a group starts music on its own. That was found and fixed twice for
// the zone path (Jens, 2026-08-31: "beim Erstellen einer Gruppe faengt die
// Gruppe an zu spielen", then again on 2026-09-06), and handleZoneForm has done
// the quiet wake ever since.
//
// formStereoPair was written mirroring handleZoneForm and its comment says so,
// but it copied only the plain wake: the quiet one was added to the sibling
// afterwards and never came across. So pairing two IDLE speakers still started
// playback by itself, with whichever preset was last showing as selected. It was
// reported with a log on 2026-09-30 (#1074), a month after the sibling was
// fixed, because nothing connected the two.
//
// This test is deliberately about the SHAPE of the source rather than the
// behaviour: the defect was never that either function was wrong on its own, it
// was that they drifted apart. Exercising formStereoPair properly needs a fake
// speaker answering /now_playing, /addGroup and /info, and that would still not
// have caught a sibling nobody remembered to update.
func TestBothGroupFormPathsWakeTheMasterQuietly(t *testing.T) {
	src, err := os.ReadFile("zones_stereo.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)

	for _, fn := range []string{"handleZoneForm", "formStereoPair"} {
		body := funcBody(t, text, fn)
		if body == "" {
			t.Fatalf("%s not found in zones_stereo.go; if it was renamed, update this test rather than deleting it", fn)
		}
		if !strings.Contains(body, "s.quietWake(ctx)") {
			t.Errorf("%s does not wake the master quietly, so forming a group there starts the speaker's last source", fn)
		}
		// The quiet wake is only for a speaker that is actually asleep; waking a
		// playing speaker "quietly" would mute something the user is listening to.
		if !strings.Contains(body, `np.Source == "STANDBY"`) {
			t.Errorf("%s calls the quiet wake without first checking the speaker is in standby", fn)
		}
		// And it has to happen BEFORE the plain wake, or the firmware has already
		// resumed by the time anything is muted.
		qi := strings.Index(body, "s.quietWake(ctx)")
		pi := strings.Index(body, "s.ensureBoxReadyErr(ctx)")
		if qi >= 0 && pi >= 0 && qi > pi {
			t.Errorf("%s wakes quietly only AFTER the plain wake, which is too late to stop the resume", fn)
		}
	}
}

// funcBody returns the source of the named method, from its signature to the
// closing brace at column 0.
func funcBody(t *testing.T, text, name string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^func \(s \*Server\) ` + regexp.QuoteMeta(name) + `\(`)
	loc := re.FindStringIndex(text)
	if loc == nil {
		return ""
	}
	rest := text[loc[0]:]
	if end := strings.Index(rest, "\n}\n"); end >= 0 {
		return rest[:end]
	}
	return rest
}
