package webui

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// #1573: a setup through Bose's own page keeps STR's network name and changes
// the password. The supersede check compares names and cannot see it; the
// owner-setup check compares the speaker's own store.

const airplayBefore = `<AirplayConfiguration BCOResetTimerEnabled="true">
<PersistentWifiProfileArray>
<PersistentWifiProfile ssid="HomeNet" passphrase="old-plain" wpaCipher="AES" security="WPA2PSK" wepKey="" encrypted="false" dhcpStatus="DHCP_ACTIVE" ipAddress="" />
<PersistentWifiProfile ssid="" passphrase="EMPTY" security="" encrypted="true" />
</PersistentWifiProfileArray></AirplayConfiguration>`

// The same network, given a new password through the setup page; the firmware
// stores it encrypted.
const airplayOwnerSetup = `<AirplayConfiguration BCOResetTimerEnabled="true">
<PersistentWifiProfileArray>
<PersistentWifiProfile ssid="HomeNet" passphrase="R0NNLUJMT0I=" wpaCipher="AES" security="WPA2PSK" wepKey="" encrypted="true" dhcpStatus="DHCP_ACTIVE" ipAddress="" />
<PersistentWifiProfile ssid="" passphrase="EMPTY" security="" encrypted="true" />
</PersistentWifiProfileArray></AirplayConfiguration>`

// Nothing the owner did: the firmware rewrote its volatile fields on a join.
const airplayVolatileOnly = `<AirplayConfiguration BCOResetTimerEnabled="true">
<PersistentWifiProfileArray>
<PersistentWifiProfile ssid="HomeNet" passphrase="old-plain" wpaCipher="AES" security="WPA2PSK" wepKey="" encrypted="false" dhcpStatus="DHCP_INACTIVE" ipAddress="192.0.2.10" />
</PersistentWifiProfileArray></AirplayConfiguration>`

func bcoDocs(doc string) map[string][]string {
	return map[string][]string{"PersistentWifiProfile": {doc}}
}

func TestBoseStoreProfileTags_IgnoreVolatileFields(t *testing.T) {
	a := boseStoreProfileTags(bcoDocs(airplayBefore))
	if len(a) != 1 {
		t.Fatalf("want one tag (the empty slot is not a profile), got %d", len(a))
	}
	if b := boseStoreProfileTags(bcoDocs(airplayVolatileOnly)); len(b) != 1 || b[0] != a[0] {
		t.Fatal("dhcpStatus / ipAddress changed the tag; a join would read as a new setup")
	}
	if c := boseStoreProfileTags(bcoDocs(airplayOwnerSetup)); len(c) != 1 || c[0] == a[0] {
		t.Fatal("a new password for the same network must change the tag")
	}
	// sm2: STR's own priority rewrite is not the owner speaking.
	np1 := `<profile SSID="HomeNet" priority="1" security="wpa_or_wpa2" passphrase="CT-A" wepKey="" encrypted="true" lastConnected="1" />`
	np2 := `<profile SSID="HomeNet" priority="10" security="wpa_or_wpa2" passphrase="CT-A" wepKey="" encrypted="true" lastConnected="0" />`
	x := boseStoreProfileTags(map[string][]string{"profile": {np1}})
	y := boseStoreProfileTags(map[string][]string{"profile": {np2}})
	if len(x) != 1 || len(y) != 1 || x[0] != y[0] {
		t.Fatal("priority / lastConnected changed the tag")
	}
}

func TestOwnerSetupAddedProfile(t *testing.T) {
	at := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	before := boseStoreProfileTags(bcoDocs(airplayBefore))
	snap := wlanSetupSnapshot{At: at.Unix(), Tags: before}
	cases := []struct {
		name     string
		now      []string
		strWrote time.Time
		want     bool
	}{
		{"store unchanged: a cancelled key combination", before, time.Time{}, false},
		{"same network, new password", boseStoreProfileTags(bcoDocs(airplayOwnerSetup)), time.Time{}, true},
		{"STR wrote credentials before the setup began", boseStoreProfileTags(bcoDocs(airplayOwnerSetup)), at.Add(-time.Hour), true},
		{"STR wrote credentials during the setup: the change is STR's", boseStoreProfileTags(bcoDocs(airplayOwnerSetup)), at.Add(time.Minute), false},
		{"a profile removed (STR's forget) is not the owner", nil, time.Time{}, false},
	}
	for _, c := range cases {
		if got := ownerSetupAddedProfile(snap, c.now, c.strWrote); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func setOld(t *testing.T, p string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

// The field case end to end: snapshot when the setup opens, retire when the
// owner's profile appears, the stale password kept aside (never deleted).
func TestOwnerSetupPass_RetiresStaleCredentials(t *testing.T) {
	p := intentPaths(t)
	snapPath := filepath.Join(filepath.Dir(p.creds), "snap.json")
	if err := writeWlanCredsFile(p.creds, "HomeNet", "wrong-pass", false); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	setOld(t, p.creds, t0.Add(-48*time.Hour))

	if ownerSetupPassAt(p, snapPath, bcoDocs(airplayBefore), true, t0, nil) {
		t.Fatal("the first setup pass must only take the snapshot")
	}
	if !exists(snapPath) {
		t.Fatal("no snapshot taken")
	}
	if ownerSetupPassAt(p, snapPath, bcoDocs(airplayBefore), true, t0.Add(15*time.Second), nil) {
		t.Fatal("an unchanged store retired STR's credentials")
	}
	if !ownerSetupPassAt(p, snapPath, bcoDocs(airplayOwnerSetup), true, t0.Add(2*time.Minute), nil) {
		t.Fatal("the owner's new password did not retire STR's stale one")
	}
	if exists(p.creds) || !exists(p.retired) || exists(snapPath) {
		t.Fatalf("creds=%v retired=%v snap=%v", exists(p.creds), exists(p.retired), exists(snapPath))
	}
}

// A setup that ends in a reboot: the snapshot survives, and the first pass out
// of setup on the next boot still compares.
func TestOwnerSetupPass_ComparesOutOfSetupAfterReboot(t *testing.T) {
	p := intentPaths(t)
	snapPath := filepath.Join(filepath.Dir(p.creds), "snap.json")
	if err := writeWlanCredsFile(p.creds, "HomeNet", "wrong-pass", false); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	setOld(t, p.creds, t0.Add(-48*time.Hour))
	ownerSetupPassAt(p, snapPath, bcoDocs(airplayBefore), true, t0, nil)
	if !ownerSetupPassAt(p, snapPath, bcoDocs(airplayOwnerSetup), false, t0.Add(3*time.Minute), nil) {
		t.Fatal("out of setup after the reboot, the owner's profile was not noticed")
	}
}

// A key combination that was cancelled changes nothing, and the snapshot goes
// once it has settled. Credentials that still work are never touched.
func TestOwnerSetupPass_CancelledCombinationKeepsCredentials(t *testing.T) {
	p := intentPaths(t)
	snapPath := filepath.Join(filepath.Dir(p.creds), "snap.json")
	if err := writeWlanCredsFile(p.creds, "HomeNet", "good-pass", false); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	setOld(t, p.creds, t0.Add(-48*time.Hour))
	ownerSetupPassAt(p, snapPath, bcoDocs(airplayBefore), true, t0, nil)
	if ownerSetupPassAt(p, snapPath, bcoDocs(airplayVolatileOnly), false, t0.Add(time.Minute), nil) {
		t.Fatal("a cancelled combination retired working credentials")
	}
	if !exists(snapPath) {
		t.Fatal("the snapshot went on the first pass out of setup; a flapping source would write NAND per flip")
	}
	ownerSetupPassAt(p, snapPath, bcoDocs(airplayVolatileOnly), false, t0.Add(wlanSetupSnapshotSettle+time.Minute), nil)
	if exists(snapPath) || !exists(p.creds) || exists(p.retired) {
		t.Fatalf("snap=%v creds=%v retired=%v", exists(snapPath), exists(p.creds), exists(p.retired))
	}
}

// STR provisioning during the episode (a stick, the app's Wi-Fi apply) writes
// wlan-creds; the store change is then STR's own and nothing is retired.
func TestOwnerSetupPass_STRProvisioningIsNotTheOwner(t *testing.T) {
	p := intentPaths(t)
	snapPath := filepath.Join(filepath.Dir(p.creds), "snap.json")
	t0 := time.Now()
	ownerSetupPassAt(p, snapPath, bcoDocs(airplayBefore), true, t0.Add(-time.Minute), nil)
	if err := writeWlanCredsFile(p.creds, "HomeNet", "new-from-str", false); err != nil {
		t.Fatal(err)
	}
	if ownerSetupPassAt(p, snapPath, bcoDocs(airplayOwnerSetup), true, t0, nil) {
		t.Fatal("STR's own provisioning retired the credentials it just wrote")
	}
	if !exists(p.creds) {
		t.Fatal("credentials gone")
	}
}

// Outside setup with no snapshot nothing is written: the watcher calls this on
// every pass of a healthy speaker.
func TestOwnerSetupPass_NoSnapshotOutsideSetup(t *testing.T) {
	p := intentPaths(t)
	snapPath := filepath.Join(filepath.Dir(p.creds), "snap.json")
	ownerSetupPassAt(p, snapPath, bcoDocs(airplayBefore), false, time.Now(), nil)
	if exists(snapPath) {
		t.Fatal("a snapshot was written outside setup")
	}
}
