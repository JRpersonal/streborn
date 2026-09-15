package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// PrimetimeFluff's SoundTouch 20, #854. An scm/spotty box on firmware 14.0.15
// from 2015 took the whole install, the app answered "install: OK", and the
// speaker then boot-looped into recovery and was unusable. The app had read
// "outdated=true" off that speaker minutes earlier and said nothing, because
// the firmware note was only ever appended to messages that report a FAILURE.
//
// The one case where the warning matters most is the one where the install
// appears to have worked.
func TestASuccessfulInstallStillNamesAnOutdatedFirmware(t *testing.T) {
	src := readInstallSrc(t)
	body := installStrBody(t, src)

	// Every message this function ends on has to carry the note. Anything
	// assigning a plain string literal to res.Message is one that does not.
	bare := regexp.MustCompile(`res\.Message = "`)
	if locs := bare.FindAllStringIndex(body, -1); len(locs) > 0 {
		var shown []string
		for _, l := range locs {
			end := l[1] + 70
			if end > len(body) {
				end = len(body)
			}
			shown = append(shown, body[l[0]:end])
		}
		t.Errorf("%d message(s) in InstallSTROnBox skip the firmware note:\n  %s",
			len(shown), strings.Join(shown, "\n  "))
	}
}

// The note itself must stay empty for a speaker on current firmware, or every
// successful install grows a paragraph nobody needs.
func TestTheNoteIsEmptyForACurrentSpeaker(t *testing.T) {
	src := readInstallSrc(t)
	if !strings.Contains(src, `fwNote := ""`) {
		t.Error("the firmware note no longer starts empty; a current speaker would get the outdated warning")
	}
	i := strings.Index(src, `fwNote = " The speaker firmware is "`)
	if i < 0 {
		t.Fatal("the firmware note text is gone; if it moved, move this test with it")
	}
	// It is only filled behind the outdated check.
	before := src[max(0, i-900):i]
	if !strings.Contains(before, "fw.Outdated") {
		t.Error("the note is filled without checking that the firmware is actually outdated")
	}
}

func readInstallSrc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("install_str.go")
	if err != nil {
		t.Fatalf("read install_str.go: %v", err)
	}
	return string(b)
}

// installStrBody is the InstallSTR function alone. RepairInstallViaSSH lives in
// the same file, reads no firmware of its own, and is deliberately not covered.
func installStrBody(t *testing.T, src string) string {
	t.Helper()
	i := strings.Index(src, "func (a *App) InstallSTROnBox(")
	if i < 0 {
		t.Fatal("InstallSTROnBox is gone; if it moved, move this test with it")
	}
	rest := src[i:]
	if j := strings.Index(rest, "\nfunc (a *App) boxHasResidualMargeUUID"); j > 0 {
		return rest[:j]
	}
	return rest
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
