package anonymise

import (
	"regexp"
	"strings"
	"testing"
)

var tagShape = regexp.MustCompile(`SSID#[0-9a-f]{8}`)

// Network names become stable, distinct tags instead of one shared marker, so a
// bundle can say which stored network is the live one and whether two speakers
// share a network (support case 2026-10-14).
func TestSSIDTag_StableDistinctAndNotTheName(t *testing.T) {
	SetSalt([]byte("test-salt"))
	defer SetSalt(nil)
	a, b := SSIDTag("HomeNet"), SSIDTag("Vodafone Homespot")
	if !tagShape.MatchString(a) || !tagShape.MatchString(b) {
		t.Fatalf("tags %q %q do not have the SSID#<8 hex> shape", a, b)
	}
	if a == b {
		t.Fatal("two networks share a tag")
	}
	if a != SSIDTag("HomeNet") {
		t.Fatal("the tag is not stable")
	}
	if strings.Contains(a, "HomeNet") {
		t.Fatal("the tag carries the name")
	}
	// A tag fed back in stays the same tag.
	if SSIDTag(a) != a {
		t.Fatal("tagging a tag produced a new tag")
	}
}

func TestRedactSSIDs_TagsNamesKeepsSecretsRedacted(t *testing.T) {
	SetSalt([]byte("test-salt"))
	defer SetSalt(nil)
	home := SSIDTag("Home Net 5G")
	cases := []struct{ in, want string }{
		{`<profile ssid="Home Net 5G" password="hunter two" />`, `<profile ssid="` + home + `" ` + ssidRedacted + ` />`},
		{`ssid=HomeNet psk=s3cr3t`, `ssid=` + SSIDTag("HomeNet") + ` ` + ssidRedacted},
		{`seeding 'Home Net 5G' so a later`, `seeding '` + home + `' so a later`},
		{`wantTag=SSID#1a2b3c4d storeTags=[SSID#0f0f0f0f]`, `wantTag=SSID#1a2b3c4d storeTags=[SSID#0f0f0f0f]`},
		{`"ssidTag": "SSID#1a2b3c4d"`, `"ssidTag": "SSID#1a2b3c4d"`},
		// The older agent spelling survives too: it is already a salted hash.
		{`"ssidTag": "ssid:1a2b3c4d"`, `"ssidTag": "ssid:1a2b3c4d"`},
	}
	for _, c := range cases {
		got := redactSSIDs(c.in)
		if got != c.want {
			t.Errorf("redactSSIDs(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
		if again := redactSSIDs(got); again != got {
			t.Errorf("not idempotent: %q -> %q", got, again)
		}
	}
}

// The structured walk tags a network name under an "ssid" key and still drops
// a secret under a secret key.
func TestDebugState_SSIDKeyBecomesATag(t *testing.T) {
	SetSalt([]byte("test-salt"))
	defer SetSalt(nil)
	in := map[string]any{
		"wlan": map[string]any{
			"networks": []any{
				map[string]any{"ssid": "HomeNet", "current": true},
				map[string]any{"ssid": "OldNet", "current": false},
				map[string]any{"ssid": "<REDACTED>", "ssidTag": "SSID#1a2b3c4d"},
			},
			"psk": "placeholder-key",
		},
	}
	out := DebugState(in)
	w := out["wlan"].(map[string]any)
	nets := w["networks"].([]any)
	first := nets[0].(map[string]any)["ssid"]
	second := nets[1].(map[string]any)["ssid"]
	if first != SSIDTag("HomeNet") || second != SSIDTag("OldNet") || first == second {
		t.Fatalf("ssid values = %v, %v", first, second)
	}
	third := nets[2].(map[string]any)
	if third["ssid"] != "<REDACTED>" || third["ssidTag"] != "SSID#1a2b3c4d" {
		t.Fatalf("an agent-redacted entry was changed: %v", third)
	}
	if w["psk"] != "<REDACTED>" {
		t.Fatalf("psk = %v", w["psk"])
	}
}
