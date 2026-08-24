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
	// WPA network: psk + key_mgmt=WPA-PSK, single network block. priority=10
	// is load-bearing (#697): NetManager injects its stored profiles into the
	// running supplicant at priority 1, and without an explicit higher rank
	// STR's block lost the selection and the box slid back to the old network.
	wpa := buildWPAConfig("MyNet", "supersecret", false)
	for _, want := range []string{`ssid="MyNet"`, `psk="supersecret"`, "key_mgmt=WPA-PSK", "update_config=1", "priority=10"} {
		if !strings.Contains(wpa, want) {
			t.Errorf("wpa config missing %q:\n%s", want, wpa)
		}
	}
	// The priority must sit INSIDE the network block (a global priority line
	// is meaningless to wpa_supplicant).
	if open, closing, prio := strings.Index(wpa, "network={"), strings.LastIndex(wpa, "}"), strings.Index(wpa, "priority=10"); prio < open || prio > closing {
		t.Errorf("priority=10 must be inside the network block:\n%s", wpa)
	}
	if strings.Count(wpa, "network={") != 1 {
		t.Errorf("expected exactly one network block:\n%s", wpa)
	}
	// Non-hidden networks must NOT probe for the SSID (scan_ssid=1 leaks the
	// SSID in probe requests and slows scans, so it is hidden-only).
	if strings.Contains(wpa, "scan_ssid") {
		t.Errorf("non-hidden network must not set scan_ssid:\n%s", wpa)
	}

	// Open network (empty password): key_mgmt=NONE, no psk line, and the same
	// winning priority (an open network is outranked by an injected profile
	// just the same).
	open := buildWPAConfig("OpenNet", "", false)
	if !strings.Contains(open, "key_mgmt=NONE") || strings.Contains(open, "psk=") {
		t.Errorf("open network must be key_mgmt=NONE with no psk:\n%s", open)
	}
	if !strings.Contains(open, "priority=10") {
		t.Errorf("open network config missing priority=10:\n%s", open)
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

// TestWpaAddNetworkVia pins the M4 fallback's command sequence, in particular
// that the added network gets an explicit priority BEFORE enable/select and
// that save_config comes last, so the persisted config carries the winning
// rank (#697: without the priority the added block sat at 0 and the profile
// NetManager injected at 1 stayed [CURRENT]).
func TestWpaAddNetworkVia(t *testing.T) {
	var calls [][]string
	run := func(args ...string) string {
		calls = append(calls, args)
		if args[0] == "add_network" {
			return "3"
		}
		return "OK"
	}
	if !wpaAddNetworkVia(run, "MyNet", "supersecret", false) {
		t.Fatal("wpaAddNetworkVia returned false on a healthy sequence")
	}
	find := func(want ...string) int {
		for i, c := range calls {
			if len(c) != len(want) {
				continue
			}
			match := true
			for j := range c {
				if c[j] != want[j] {
					match = false
					break
				}
			}
			if match {
				return i
			}
		}
		t.Fatalf("call %v missing in %v", want, calls)
		return -1
	}
	prio := find("set_network", "3", "priority", "10")
	enable := find("enable_network", "3")
	sel := find("select_network", "3")
	save := find("save_config")
	if prio > enable || prio > sel {
		t.Errorf("priority must be set before enable/select: %v", calls)
	}
	if save != len(calls)-1 {
		t.Errorf("save_config must be the last call: %v", calls)
	}

	// A failed add_network aborts the sequence before any set_network.
	calls = nil
	failRun := func(args ...string) string {
		calls = append(calls, args)
		return "FAIL"
	}
	if wpaAddNetworkVia(failRun, "MyNet", "supersecret", false) {
		t.Error("wpaAddNetworkVia must return false when add_network fails")
	}
	if len(calls) != 1 {
		t.Errorf("no commands may follow a failed add_network: %v", calls)
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
