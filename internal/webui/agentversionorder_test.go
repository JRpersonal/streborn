// The desktop app decides a speaker is ready to play by looking for the
// "version" key in the first bytes of /api/agent/version. The handler built a
// map, encoding/json sorts a map's keys, and every optional flag sorts BEFORE
// "version" and pushed it further right. Measured on live hardware 2026-09-25:
// a healthy speaker put the key at offset 419 to 435 of a 440 to 478 byte body,
// leaving 77 to 93 bytes of headroom against the app's 512-byte read. An
// OpenCloudTouch box emits conflictingMod (33 bytes) and foreignCloudURL (66)
// together, because both have the same cause, so the one speaker the flag was
// added to diagnose answered a body whose version key fell outside the window,
// and every play and preset copy to it was refused as "still starting".

package webui

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// theFlagsAnUnhealthyBoxEmits is the worst real combination, with the values
// measured on #986's ST30 and the lengths that broke the probe.
func theFlagsAnUnhealthyBoxEmits() map[string]string {
	return map[string]string{
		"version":              "v0.9.86",
		"build":                "2026-09-25-0552",
		"friendlyName":         "Wohnzimmer",
		"model":                "SoundTouch 30",
		"boxHealth":            "wedged",
		"boxHealthSinceSec":    "412",
		"boxHealthStrikes":     "2",
		"nandFreeBytes":        "6402048",
		"nandTotalBytes":       "27361280",
		"uptimeSec":            "706",
		"goLibrespot":          "present",
		"goLibrespotSha256":    strings.Repeat("6d", 32),
		"goLibrespotSizeBytes": "15990968",
		"agentBinarySha256":    strings.Repeat("44", 32),
		"engineHotSwap":        "true",
		"conflictingMod":       "OpenCloudTouch",
		"foreignCloudURL":      "margeServerUrl=http://content.api.bose.io:7777",
		"preset1036Storm":      "active",
		"preset1036Count":      "37",
		"preset1036SinceSec":   "1840",
		"wlanCredsMissing":     "true",
	}
}

func TestTheVersionKeyIsFirstWhateverElseTheBoxReports(t *testing.T) {
	rec := httptest.NewRecorder()
	writeAgentVersion(rec, theFlagsAnUnhealthyBoxEmits())
	body := rec.Body.String()

	at := strings.Index(body, `"version"`)
	if at != 1 {
		t.Errorf(`"version" starts at offset %d, want 1 (right after the brace); body: %s`, at, body[:80])
	}
	// The app reads a prefix. Whatever that prefix is, the key has to be in it.
	if at >= 512 {
		t.Errorf("the version key is outside the app's smallest read window (%d)", at)
	}
	// Payloads this size are exactly the case that broke: prove the body really
	// is bigger than the old window, so the test would have caught the bug.
	if len(body) <= 512 {
		t.Errorf("fixture too small (%d bytes) to exercise the truncation", len(body))
	}
}

func TestTheAnswerStillCarriesEveryFieldAndParses(t *testing.T) {
	in := theFlagsAnUnhealthyBoxEmits()
	rec := httptest.NewRecorder()
	writeAgentVersion(rec, in)

	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("the answer is not valid JSON: %v", err)
	}
	if len(out) != len(in) {
		t.Errorf("got %d fields, want %d", len(out), len(in))
	}
	for k, v := range in {
		if out[k] != v {
			t.Errorf("field %s = %q, want %q", k, out[k], v)
		}
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
}

// A value with a quote or a backslash in it must not break the hand-rolled
// object. friendlyName is user-supplied: people name speakers anything.
func TestAHostileSpeakerNameCannotBreakTheAnswer(t *testing.T) {
	rec := httptest.NewRecorder()
	writeAgentVersion(rec, map[string]string{
		"version":      "v0.9.86",
		"build":        "2026-09-25",
		"friendlyName": `Jens' "Küche" \ Wohnzimmer` + "\n\t",
	})
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("a quoted speaker name broke the JSON: %v", err)
	}
	if out["friendlyName"] != `Jens' "Küche" \ Wohnzimmer`+"\n\t" {
		t.Errorf("name round-tripped as %q", out["friendlyName"])
	}
}

// A box with nothing to report must still answer the two keys, and the order
// must not depend on the map iteration order, which Go randomises per run.
func TestTheOrderIsStableAcrossRuns(t *testing.T) {
	want := ""
	for i := 0; i < 20; i++ {
		rec := httptest.NewRecorder()
		writeAgentVersion(rec, theFlagsAnUnhealthyBoxEmits())
		got := rec.Body.String()
		if want == "" {
			want = got
			continue
		}
		if got != want {
			t.Fatalf("the answer changed between runs:\n%s\n%s", want[:100], got[:100])
		}
	}
}
