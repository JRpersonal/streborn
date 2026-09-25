// The play-readiness probe read 512 bytes of /api/agent/version and decided on
// strings.Contains(body, `"version"`). The agent marshalled a map, map keys are
// sorted, and every optional flag sorted before "version" and pushed it right.
// A box carrying the conflicting-mod and foreign-cloud-URL flags together put
// the key past 512, the probe never matched, and every play and preset copy to
// that speaker was refused as "still starting".

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTheReadinessProbeAcceptsAFlagHeavyAnswer(t *testing.T) {
	// The shape that broke it: every optional flag an unhealthy box emits,
	// with "version" no longer first (the old, sorted-map wire order).
	fields := map[string]string{
		"agentBinarySha256":    strings.Repeat("44", 32),
		"boxHealth":            "wedged",
		"build":                "2026-09-25-0552",
		"conflictingMod":       "OpenCloudTouch",
		"engineHotSwap":        "true",
		"foreignCloudURL":      "margeServerUrl=http://content.api.bose.io:7777",
		"friendlyName":         "Wohnzimmer",
		"goLibrespot":          "present",
		"goLibrespotSha256":    strings.Repeat("6d", 32),
		"goLibrespotSizeBytes": "15990968",
		"model":                "SoundTouch 30",
		"nandFreeBytes":        "6402048",
		"nandTotalBytes":       "27361280",
		"preset1036Count":      "37",
		"preset1036SinceSec":   "1840",
		"preset1036Storm":      "active",
		"uptimeSec":            "706",
		"version":              "v0.9.86",
	}
	body, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= 512 {
		t.Fatalf("fixture is only %d bytes, too small to exercise the truncation", len(body))
	}
	if !agentVersionAnswered(body) {
		t.Error("a flag-heavy but perfectly healthy answer was read as not ready")
	}
}

func TestTheProbeStillRejectsWhatIsNotAnAgent(t *testing.T) {
	cases := map[string][]byte{
		"empty":                    []byte(""),
		"bare 400 from the box":    []byte("400 Bad Request"),
		"stock firmware HTML":      []byte("<!DOCTYPE html><html><body>Not found</body></html>"),
		"an object without it":     []byte(`{"status":"ok"}`),
		"an empty version":         []byte(`{"version":""}`),
		"a whitespace version":     []byte(`{"version":"   "}`),
		"truncated JSON":           []byte(`{"version":"v0.9.8`),
		"a list, not an object":    []byte(`["version"]`),
		"the key only as a string": []byte(`"version"`),
	}
	for name, body := range cases {
		if agentVersionAnswered(body) {
			t.Errorf("%s was accepted as a ready agent", name)
		}
	}
}

func TestTheProbeAcceptsTheSmallestRealAnswer(t *testing.T) {
	if !agentVersionAnswered([]byte(`{"version":"v0.9.86","build":"2026-09-25"}`)) {
		t.Error("the minimal real answer was rejected")
	}
}
