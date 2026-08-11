package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The sanitizers are the last line of defense before a user attaches a
// diagnostic bundle to a public GitHub issue. A regression here silently
// leaks real IPs / MACs / SSIDs / serials, so these tests assert both the
// exact replacement shape AND that no raw sensitive value survives.

func TestSanitizeLog_ScrubsAndLeavesNoRawSecrets(t *testing.T) {
	raw := strings.Join([]string{
		"connecting to 192.168.178.79 on wlan0",
		"link/ether a0:b1:c2:d3:e4:f5 brd ff:ff:ff:ff:ff:ff",
		"deviceID 0011223344AB selected",
		"ssid=MyHomeWifi psk=s3cr3tpass",
	}, "\n")
	got := string(sanitizeLog([]byte(raw)))

	for _, leaked := range []string{"192.168.178.79", "a0:b1:c2:d3:e4:f5", "0011223344AB", "MyHomeWifi", "s3cr3tpass"} {
		if strings.Contains(got, leaked) {
			t.Errorf("sanitized log still contains %q:\n%s", leaked, got)
		}
	}
	if !strings.Contains(got, "192.0.2.79") {
		t.Errorf("IP not masked to TEST-NET-3 with last octet preserved:\n%s", got)
	}
	if !strings.Contains(got, "MAC#") || !strings.Contains(got, "DEV#") || !strings.Contains(got, "<SSID-REDACTED>") {
		t.Errorf("expected MAC#/DEV#/SSID redaction markers:\n%s", got)
	}
}

func TestMaskIP(t *testing.T) {
	cases := map[string]string{
		"192.168.1.50": "192.0.2.50",
		"10.0.0.1":     "192.0.2.1",
		"not.an.ip":    "not.an.ip", // 4 dotted parts but non-numeric: left as-is by callers (regex won't match)
		"1.2.3":        "1.2.3",     // not 4 octets -> unchanged
	}
	for in, want := range cases {
		if got := maskIP(in); got != want {
			t.Errorf("maskIP(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHashShort(t *testing.T) {
	if hashShort("") != "" {
		t.Error("hashShort(\"\") must be empty")
	}
	a, b := hashShort("AA:BB:CC:DD:EE:FF"), hashShort("AA:BB:CC:DD:EE:FF")
	if a != b {
		t.Errorf("hashShort not deterministic: %q vs %q", a, b)
	}
	if len(a) != 8 {
		t.Errorf("hashShort length = %d, want 8", len(a))
	}
	if hashShort("AA:BB:CC:DD:EE:FF") == hashShort("11:22:33:44:55:66") {
		t.Error("distinct inputs hashed to the same value")
	}
}

func TestAnonymizeBoseInfoXML(t *testing.T) {
	xml := `<info deviceID="C8DF84ABCDEF">` +
		`<name>Living Room</name>` +
		`<macAddress>C8DF84ABCDEF</macAddress>` +
		`<serialNumber>071234567890AE00123</serialNumber>` +
		`<margeAccountUUID>abc-123-uuid</margeAccountUUID>` +
		`<networkInfo><ipAddress>192.168.4.21</ipAddress></networkInfo></info>`
	got := anonymizeBoseInfoXML(xml)

	for _, leaked := range []string{`Living Room`, `071234567890AE00123`, `abc-123-uuid`, `192.168.4.21`} {
		if strings.Contains(got, leaked) {
			t.Errorf("anonymized Bose info still contains %q:\n%s", leaked, got)
		}
	}
	for _, marker := range []string{`deviceID="DEV#`, `<name>NAME#`, `<macAddress>MAC#`, `<serialNumber>SERIAL#`, `<margeAccountUUID>MARGE#`, `192.0.2.21`} {
		if !strings.Contains(got, marker) {
			t.Errorf("expected marker %q in:\n%s", marker, got)
		}
	}
	if anonymizeBoseInfoXML("") != "" {
		t.Error("empty input must stay empty")
	}
}

// TestAnonymizeBoseSourcesXML pins both halves of the /sources pass, because
// both can fail silently: over-scrubbing leaves the bundle unable to say which
// inputs a box has (the reason the field exists), and under-scrubbing publishes
// somebody's streaming account on a GitHub issue.
func TestAnonymizeBoseSourcesXML(t *testing.T) {
	// Verbatim shapes from a SoundTouch 30 and a SoundTouch 10, plus the linked
	// Deezer and soundbar-socket entries we do not have hardware for.
	xml := `<sources deviceID="000C8A96488D">` +
		`<sourceItem source="AUX" sourceAccount="AUX" status="READY" isLocal="true" multiroomallowed="true">AUX IN</sourceItem>` +
		`<sourceItem source="PRODUCT" sourceAccount="TV" status="READY" isLocal="true" multiroomallowed="false">CBL-Sat</sourceItem>` +
		`<sourceItem source="BLUETOOTH" status="UNAVAILABLE" isLocal="true" multiroomallowed="true" />` +
		`<sourceItem source="QPLAY" sourceAccount="QPlay1UserName" status="UNAVAILABLE" isLocal="true" multiroomallowed="true">QPlay1UserName</sourceItem>` +
		`<sourceItem source="SPOTIFY" sourceAccount="SpotifyConnectUserName" status="UNAVAILABLE" isLocal="false" multiroomallowed="true">SpotifyConnectUserName</sourceItem>` +
		`<sourceItem source="DEEZER" sourceAccount="1456373802" status="READY" isLocal="false" multiroomallowed="true">DeezerUser</sourceItem>` +
		`<sourceItem source="PANDORA" sourceAccount="listener@example.com" status="READY" isLocal="false" multiroomallowed="true">listener@example.com</sourceItem>` +
		`</sources>`
	got := anonymizeBoseSourcesXML(xml)

	// The account id, the nickname that belongs to it, the address, and the
	// device id must all be gone.
	for _, leaked := range []string{`1456373802`, `DeezerUser`, `listener@example.com`, `000C8A96488D`} {
		if strings.Contains(got, leaked) {
			t.Errorf("anonymized /sources still contains %q:\n%s", leaked, got)
		}
	}
	// Everything that describes the box rather than its owner must survive.
	for _, kept := range []string{
		`source="AUX"`, `sourceAccount="AUX"`, `>AUX IN<`,
		`source="PRODUCT"`, `sourceAccount="TV"`, `>CBL-Sat<`,
		`source="BLUETOOTH"`, `isLocal="true"`, `status="UNAVAILABLE"`,
		`sourceAccount="QPlay1UserName"`, `sourceAccount="SpotifyConnectUserName"`,
	} {
		if !strings.Contains(got, kept) {
			t.Errorf("anonymized /sources dropped %q:\n%s", kept, got)
		}
	}
	if anonymizeBoseSourcesXML("") != "" {
		t.Error("empty input must stay empty")
	}
}

func TestLooksLikeAccountIdentity(t *testing.T) {
	personal := []string{"1456373802", "user@example.com", "31abcdefghijklmnop"}
	notPersonal := []string{"", "AUX", "AUX1", "TV", "CBL-Sat", "BD-DVD", "HDMI 1",
		"QPlay1UserName", "SpotifyConnectUserName", "AirPlay2DefaultUserName",
		"StoredMusicUserName"}
	for _, v := range personal {
		if !looksLikeAccountIdentity(v) {
			t.Errorf("%q should be treated as an account identity", v)
		}
	}
	for _, v := range notPersonal {
		if looksLikeAccountIdentity(v) {
			t.Errorf("%q must not be treated as an account identity", v)
		}
	}
}

func TestAnonymizeText(t *testing.T) {
	got := anonymizeText("box 192.168.0.5 mac de:ad:be:ef:00:11 ssid=Cafe")
	for _, leaked := range []string{"192.168.0.5", "de:ad:be:ef:00:11", "Cafe"} {
		if strings.Contains(got, leaked) {
			t.Errorf("anonymizeText leaked %q: %s", leaked, got)
		}
	}
}

// TestAnonymizeDebugState_ScrubsSSIDValues guards the leak found in the #592
// bundle: wlan_configured lists the speaker's stored networks as structured
// JSON, so each network name is a bare string value under an "ssid" key. The
// SSID hint pattern needs the word in the text itself, so those names travelled
// into a bundle attached to a public issue while the README promised they never
// leave the host. The scrub has to key on the FIELD, not on the value.
func TestAnonymizeDebugState_ScrubsSSIDValues(t *testing.T) {
	in := map[string]any{
		"wlan_configured": map[string]any{
			"tool": "BoseApp-Persistence",
			"networks": []any{
				map[string]any{"id": float64(0), "ssid": "2WIRE904", "current": false},
				map[string]any{"id": float64(1), "ssid": "SomeHouseholdName", "current": true},
			},
		},
		"wpa_supplicant": "network={\n\tssid=\"HomeNet\"\n\tpsk=\"hunter2\"\n}",
		"presets":        map[string]any{"1": "NDR 2"},
	}
	out := anonymizeDebugState(in)
	blob, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(blob)
	for _, leaked := range []string{"2WIRE904", "SomeHouseholdName", "HomeNet", "hunter2"} {
		if strings.Contains(got, leaked) {
			t.Errorf("anonymizeDebugState leaked %q:\n%s", leaked, got)
		}
	}
	// Everything that is not a secret must survive, or the bundle stops being
	// worth reading.
	for _, keep := range []string{"BoseApp-Persistence", "NDR 2"} {
		if !strings.Contains(got, keep) {
			t.Errorf("anonymizeDebugState dropped %q:\n%s", keep, got)
		}
	}
}

// TestAnonymizeText_ScrubsDeviceIDAndFriendlyName guards the leak found in the
// #187/#197 diagnostic bundles: the gabbo frames captured in the agent log /
// debug state carried the raw device ID and the user-chosen friendly name
// because anonymizeText only scrubbed IP/MAC/SSID. Both must now be hashed.
func TestAnonymizeText_ScrubsDeviceIDAndFriendlyName(t *testing.T) {
	raw := `<updates deviceID="B0D5CC04E5BF"><nameUpdated>ST-10-Firma 7ADB</nameUpdated></updates>` +
		` nowPlaying deviceID="68C90B85A0A9"`
	got := anonymizeText(raw)
	for _, leaked := range []string{"B0D5CC04E5BF", "68C90B85A0A9", "ST-10-Firma 7ADB"} {
		if strings.Contains(got, leaked) {
			t.Errorf("anonymizeText leaked %q:\n%s", leaked, got)
		}
	}
	if !strings.Contains(got, "DEV#") || !strings.Contains(got, "<nameUpdated>NAME#") {
		t.Errorf("expected DEV#/NAME# markers:\n%s", got)
	}
}

// TestAnonymizeSnapshot_ScrubsAgentFriendlyName guards the second leak: the
// /api/agent/version friendlyName ("Bose Wit") was copied into box-<n>.json
// verbatim because anonymizeSnapshot never touched STRAgentVer.
func TestAnonymizeSnapshot_ScrubsAgentFriendlyName(t *testing.T) {
	in := boxSnapshot{
		Host:        "192.168.1.50",
		STRAgentVer: map[string]any{"friendlyName": "Bose Wit", "model": "SoundTouch 20", "version": "v0.8.14"},
	}
	got := anonymizeSnapshot(in)
	if fn, _ := got.STRAgentVer["friendlyName"].(string); strings.Contains(fn, "Bose Wit") || !strings.HasPrefix(fn, "NAME#") {
		t.Errorf("friendlyName not hashed: %v", got.STRAgentVer["friendlyName"])
	}
	if got.STRAgentVer["model"] != "SoundTouch 20" {
		t.Errorf("model must be left intact, got %v", got.STRAgentVer["model"])
	}
}

// TestScrubPII_MasksUserHomePaths guards the third leak: OS account names
// (often the user's real first name) shipped verbatim in public bundles via
// log lines like "logFile=/Users/Dennis/Library/..." and
// "logFile=C:\Users\Rettcom\AppData\..." (seen in #270/#119 exports).
func TestScrubPII_MasksUserHomePaths(t *testing.T) {
	cases := []struct{ in, leaked string }{
		{`logFile=/Users/Dennis/Library/Application Support/STReborn/str.log`, "Dennis"},
		{`logFile=C:\Users\Rettcom\AppData\Local\STReborn\str.log`, "Rettcom"},
		{`"path":"C:\Users\Rettcom\AppData\Local\STReborn"`, "Rettcom"},
		{`config at /home/jensr/.config/streborn`, "jensr"},
	}
	for _, c := range cases {
		got := scrubPII(c.in)
		if strings.Contains(got, c.leaked) {
			t.Errorf("scrubPII leaked %q:\n in: %s\nout: %s", c.leaked, c.in, got)
		}
		if !strings.Contains(got, "<user>") {
			t.Errorf("scrubPII did not insert the <user> mask:\n in: %s\nout: %s", c.in, got)
		}
	}
}

// The bundle filename must self-identify (model, STR version, date) so an
// upload in a GitHub thread is readable without opening the zip - reporters
// were renaming bundles by hand to carry exactly this (issue #435).

func TestShortModel(t *testing.T) {
	cases := map[string]string{
		"SoundTouch 20":        "ST20",
		"SoundTouch 10":        "ST10",
		"SoundTouch 300":       "ST300",
		"SoundTouch Portable":  "Portable",
		"Bose SoundTouch 30":   "ST30",
		"Bose Wave SoundTouch": "WaveSoundTouch",
		"SoundTouch":           "SoundTouch",
		"":                     "",
		"weird/model name":     "weird-modelname",
	}
	for in, want := range cases {
		if got := shortModel(in); got != want {
			t.Errorf("shortModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFilenameToken(t *testing.T) {
	if got := filenameToken("v0.9.18"); got != "v0.9.18" {
		t.Errorf("version token mangled: %q", got)
	}
	if got := filenameToken("a b:c*d"); got != "a-b-c-d" {
		t.Errorf("unsafe chars not hyphenated: %q", got)
	}
	long := strings.Repeat("x", 40)
	if got := filenameToken(long); len(got) != 24 {
		t.Errorf("token not capped at 24: %d", len(got))
	}
}

func TestDiagnosticDefaultName_FallbackWithoutBoxes(t *testing.T) {
	now := time.Date(2026, 7, 25, 6, 26, 3, 0, time.UTC)
	got := diagnosticDefaultName(nil, now)
	want := "str-diagnostic-20260725-062603.zip"
	if got != want {
		t.Errorf("diagnosticDefaultName(nil) = %q, want %q", got, want)
	}
}
