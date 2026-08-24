package webui

// Tests for the NetworkProfiles.xml priority assertion (#697).
//
// The document under test mirrors the store from the #697 report: two
// profiles survive a deliberate switch, the OLD network ranked higher
// (priority="1" vs "0"), tags wrapped across lines, the SSID attribute
// uppercase, and ciphertext in passphrase/wepKey that must survive byte for
// byte. The values here are placeholders, not captured material.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const reporterProfileStore = `<?xml version="1.0" encoding="UTF-8"?>
<WiFiProfiles>
  <profile SSID="WLAN10"
           priority="1" security="wpa_or_wpa2"
           passphrase="cGxhY2Vob2xkZXItY2lwaGVydGV4dA==" wepKey="cGxhY2Vob2xkZXItaXY="
           encrypted="true" lastConnected="1755900000" />
  <profile SSID="WLAN50"
           priority="0" security="wpa_or_wpa2"
           passphrase="YW5vdGhlci1wbGFjZWhvbGRlcg==" wepKey="c2Vjb25kLXBsYWNlaG9sZGVy"
           encrypted="true" lastConnected="1755990000" />
</WiFiProfiles>
`

// TestSetProfilePrioritiesReporterState is the #697 regression: after a
// deliberate switch to WLAN50 the store must rank WLAN50 strictly highest and
// change NOTHING but the two priority values. The expected output is built
// from the input by exactly those two replacements, so any other byte
// changing (ciphertext, whitespace, attribute order) fails the test.
func TestSetProfilePrioritiesReporterState(t *testing.T) {
	out, found, changed := setProfilePriorities(reporterProfileStore, "WLAN50")
	if !found {
		t.Fatalf("WLAN50 profile not found in store")
	}
	if !changed {
		t.Fatalf("expected a rewrite, got changed=false")
	}
	want := strings.Replace(reporterProfileStore, `priority="1"`, `priority="0"`, 1)
	want = strings.Replace(want, `priority="0" security="wpa_or_wpa2"
           passphrase="YW5vdGhlci1wbGFjZWhvbGRlcg=="`, `priority="10" security="wpa_or_wpa2"
           passphrase="YW5vdGhlci1wbGFjZWhvbGRlcg=="`, 1)
	if out != want {
		t.Errorf("rewrite mismatch:\ngot:\n%s\nwant:\n%s", out, want)
	}

	// Idempotent: asserting the same choice again must not rewrite (repeat
	// switches to the same network must not wear the NAND).
	again, found2, changed2 := setProfilePriorities(out, "WLAN50")
	if !found2 || changed2 {
		t.Errorf("second assert: found=%v changed=%v, want found=true changed=false", found2, changed2)
	}
	if again != out {
		t.Errorf("second assert altered the document:\n%s", again)
	}
}

// TestSetProfilePrioritiesTargetAbsent covers the first switch to a network
// the firmware never stored: the profile cannot be added agent-side (its
// passphrase must be NetManager's own ciphertext), so the existing profiles
// are demoted to 0 and found=false tells the caller the add is still owed by
// the firmware's boot path.
func TestSetProfilePrioritiesTargetAbsent(t *testing.T) {
	out, found, changed := setProfilePriorities(reporterProfileStore, "WLAN99")
	if found {
		t.Fatalf("WLAN99 must not be found")
	}
	if !changed {
		t.Fatalf("the priority=1 profile must be demoted")
	}
	if strings.Contains(out, `priority="1"`) {
		t.Errorf("old profile still ranked above the firmware default:\n%s", out)
	}
	if got := strings.Count(out, `priority="0"`); got != 2 {
		t.Errorf("expected both profiles at priority 0, got %d:\n%s", got, out)
	}

	// All profiles already at 0: nothing to demote, no write.
	_, found2, changed2 := setProfilePriorities(out, "WLAN99")
	if found2 || changed2 {
		t.Errorf("re-run on demoted store: found=%v changed=%v, want false/false", found2, changed2)
	}
}

// TestSetProfilePrioritiesInsertsMissingAttr: a chosen profile that carries no
// priority attribute gets an explicit one, and a lowercase ssid attribute name
// still matches (the attribute NAME casing varies, the VALUE is compared
// exactly).
func TestSetProfilePrioritiesInsertsMissingAttr(t *testing.T) {
	doc := `<WiFiProfiles><profile ssid="HomeNet" encrypted="true" /></WiFiProfiles>`
	out, found, changed := setProfilePriorities(doc, "HomeNet")
	if !found || !changed {
		t.Fatalf("found=%v changed=%v, want true/true", found, changed)
	}
	if !strings.Contains(out, `<profile priority="10" ssid="HomeNet"`) {
		t.Errorf("priority attribute not inserted on the chosen profile:\n%s", out)
	}

	// SSID values are case-sensitive: a different casing is a different network.
	_, found, _ = setProfilePriorities(doc, "homenet")
	if found {
		t.Errorf("SSID value match must be case-sensitive")
	}
}

// TestSetProfilePrioritiesEscapedSSID: the store holds XML-escaped attribute
// values, the switch request holds what the user typed.
func TestSetProfilePrioritiesEscapedSSID(t *testing.T) {
	doc := `<WiFiProfiles>
  <profile SSID="Caf&amp;e &gt;5GHz&lt;" priority="0" />
  <profile SSID="Other" priority="1" />
</WiFiProfiles>`
	out, found, changed := setProfilePriorities(doc, `Caf&e >5GHz<`)
	if !found || !changed {
		t.Fatalf("found=%v changed=%v, want true/true", found, changed)
	}
	if !strings.Contains(out, `SSID="Caf&amp;e &gt;5GHz&lt;" priority="10"`) {
		t.Errorf("escaped-SSID profile not raised:\n%s", out)
	}
	if !strings.Contains(out, `SSID="Other" priority="0"`) {
		t.Errorf("other profile not demoted:\n%s", out)
	}
}

// TestSetProfilePrioritiesRawGtInValue: an attribute value may legally contain
// '>', and an SSID is arbitrary bytes, so the tag scanner must not mistake a
// quoted '>' for the end of the tag.
func TestSetProfilePrioritiesRawGtInValue(t *testing.T) {
	doc := `<WiFiProfiles><profile SSID="a>b" priority="1" /><profile SSID="New" priority="0" /></WiFiProfiles>`
	out, found, changed := setProfilePriorities(doc, "New")
	if !found || !changed {
		t.Fatalf("found=%v changed=%v, want true/true", found, changed)
	}
	if !strings.Contains(out, `SSID="a>b" priority="0"`) || !strings.Contains(out, `SSID="New" priority="10"`) {
		t.Errorf("quoted '>' broke the tag scan:\n%s", out)
	}
}

// TestSetProfilePrioritiesNoProfiles: an empty or unrelated document is left
// alone and reports nothing to do.
func TestSetProfilePrioritiesNoProfiles(t *testing.T) {
	for _, doc := range []string{
		"",
		"<WiFiProfiles />",
		"<profiles><entry ssid=\"x\"/></profiles>",
		// An element whose name merely STARTS with "profile" is not a profile:
		// the scanner must not edit it even when it carries a matching ssid.
		`<profileList ssid="WLAN50" priority="1"/>`,
	} {
		out, found, changed := setProfilePriorities(doc, "WLAN50")
		if found || changed || out != doc {
			t.Errorf("doc %q: found=%v changed=%v out=%q, want untouched", doc, found, changed, out)
		}
	}
}

// TestRewriteProfilePriorityFile drives the file-level path: the store on
// disk ends in the reporter's verified end state, and a second run performs
// no write at all.
func TestRewriteProfilePriorityFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "NetworkProfiles.xml")
	if err := os.WriteFile(p, []byte(reporterProfileStore), 0o644); err != nil {
		t.Fatal(err)
	}

	found, changed, err := rewriteProfilePriorityFile(p, "WLAN50")
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if !found || !changed {
		t.Fatalf("found=%v changed=%v, want true/true", found, changed)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, `SSID="WLAN50"
           priority="10"`) {
		t.Errorf("new network not ranked highest on disk:\n%s", got)
	}
	if !strings.Contains(got, `SSID="WLAN10"
           priority="0"`) {
		t.Errorf("old network not demoted on disk:\n%s", got)
	}
	// The ciphertext attributes must survive untouched.
	for _, keep := range []string{"cGxhY2Vob2xkZXItY2lwaGVydGV4dA==", "YW5vdGhlci1wbGFjZWhvbGRlcg=="} {
		if !strings.Contains(got, keep) {
			t.Errorf("ciphertext attribute lost: %s\n%s", keep, got)
		}
	}

	found, changed, err = rewriteProfilePriorityFile(p, "WLAN50")
	if err != nil || !found || changed {
		t.Errorf("second run: found=%v changed=%v err=%v, want true/false/nil", found, changed, err)
	}

	if _, _, err := rewriteProfilePriorityFile(filepath.Join(dir, "missing.xml"), "WLAN50"); err == nil {
		t.Errorf("missing file must return an error")
	}
}
