package main

import (
	"strings"
	"testing"
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

func TestAnonymizeText(t *testing.T) {
	got := anonymizeText("box 192.168.0.5 mac de:ad:be:ef:00:11 ssid=Cafe")
	for _, leaked := range []string{"192.168.0.5", "de:ad:be:ef:00:11", "Cafe"} {
		if strings.Contains(got, leaked) {
			t.Errorf("anonymizeText leaked %q: %s", leaked, got)
		}
	}
}
