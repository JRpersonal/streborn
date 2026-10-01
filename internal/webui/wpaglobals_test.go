package webui

import (
	"strings"
	"testing"
)

// vendorTemplate is the shape of the conf measured on an ST10 (rhino/sm2) on
// 2026-09-30: thirteen global directives and NOT ONE network block, because the
// credential lives in NetManager's own store and is injected into the running
// supplicant. Values are placeholders; the names and the structure are real.
const vendorTemplate = `# Bose wpa_supplicant configuration
ctrl_interface=/var/run/wpa_supplicant
update_config=1
eapol_version=1
ap_scan=1
fast_reauth=1
disassoc_low_ack=1
driver_param=placeholder
device_name=SoundTouch
manufacturer=Bose Corporation
model_name=SoundTouch 10
model_number=placeholder
serial_number=placeholder
config_methods=virtual_push_button
`

// vendorGlobals is every directive that must survive a rewrite. Losing the six
// identity ones changes what the speaker advertises over WPS/P2P, and losing the
// radio ones changes how it scans and roams.
var vendorGlobals = []string{
	"ctrl_interface", "update_config", "eapol_version", "ap_scan", "fast_reauth",
	"disassoc_low_ack", "driver_param", "device_name", "manufacturer",
	"model_name", "model_number", "serial_number", "config_methods",
}

func TestBuildWPAConfigFromKeepsEveryVendorGlobal(t *testing.T) {
	got := buildWPAConfigFrom(vendorTemplate, "NewNet", "supersecret", false)

	for _, d := range vendorGlobals {
		if !strings.Contains(got, "\n"+d+"=") && !strings.HasPrefix(got, d+"=") {
			t.Errorf("global %q was dropped from the rewritten conf", d)
		}
	}
	if !strings.Contains(got, "manufacturer=Bose Corporation") {
		t.Error("a preserved global lost its value")
	}
	if !strings.Contains(got, "# Bose wpa_supplicant configuration") {
		t.Error("the leading comment was dropped; globals are meant to be verbatim")
	}
	if !strings.Contains(got, `ssid="NewNet"`) || !strings.Contains(got, `psk="supersecret"`) {
		t.Error("the chosen network is not in the conf")
	}
	if strings.Count(got, "network={") != 1 {
		t.Errorf("want exactly one network block, got %d", strings.Count(got, "network={"))
	}
}

func TestBuildWPAConfigFromKeepsTheVendorCtrlInterfaceForm(t *testing.T) {
	got := buildWPAConfigFrom(vendorTemplate, "NewNet", "pw12345678", false)

	// Bose writes the bare-path form and wpa_supplicant accepts it. Preserving
	// it is the point: STR's DIR=/GROUP= spelling must not be added on top,
	// because two ctrl_interface lines are a conf wpa_supplicant may reject.
	if n := strings.Count(got, "ctrl_interface="); n != 1 {
		t.Fatalf("want exactly one ctrl_interface line, got %d\n%s", n, got)
	}
	if !strings.Contains(got, "ctrl_interface=/var/run/wpa_supplicant\n") {
		t.Error("the speaker's own ctrl_interface line was replaced instead of kept")
	}
	if n := strings.Count(got, "update_config="); n != 1 {
		t.Errorf("want exactly one update_config line, got %d", n)
	}
}

func TestBuildWPAConfigFromAddsWhatTheConfLacks(t *testing.T) {
	// A conf with globals but neither of the two directives STR depends on.
	got := buildWPAConfigFrom("ap_scan=1\ndevice_name=SoundTouch\n", "NewNet", "pw12345678", false)

	if !strings.Contains(got, wpaCtrlInterfaceLine) {
		t.Error("no ctrl_interface line at all: wpa_cli would have no socket to talk to")
	}
	if !strings.Contains(got, "update_config=1") {
		t.Error("update_config missing: the M4 save_config fallback needs it")
	}
	if !strings.Contains(got, "ap_scan=1") || !strings.Contains(got, "device_name=SoundTouch") {
		t.Error("the globals that WERE there got lost")
	}
}

func TestBuildWPAConfigFromReplacesOldNetworkBlocks(t *testing.T) {
	// The second switch in a row reads STR's own previous write. Blocks must be
	// replaced, never accumulated: dead networks first is the documented way to
	// break Wi-Fi on these speakers.
	previous := buildWPAConfigFrom(vendorTemplate, "OldNet", "oldpassword", true)
	got := buildWPAConfigFrom(previous, "NewNet", "newpassword", false)

	if strings.Count(got, "network={") != 1 {
		t.Fatalf("network blocks accumulated: %d\n%s", strings.Count(got, "network={"), got)
	}
	if strings.Contains(got, "OldNet") || strings.Contains(got, "oldpassword") {
		t.Error("the previous network survived the rewrite")
	}
	if strings.Contains(got, "scan_ssid=1") {
		t.Error("the previous block's hidden flag leaked into a non-hidden switch")
	}
	for _, d := range vendorGlobals {
		if !strings.Contains(got, d+"=") {
			t.Errorf("global %q lost on the SECOND rewrite", d)
		}
	}
}

func TestBuildWPAConfigFromFallsBackWithNothingToPreserve(t *testing.T) {
	want := buildWPAConfig("NewNet", "pw12345678", false)

	for name, existing := range map[string]string{
		"unreadable conf":   "",
		"only whitespace":   "\n\n   \n",
		"only a network":    "network={\n    ssid=\"OldNet\"\n    psk=\"oldpassword\"\n}\n",
		"only two networks": "network={\n    ssid=\"A\"\n}\nnetwork={\n    ssid=\"B\"\n}\n",
	} {
		if got := buildWPAConfigFrom(existing, "NewNet", "pw12345678", false); got != want {
			t.Errorf("%s: want the minimal fallback, got:\n%s", name, got)
		}
	}
}

func TestWpaGlobalLinesBlockHandling(t *testing.T) {
	// A cred block is not ours to touch, and a network block written on one line
	// must not swallow everything after it.
	in := strings.Join([]string{
		"ap_scan=1",
		"cred={",
		"    realm=\"example\"",
		"}",
		"network={ ssid=\"OneLiner\" }",
		"device_name=SoundTouch",
		"network={",
		"    ssid=\"Multi\"",
		"}",
		"fast_reauth=1",
	}, "\n")

	got := wpaGlobalLines(in)
	joined := strings.Join(got, "\n")

	for _, want := range []string{"ap_scan=1", "cred={", `realm="example"`, "device_name=SoundTouch", "fast_reauth=1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("kept lines lost %q:\n%s", want, joined)
		}
	}
	for _, unwanted := range []string{"OneLiner", "Multi"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("a network block leaked into the globals (%q):\n%s", unwanted, joined)
		}
	}
	// The cred block's closing brace must still be there; dropping it would
	// produce a conf wpa_supplicant cannot parse at all.
	if strings.Count(joined, "}") != 1 {
		t.Errorf("want the cred block's brace kept exactly once, got %d:\n%s", strings.Count(joined, "}"), joined)
	}
}

func TestWpaGlobalLinesHandlesCRLF(t *testing.T) {
	got := wpaGlobalLines("ap_scan=1\r\nnetwork={\r\n    ssid=\"X\"\r\n}\r\ndevice_name=ST\r\n")

	for _, l := range got {
		if strings.Contains(l, "\r") {
			t.Errorf("a carriage return survived into %q, which would end up inside a directive value", l)
		}
	}
	if strings.Join(got, "|") != "ap_scan=1|device_name=ST" {
		t.Errorf("CRLF conf parsed wrong: %q", got)
	}
}

func TestReloadWPAInPlaceReportsWhenItCannot(t *testing.T) {
	// No wpa_cli anywhere on PATH means nothing was done, and the caller has to
	// escalate rather than wait twelve seconds for a reload that never happened.
	t.Setenv("PATH", t.TempDir())
	if reloadWPAInPlace("wlan0") {
		t.Error("reloadWPAInPlace claimed success with no wpa_cli on PATH")
	}
}

func TestBuildWPAConfigFromKeepsNoSecretOutsideItsBlock(t *testing.T) {
	// Whatever else changes, the passphrase belongs in the network block and
	// nowhere else: a copy in a preserved global would be a second plaintext
	// location nobody knows to redact.
	got := buildWPAConfigFrom(vendorTemplate, "NewNet", "supersecret", false)
	before, _, found := strings.Cut(got, "network={")
	if !found {
		t.Fatal("no network block")
	}
	if strings.Contains(before, "supersecret") {
		t.Errorf("the passphrase appears in the globals:\n%s", before)
	}
}
