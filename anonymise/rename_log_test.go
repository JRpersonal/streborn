package anonymise

import (
	"strings"
	"testing"
)

// The rename lines added for the "speaker turned up under another room's name"
// report must leave the bundle with both names hashed, the old and the new one.
func TestRenameLogLinesHashBothNames(t *testing.T) {
	for _, line := range []string{
		`time=2026-11-15T09:40:00.000+01:00 level=INFO msg="speaker name changed on request" old=Wohnzimmer new="Schlafzimmer 9550" from=192.0.2.5:51234 ua="Mozilla/5.0"`,
		`time=2026-11-15T09:40:00.000+01:00 level=INFO msg="speaker name changed from the app" host=192.0.2.24 new="Schlafzimmer 9550"`,
	} {
		out := scrubIdentities(line)
		for _, leak := range []string{"Wohnzimmer", "Schlafzimmer"} {
			if strings.Contains(out, leak) {
				t.Errorf("name %q survived: %s", leak, out)
			}
		}
		if !strings.Contains(out, "NAME#") {
			t.Errorf("no name token in %s", out)
		}
	}
}
