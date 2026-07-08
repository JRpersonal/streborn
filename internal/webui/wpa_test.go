package webui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEscapeWPAValue(t *testing.T) {
	cases := map[string]string{
		`plain`:        `plain`,
		`a"b`:          `a\"b`,
		`a\b`:          `a\\b`,
		"line1\nline2": `line1\nline2`,
		"tab\there":    `tab\there`,
		"cr\rhere":     `cr\rhere`,
	}
	for in, want := range cases {
		if got := escapeWPAValue(in); got != want {
			t.Errorf("escapeWPAValue(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildWPAConfig(t *testing.T) {
	// WPA network: psk + key_mgmt=WPA-PSK, single network block.
	wpa := buildWPAConfig("MyNet", "supersecret", false)
	for _, want := range []string{`ssid="MyNet"`, `psk="supersecret"`, "key_mgmt=WPA-PSK", "update_config=1"} {
		if !strings.Contains(wpa, want) {
			t.Errorf("wpa config missing %q:\n%s", want, wpa)
		}
	}
	if strings.Count(wpa, "network={") != 1 {
		t.Errorf("expected exactly one network block:\n%s", wpa)
	}
	// Non-hidden networks must NOT probe for the SSID (scan_ssid=1 leaks the
	// SSID in probe requests and slows scans, so it is hidden-only).
	if strings.Contains(wpa, "scan_ssid") {
		t.Errorf("non-hidden network must not set scan_ssid:\n%s", wpa)
	}

	// Open network (empty password): key_mgmt=NONE, no psk line.
	open := buildWPAConfig("OpenNet", "", false)
	if !strings.Contains(open, "key_mgmt=NONE") || strings.Contains(open, "psk=") {
		t.Errorf("open network must be key_mgmt=NONE with no psk:\n%s", open)
	}

	// A quote in the SSID must be escaped, not break the block.
	q := buildWPAConfig(`He"llo`, "12345678", false)
	if !strings.Contains(q, `ssid="He\"llo"`) {
		t.Errorf("ssid quote not escaped:\n%s", q)
	}
}

func TestBuildWPAConfigHidden(t *testing.T) {
	// Hidden network: scan_ssid=1 must land INSIDE the single network block so
	// wpa_supplicant probes for the SSID directly (a hidden AP never carries
	// the SSID in its beacons).
	h := buildWPAConfig("Stealth", "supersecret", true)
	if strings.Count(h, "network={") != 1 {
		t.Fatalf("expected exactly one network block:\n%s", h)
	}
	open := strings.Index(h, "network={")
	closing := strings.LastIndex(h, "}")
	scan := strings.Index(h, "scan_ssid=1")
	if scan < 0 {
		t.Fatalf("hidden network config missing scan_ssid=1:\n%s", h)
	}
	if scan < open || scan > closing {
		t.Errorf("scan_ssid=1 must be inside the network block:\n%s", h)
	}
}

// TestWlanPreflightApplies pins the pre-flight gating: hidden networks are
// invisible to the box's site survey by design, so requesting one must skip
// the visibility check exactly like an explicit force override. Otherwise a
// hidden SSID would be refused with "ssid-not-visible" forever.
func TestWlanPreflightApplies(t *testing.T) {
	cases := []struct {
		force, hidden, want bool
	}{
		{false, false, true},
		{true, false, false},
		{false, true, false},
		{true, true, false},
	}
	for _, c := range cases {
		if got := wlanPreflightApplies(c.force, c.hidden); got != c.want {
			t.Errorf("wlanPreflightApplies(force=%v, hidden=%v) = %v, want %v", c.force, c.hidden, got, c.want)
		}
	}
}

// TestWriteWlanCredsFile pins the NAND wlan-creds format that run.sh's boot
// replay parses: SSID=/PASS= always, HIDDEN=1 only for hidden networks.
func TestWriteWlanCredsFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "wlan-creds")

	if err := writeWlanCredsFile(p, "MyNet", "supersecret", false); err != nil {
		t.Fatalf("writeWlanCredsFile: %v", err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("reading wlan-creds: %v", err)
	}
	if string(got) != "SSID=MyNet\nPASS=supersecret\n" {
		t.Errorf("non-hidden wlan-creds mismatch:\n%s", got)
	}

	if err := writeWlanCredsFile(p, "Stealth", "supersecret", true); err != nil {
		t.Fatalf("writeWlanCredsFile hidden: %v", err)
	}
	got, err = os.ReadFile(p)
	if err != nil {
		t.Fatalf("reading wlan-creds: %v", err)
	}
	if string(got) != "SSID=Stealth\nPASS=supersecret\nHIDDEN=1\n" {
		t.Errorf("hidden wlan-creds mismatch:\n%s", got)
	}
}

// TestWriteWPAConfAtDirect covers the writable-target path: a direct write must
// succeed, report "direct", and land the exact content. This is the regression
// guard for the read-only-/etc fix — the runtime switch must actually write the
// conf rather than abort on a backup failure.
func TestWriteWPAConfAtDirect(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "wpa_supplicant.conf")
	tmp := filepath.Join(dir, "wpa.str")
	content := buildWPAConfig("MyNet", "supersecret", false)

	method, err := writeWPAConfAt(conf, tmp, content)
	if err != nil {
		t.Fatalf("writeWPAConfAt returned error on writable target: %v", err)
	}
	if method != "direct" {
		t.Errorf("method = %q, want %q", method, "direct")
	}
	got, err := os.ReadFile(conf)
	if err != nil {
		t.Fatalf("reading written conf: %v", err)
	}
	if string(got) != content {
		t.Errorf("written conf mismatch:\ngot:\n%s\nwant:\n%s", got, content)
	}
}
