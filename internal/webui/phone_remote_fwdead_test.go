package webui

import (
	"regexp"
	"strings"
	"testing"
)

// The phone remote says "unplug the speaker" when the agent reports boxHealth
// firmware-unresponsive (the speaker's own Bose software stopped answering,
// boseapp_health.go), in every language the page speaks, and gives a Portable
// (batteryPowered) its AUX restart instead, because its battery stays in.
func TestPhoneRemoteFirmwareUnresponsiveCard(t *testing.T) {
	page := readIndexHTML(t)
	for _, want := range []string{
		`id="fwDeadCard"`, `id="lblFwDead"`, `id="fwDeadText"`,
		`set('lblFwDead', T.fwDeadTitle);`,
		`av.boxHealth === 'firmware-unresponsive'`,
		`av.batteryPowered === 'true' ? T.fwDeadTextPortable : T.fwDeadText`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("index.html misses %s", want)
		}
	}

	start := strings.Index(page, "var I18N2 = {")
	if start < 0 {
		t.Fatal("I18N2 dictionary not found")
	}
	end := strings.Index(page[start:], "\n};")
	lang := regexp.MustCompile(`^  ([a-z]{2}):\{`)
	dash := regexp.MustCompile(`[\x{2013}\x{2014}]| - `)
	seen := 0
	for _, line := range strings.Split(page[start:start+end], "\n") {
		m := lang.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		seen++
		for _, k := range []string{"fwDeadTitle", "fwDeadText", "fwDeadTextPortable"} {
			v := regexp.MustCompile(k + `:"([^"]*)"`).FindStringSubmatch(line)
			if v == nil || !strings.Contains(v[1], "Bose") {
				t.Errorf("language %q: %s missing or does not name Bose", m[1], k)
				continue
			}
			if dash.MatchString(v[1]) {
				t.Errorf("language %q: %s uses a dash as punctuation: %q", m[1], k, v[1])
			}
			if k == "fwDeadTextPortable" && !strings.Contains(v[1], "AUX") {
				t.Errorf("language %q: the Portable text does not name AUX: %q", m[1], v[1])
			}
		}
	}
	if seen < 12 {
		t.Errorf("only %d languages in I18N2", seen)
	}
	if !strings.Contains(page, `fwDeadText:"Die Bose-Software des Lautsprechers reagiert nicht mehr. Zieh den Stecker für etwa 30 Sekunden und steck ihn wieder ein."`) {
		t.Error("the German text is missing or changed")
	}
}
