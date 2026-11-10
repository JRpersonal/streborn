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
