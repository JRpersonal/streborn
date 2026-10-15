package webui

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The field case behind this feature: a speaker on its home network with an
// old network and a public hotspot still stored. Placeholder names only.
func fieldInputs() wlanForgetInputs {
	return wlanForgetInputs{
		Mechanism: "wpa",
		LiveSSID:  "HomeNet",
		Firmware:  []string{"HomeNet", "OldNet", "Vodafone Homespot"},
		Running:   []string{"HomeNet", "OldNet", "Vodafone Homespot"},
	}
}

func TestPlanWLANForget_KeepsCurrentRemovesTheRest(t *testing.T) {
	p := planWLANForget(fieldInputs(), "")
	if p.Code != "" {
		t.Fatalf("refused: %s %s", p.Code, p.Reason)
	}
	if p.Keep != "HomeNet" {
		t.Fatalf("keep = %q, want the live network", p.Keep)
	}
	if want := []string{"OldNet", "Vodafone Homespot"}; !reflect.DeepEqual(p.Remove, want) {
		t.Fatalf("remove = %v, want %v", p.Remove, want)
	}
}

func TestPlanWLANForget_KeepsTheTargetWhenItIsTheLiveNetwork(t *testing.T) {
	in := fieldInputs()
	in.Target = "HomeNet"
	p := planWLANForget(in, "")
	if p.Code != "" || p.Keep != "HomeNet" || len(p.Remove) != 2 {
		t.Fatalf("plan = %+v", p)
	}
}

func TestPlanWLANForget_RefusesWhenTargetAndLiveDisagree(t *testing.T) {
	in := fieldInputs()
	in.LiveSSID = "Vodafone Homespot" // the speaker fell back to the hotspot
	in.Target = "HomeNet"
	p := planWLANForget(in, "")
	if p.Code != forgetOnHotspot {
		t.Fatalf("code = %q, want %q (keeping the hotspot would forget the home network)", p.Code, forgetOnHotspot)
	}
	if len(p.Remove) != 0 {
		t.Fatalf("a refusal must not carry anything to remove: %v", p.Remove)
	}
	// A plain mismatch between two private networks keeps its own refusal.
	in.LiveSSID = "OldNet"
	if p := planWLANForget(in, ""); p.Code != forgetMismatch {
		t.Fatalf("code = %q, want %q", p.Code, forgetMismatch)
	}
}

// Support case 2026-10-15: a speaker on a public hotspot, with no STR target
// to compare against. Keeping "the current network" would keep the hotspot and
// delete the home network, so nothing may be removed but other hotspots.
func TestPlanWLANForget_NeverKeepsAHotspotOverTheHomeNetwork(t *testing.T) {
	in := fieldInputs()
	in.LiveSSID = "Vodafone Homespot"
	if p := planWLANForget(in, ""); p.Code != forgetOnHotspot || len(p.Remove) != 0 {
		t.Fatalf("plan = %+v, want %q", p, forgetOnHotspot)
	}
	// Deleting the home network by name while on the hotspot: refused too.
	if p := planWLANForget(in, "HomeNet"); p.Code != forgetOnHotspot {
		t.Fatalf("delete-one of a keyed network on a hotspot: code = %q, want %q", p.Code, forgetOnHotspot)
	}

	// An open network the speaker happens to be on counts as a hotspot.
	in = fieldInputs()
	in.Firmware = append(in.Firmware, "CafeFree")
	in.LiveSSID = "CafeFree"
	in.Open = map[string]bool{"CafeFree": true}
	if p := planWLANForget(in, ""); p.Code != forgetOnHotspot {
		t.Fatalf("open live network: code = %q, want %q", p.Code, forgetOnHotspot)
	}
	// Removing another hotspot while on an open network is fine.
	if p := planWLANForget(in, "Vodafone Homespot"); p.Code != "" || !reflect.DeepEqual(p.Remove, []string{"Vodafone Homespot"}) {
		t.Fatalf("plan = %+v, want the other hotspot removed", p)
	}

	// An open network STR itself gave the speaker is the owner's choice.
	in.Target = "CafeFree"
	if p := planWLANForget(in, ""); p.Code != "" || p.Keep != "CafeFree" {
		t.Fatalf("plan = %+v, want the STR-set open network kept", p)
	}
	// A public hotspot is refused even when STR set it.
	in = fieldInputs()
	in.LiveSSID = "Vodafone Homespot"
	in.Target = "Vodafone Homespot"
	if p := planWLANForget(in, ""); p.Code != forgetOnHotspot {
		t.Fatalf("STR-set public hotspot: code = %q, want %q", p.Code, forgetOnHotspot)
	}
}

func TestPlanWLANForget_RefusesWithoutAWiFiAssociation(t *testing.T) {
	in := fieldInputs()
	in.LiveSSID = ""
	if p := planWLANForget(in, ""); p.Code != forgetNotAssociated {
		t.Fatalf("code = %q, want %q", p.Code, forgetNotAssociated)
	}
	in.Wired = true
	if p := planWLANForget(in, ""); p.Code != forgetWired {
		t.Fatalf("code = %q, want %q", p.Code, forgetWired)
	}
}

// Never empty: the kept network has to be in the firmware store, or a restart
// would leave the speaker with nothing to join.
func TestPlanWLANForget_NeverEmptiesTheFirmwareStore(t *testing.T) {
	in := fieldInputs()
	in.Firmware = []string{"OldNet", "Vodafone Homespot"} // live network not stored
	if p := planWLANForget(in, ""); p.Code != forgetKeepNotStored {
		t.Fatalf("code = %q, want %q", p.Code, forgetKeepNotStored)
	}
	// Deleting the last remaining stored profile by name is refused as well.
	in = fieldInputs()
	in.Firmware = []string{"OldNet"}
	in.Running = []string{"HomeNet", "OldNet"}
	if p := planWLANForget(in, "OldNet"); p.Code == "" {
		t.Fatalf("deleting the only stored profile was allowed: %+v", p)
	}
}

func TestPlanWLANForget_NothingToDo(t *testing.T) {
	in := wlanForgetInputs{Mechanism: "wpa", LiveSSID: "HomeNet", Firmware: []string{"HomeNet"}, Running: []string{"HomeNet"}}
	p := planWLANForget(in, "")
	if p.Code != "" || len(p.Remove) != 0 {
		t.Fatalf("plan = %+v, want nothing to do", p)
	}
}

func TestPlanWLANForget_BCOChassisIsRefusedOnlyWhenThereIsWork(t *testing.T) {
	in := wlanForgetInputs{Mechanism: "bco", LiveSSID: "HomeNet", Chip: []string{"HomeNet"}}
	if p := planWLANForget(in, ""); p.Code != "" {
		t.Fatalf("one profile on a BCO chassis is nothing to do, got %q", p.Code)
	}
	// Some BCO chips do not report the SSID they are on: one stored network
	// is still nothing to do, not a liveness refusal.
	in.LiveSSID = ""
	if p := planWLANForget(in, ""); p.Code != "" || len(p.Remove) != 0 {
		t.Fatalf("plan = %+v, want nothing to do", p)
	}
	in.Chip = append(in.Chip, "OldNet")
	if p := planWLANForget(in, ""); p.Code != forgetChassis {
		t.Fatalf("code = %q, want %q", p.Code, forgetChassis)
	}
	if p := planWLANForget(in, "OldNet"); p.Code != forgetChassis {
		t.Fatalf("delete-one on BCO: code = %q, want %q", p.Code, forgetChassis)
	}
}

func TestPlanWLANForget_DeleteOne(t *testing.T) {
	in := fieldInputs()
	in.Target = "HomeNet"
	if p := planWLANForget(in, "OldNet"); p.Code != "" || !reflect.DeepEqual(p.Remove, []string{"OldNet"}) {
		t.Fatalf("plan = %+v", p)
	}
	if p := planWLANForget(in, "HomeNet"); p.Code != forgetIsCurrent {
		t.Fatalf("code = %q, want %q", p.Code, forgetIsCurrent)
	}
	if p := planWLANForget(in, "Nowhere"); p.Code != forgetNotFound {
		t.Fatalf("code = %q, want %q", p.Code, forgetNotFound)
	}
	// A target that is not the live network may still not be deleted.
	in.Target = "OldNet"
	if p := planWLANForget(in, "OldNet"); p.Code != forgetIsTarget {
		t.Fatalf("code = %q, want %q", p.Code, forgetIsTarget)
	}
}

const sampleProfiles = `<?xml version="1.0" encoding="UTF-8" ?>
<WiFiProfiles>
    <profile SSID="HomeNet" priority="10" security="wpa_or_wpa2" passphrase="CIPHERTEXT-A" wepKey="" encrypted="true" lastConnected="1" />
    <profile SSID="OldNet" priority="1" security="wpa_or_wpa2" passphrase="CIPHERTEXT-B" wepKey="" encrypted="true" lastConnected="0" />
    <profile SSID="Vodafone Homespot" priority="0" security="none" passphrase="" wepKey="" encrypted="true" lastConnected="0" />
</WiFiProfiles>
`

func TestRemoveProfileElements_CutsWholeLinesAndKeepsTheRestByteForByte(t *testing.T) {
	drop := func(s string) bool { return s != "HomeNet" }
	out, n, ok := removeProfileElements(sampleProfiles, drop)
	if !ok || n != 2 {
		t.Fatalf("ok=%v removed=%d, want 2", ok, n)
	}
	want := `<?xml version="1.0" encoding="UTF-8" ?>
<WiFiProfiles>
    <profile SSID="HomeNet" priority="10" security="wpa_or_wpa2" passphrase="CIPHERTEXT-A" wepKey="" encrypted="true" lastConnected="1" />
</WiFiProfiles>
`
	if out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
	// Nothing selected: the document comes back untouched.
	same, n, ok := removeProfileElements(sampleProfiles, func(string) bool { return false })
	if !ok || n != 0 || same != sampleProfiles {
		t.Fatal("a no-op removal changed the document")
	}
}

func TestRemoveProfileElements_EscapedNamesAndSingleLineDocs(t *testing.T) {
	doc := `<WiFiProfiles><profile SSID="Tom &amp; Jerry" priority="0" /><profile SSID="Keep" priority="1" /></WiFiProfiles>`
	out, n, ok := removeProfileElements(doc, func(s string) bool { return s == "Tom & Jerry" })
	if !ok || n != 1 {
		t.Fatalf("ok=%v n=%d", ok, n)
	}
	if out != `<WiFiProfiles><profile SSID="Keep" priority="1" /></WiFiProfiles>` {
		t.Fatalf("got %s", out)
	}
}

func TestRemoveProfileElements_RefusesAnUnclosedElement(t *testing.T) {
	doc := `<WiFiProfiles><profile SSID="Old" priority="0"><x/></WiFiProfiles>`
	if _, _, ok := removeProfileElements(doc, func(string) bool { return true }); ok {
		t.Fatal("an element without an end tag must not be cut by guesswork")
	}
}

func TestParseStoredProfiles_OpenAndEmptySlots(t *testing.T) {
	got := parseStoredProfiles(sampleProfiles, "profile")
	want := []profileEntry{{"HomeNet", false}, {"OldNet", false}, {"Vodafone Homespot", true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	airplay := `<PersistentWifiProfileArray>
<PersistentWifiProfile ssid="HomeNet" passphrase="X" security="WPA2PSK" />
<PersistentWifiProfile ssid="" passphrase="" security="" />
</PersistentWifiProfileArray>`
	if got := parseStoredProfiles(airplay, "PersistentWifiProfile"); len(got) != 1 || got[0].SSID != "HomeNet" {
		t.Fatalf("empty chip slots must be skipped, got %+v", got)
	}
	// The array element must not be read as a profile.
	if got := parseStoredProfiles(airplay, "PersistentWifiProfileArray"); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

const sampleConf = `ctrl_interface=/var/run/wpa_supplicant
update_config=1
device_name=Speaker
network={
    ssid="HomeNet"
    psk="placeholder-key"
    key_mgmt=WPA-PSK
    priority=10
}
network={
    ssid="Vodafone Homespot"
    key_mgmt=NONE
}
network={
    ssid=4f6c644e6574
    psk=0123
}
`

func TestWPAConfDropNetworks_RewritesWithOnlyTheKeptBlock(t *testing.T) {
	out, blocks, n := wpaConfDropNetworks(sampleConf, func(s string) bool { return s != "HomeNet" })
	if n != 2 {
		t.Fatalf("removed %d blocks, want 2", n)
	}
	if len(blocks) != 3 || blocks[1].SSID != "Vodafone Homespot" || !blocks[1].Open || blocks[2].SSID != "OldNet" {
		t.Fatalf("blocks = %+v", blocks)
	}
	want := `ctrl_interface=/var/run/wpa_supplicant
update_config=1
device_name=Speaker
network={
    ssid="HomeNet"
    psk="placeholder-key"
    key_mgmt=WPA-PSK
    priority=10
}
`
	if out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
	if strings.Count(out, "network={") != 1 {
		t.Fatal("the rewrite must leave exactly one network block")
	}
	// The vendor template shape: no blocks, nothing changes.
	tmpl := "ctrl_interface=/var/run/wpa_supplicant\nupdate_config=1\n"
	if got, _, n := wpaConfDropNetworks(tmpl, func(string) bool { return true }); got != tmpl || n != 0 {
		t.Fatal("a conf without network blocks must come back unchanged")
	}
}

func TestParseWPAConfSSID(t *testing.T) {
	cases := map[string]string{
		`"Home Net"`:     "Home Net",
		`"a\"b"`:         `a"b`,
		`486f6d654e6574`: "HomeNet",
	}
	for in, want := range cases {
		if got := parseWPAConfSSID(in); got != want {
			t.Errorf("parseWPAConfSSID(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestIsPublicHotspot(t *testing.T) {
	for _, n := range []string{"Vodafone Homespot", "vodafone hotspot", " Telekom_FON ", "WIFIonICE", "Telekom"} {
		if !isPublicHotspot(n) {
			t.Errorf("%q should be a public hotspot", n)
		}
	}
	for _, n := range []string{"", "HomeNet", "FRITZ!Box Gastzugang", "Telekom-ABC123", "Vodafone-1234"} {
		if isPublicHotspot(n) {
			t.Errorf("%q must not be flagged as a public hotspot", n)
		}
	}
}

func TestWLANForgetVerdict(t *testing.T) {
	rec := wlanForgetRecord{KeptTag: "SSID#aaaaaaaa", RemovedTags: []string{"SSID#bbbbbbbb", "SSID#cccccccc"}, Verdict: "pending-reboot"}
	if got := wlanForgetVerdict(rec, []string{"SSID#aaaaaaaa"}); got.Verdict != "held" || len(got.CameBack) != 0 {
		t.Fatalf("got %+v", got)
	}
	got := wlanForgetVerdict(rec, []string{"SSID#aaaaaaaa", "SSID#cccccccc"})
	if got.Verdict != "came-back" || !reflect.DeepEqual(got.CameBack, []string{"SSID#cccccccc"}) {
		t.Fatalf("got %+v", got)
	}
}

func TestWLANForgetRecordRoundTrip(t *testing.T) {
	old := wlanForgetRecordPath
	wlanForgetRecordPath = filepath.Join(t.TempDir(), "wlan-forget.json")
	t.Cleanup(func() { wlanForgetRecordPath = old })
	writeWLANForgetRecord(wlanForgetRecord{At: 1, KeptTag: "SSID#aaaaaaaa", RemovedTags: []string{"SSID#bbbbbbbb"}, Verdict: "pending-reboot"})
	rec, ok := readWLANForgetRecord()
	if !ok || rec.Verdict != "pending-reboot" || rec.KeptTag != "SSID#aaaaaaaa" {
		t.Fatalf("got %+v ok=%v", rec, ok)
	}
	d := wlanForgetDebug()
	if d["ran"] != true || d["verdict"] != "pending-reboot" {
		t.Fatalf("debug = %v", d)
	}
}

func TestWLANForgetStateList(t *testing.T) {
	st := wlanForgetState{
		in: wlanForgetInputs{
			LiveSSID: "HomeNet", Target: "HomeNet",
			Firmware: []string{"HomeNet", "Vodafone Homespot"},
			Running:  []string{"HomeNet", "Vodafone Homespot"},
		},
		open: map[string]bool{"Vodafone Homespot": true},
	}
	got := st.list()
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if !got[0].Current || !got[0].STRTarget || got[0].Open || !reflect.DeepEqual(got[0].Sources, []string{"firmware", "running"}) {
		t.Fatalf("home entry = %+v", got[0])
	}
	if got[1].Current || !got[1].Open || !got[1].PublicHotspot {
		t.Fatalf("hotspot entry = %+v", got[1])
	}
}

func TestReadWlanCredsSSIDAt(t *testing.T) {
	p := filepath.Join(t.TempDir(), "wlan-creds")
	if err := writeWlanCredsFile(p, "HomeNet", "placeholder-key", false); err != nil {
		t.Fatal(err)
	}
	if got := readWlanCredsSSIDAt(p); got != "HomeNet" {
		t.Fatalf("got %q", got)
	}
	if got := readWlanCredsSSIDAt(p + ".missing"); got != "" {
		t.Fatalf("got %q", got)
	}
}
