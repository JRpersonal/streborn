package dlna

import (
	"os"
	"strings"
	"testing"
)

// #1890: one SSDP lookup on a busy network wrote ~100 per-response lines, and
// on a speaker they pushed everything else out of the 32 KB NAND log, the
// minute a bug report needed included. Each answer is DEBUG; the per-probe
// summary with its counts stays INFO.
func TestSSDPResponsesLogAtDebugAndTheSummaryAtInfo(t *testing.T) {
	cases := []struct{ file, want, not string }{
		{"dlna.go", `Logger.Debug("dlna: SSDP response"`, `Logger.Info("dlna: SSDP response"`},
		{"searchhost.go", `Logger.Debug("dlna: unicast SSDP response"`, `Logger.Info("dlna: unicast SSDP response"`},
		{"dlna.go", `Logger.Info("dlna: SSDP probe done"`, ""},
		{"searchhost.go", `Logger.Info("dlna: unicast M-SEARCH done"`, ""},
	}
	for _, c := range cases {
		b, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		if !strings.Contains(src, c.want) {
			t.Errorf("%s: missing %s", c.file, c.want)
		}
		if c.not != "" && strings.Contains(src, c.not) {
			t.Errorf("%s: still has %s", c.file, c.not)
		}
	}
}
