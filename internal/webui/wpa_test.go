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
	wpa := buildWPAConfig("MyNet", "supersecret")
	for _, want := range []string{`ssid="MyNet"`, `psk="supersecret"`, "key_mgmt=WPA-PSK", "update_config=1"} {
		if !strings.Contains(wpa, want) {
			t.Errorf("wpa config missing %q:\n%s", want, wpa)
		}
	}
	if strings.Count(wpa, "network={") != 1 {
		t.Errorf("expected exactly one network block:\n%s", wpa)
	}

	// Open network (empty password): key_mgmt=NONE, no psk line.
	open := buildWPAConfig("OpenNet", "")
	if !strings.Contains(open, "key_mgmt=NONE") || strings.Contains(open, "psk=") {
		t.Errorf("open network must be key_mgmt=NONE with no psk:\n%s", open)
	}

	// A quote in the SSID must be escaped, not break the block.
	q := buildWPAConfig(`He"llo`, "12345678")
	if !strings.Contains(q, `ssid="He\"llo"`) {
		t.Errorf("ssid quote not escaped:\n%s", q)
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
	content := buildWPAConfig("MyNet", "supersecret")

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
