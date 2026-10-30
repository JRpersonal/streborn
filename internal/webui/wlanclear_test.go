package webui

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanWLANClear(t *testing.T) {
	cases := []struct {
		name string
		in   wlanClearInputs
		want string
	}{
		{"cabled wpa speaker reached over its cable", wlanClearInputs{Mechanism: "wpa", WiredIface: "eth0", RequestOnWired: true}, ""},
		{"wpa speaker on Wi-Fi only", wlanClearInputs{Mechanism: "wpa"}, clearNotWired},
		{"cabled, but reached over Wi-Fi", wlanClearInputs{Mechanism: "wpa", WiredIface: "eth0"}, clearReachedOverAir},
		{"BCO chassis, even with a route out of eth0", wlanClearInputs{Mechanism: "bco", WiredIface: "eth0", RequestOnWired: true}, clearChassis},
		{"unknown mechanism", wlanClearInputs{}, clearChassis},
	}
	for _, c := range cases {
		code, reason := planWLANClear(c.in)
		if code != c.want {
			t.Errorf("%s: code = %q, want %q", c.name, code, c.want)
		}
		if (code == "") != (reason == "") {
			t.Errorf("%s: a refusal needs a reason and an allow none (code %q, reason %q)", c.name, code, reason)
		}
	}
}

func TestAddrsContainIP(t *testing.T) {
	_, nw, _ := net.ParseCIDR("192.0.2.0/24")
	own := &net.IPNet{IP: net.ParseIP("192.0.2.7"), Mask: nw.Mask}
	addrs := []net.Addr{own, &net.IPAddr{IP: net.ParseIP("2001:db8::7")}}
	if !addrsContainIP(addrs, net.ParseIP("192.0.2.7")) {
		t.Fatal("own IPv4 not found")
	}
	if !addrsContainIP(addrs, net.ParseIP("2001:db8::7")) {
		t.Fatal("own IPv6 not found")
	}
	if addrsContainIP(addrs, net.ParseIP("192.0.2.8")) {
		t.Fatal("a neighbour's address in the same subnet is not ours")
	}
	if addrsContainIP(addrs, nil) {
		t.Fatal("an unknown address must never count as on the cable")
	}
}

func TestRequestLocalIP(t *testing.T) {
	r := httptest.NewRequest(http.MethodDelete, "/api/box/wlan", nil)
	if ip := requestLocalIP(r); ip != nil {
		t.Fatalf("no local addr recorded, got %v", ip)
	}
	addr := &net.TCPAddr{IP: net.ParseIP("192.0.2.7"), Port: 8888}
	r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, net.Addr(addr)))
	if ip := requestLocalIP(r); !ip.Equal(net.ParseIP("192.0.2.7")) {
		t.Fatalf("local ip = %v", ip)
	}
}

const sampleWPAConfWithNetwork = `ctrl_interface=/var/run/wpa_supplicant
update_config=1
device_name=SoundTouch
ap_scan=1
network={
    ssid="HomeNet"
    psk="secret-pass"
    key_mgmt=WPA-PSK
    priority=10
}
`

func TestBuildWPAConfigNoNetwork(t *testing.T) {
	got := buildWPAConfigNoNetwork(sampleWPAConfWithNetwork)
	want := "ctrl_interface=/var/run/wpa_supplicant\nupdate_config=1\ndevice_name=SoundTouch\nap_scan=1\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	// An unreadable conf still yields a usable file with no network.
	min := buildWPAConfigNoNetwork("")
	if !strings.Contains(min, wpaCtrlInterfaceLine) || !strings.Contains(min, "update_config=1") || strings.Contains(min, "network=") {
		t.Fatalf("minimal conf = %q", min)
	}
}

// clearFixture lays out a cabled speaker's Wi-Fi stores in a temp dir and
// points the clear at them.
type clearFixture struct {
	dir      string
	paths    wlanClearPaths
	profiles string
	rebooted int
	running  int
}

func newClearFixture(t *testing.T, in wlanClearInputs) *clearFixture {
	t.Helper()
	dir := t.TempDir()
	f := &clearFixture{dir: dir}
	persist := filepath.Join(dir, "BoseApp-Persistence", "1")
	if err := os.MkdirAll(persist, 0o755); err != nil {
		t.Fatal(err)
	}
	f.profiles = filepath.Join(persist, "NetworkProfiles.xml")
	f.paths = wlanClearPaths{
		profilesGlob: filepath.Join(dir, "BoseApp-Persistence", "*", "NetworkProfiles.xml"),
		wpaConf:      filepath.Join(dir, "wpa_supplicant.conf"),
		wpaConfTmp:   filepath.Join(dir, "wpa_supplicant.conf.str"),
		wpaBackup:    filepath.Join(dir, "wpa_supplicant.conf.bak"),
		creds:        filepath.Join(dir, "wlan-creds"),
		credsRetired: filepath.Join(dir, "wlan-creds.superseded"),
		target:       filepath.Join(dir, "wlan-target"),
		known:        filepath.Join(dir, "wlan-known.json"),
		applyMarker:  filepath.Join(dir, ".wlan-apply-pending"),
	}
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(f.profiles, sampleProfiles)
	write(f.paths.wpaConf, sampleWPAConfWithNetwork)
	write(f.paths.creds, "SSID=HomeNet\nPASS=secret-pass\n")
	write(f.paths.creds+".bak", "SSID=OldNet\nPASS=old-pass\n")
	write(f.paths.target, `{"ssid":"HomeNet"}`)
	write(f.paths.applyMarker, "1\n")

	oldPaths, oldIn, oldRun, oldReboot, oldRec := wlanClearPathsFn, wlanClearInputsFn, wlanClearRunningFn, wlanClearRebootFn, wlanForgetRecordPath
	t.Cleanup(func() {
		wlanClearPathsFn, wlanClearInputsFn, wlanClearRunningFn, wlanClearRebootFn, wlanForgetRecordPath = oldPaths, oldIn, oldRun, oldReboot, oldRec
	})
	wlanClearPathsFn = func() wlanClearPaths { return f.paths }
	wlanClearInputsFn = func(*http.Request) (wlanClearInputs, string) { return in, "wlan0" }
	wlanClearRunningFn = func(string) int { f.running++; return 3 }
	wlanClearRebootFn = func() { f.rebooted++ }
	wlanForgetRecordPath = filepath.Join(dir, "wlan-forget.json")
	return f
}

func (f *clearFixture) read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func doClear(t *testing.T, body string) (*httptest.ResponseRecorder, wlanClearResult) {
	t.Helper()
	s := &Server{logger: slog.New(slog.DiscardHandler)}
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(http.MethodDelete, "/api/box/wlan", nil)
	} else {
		r = httptest.NewRequest(http.MethodDelete, "/api/box/wlan", strings.NewReader(body))
	}
	r.RemoteAddr = "127.0.0.1:50000"
	w := httptest.NewRecorder()
	s.handleBoxWLAN(w, r)
	var res wlanClearResult
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	return w, res
}

func TestHandleBoxWLANClear_RefusesASpeakerThatIsNotWired(t *testing.T) {
	for _, in := range []wlanClearInputs{
		{Mechanism: "wpa"},
		{Mechanism: "wpa", WiredIface: "eth0"},
		{Mechanism: "bco", WiredIface: "eth0", RequestOnWired: true},
	} {
		f := newClearFixture(t, in)
		w, res := doClear(t, "")
		wantCode, _ := planWLANClear(in)
		if w.Code != http.StatusConflict || res.Status != "refused" || res.Code != wantCode {
			t.Fatalf("%+v: HTTP %d %+v, want 409 refused %q", in, w.Code, res, wantCode)
		}
		// Nothing may have been touched.
		if f.read(t, f.profiles) != sampleProfiles {
			t.Fatalf("%+v: the firmware store changed on a refusal", in)
		}
		if f.read(t, f.paths.wpaConf) != sampleWPAConfWithNetwork {
			t.Fatalf("%+v: the conf changed on a refusal", in)
		}
		if _, err := os.Stat(f.paths.creds); err != nil {
			t.Fatalf("%+v: wlan-creds removed on a refusal", in)
		}
		if f.rebooted != 0 || f.running != 0 {
			t.Fatalf("%+v: restarted or cleared the running supplicant on a refusal", in)
		}
	}
}

func TestHandleBoxWLANClear_CabledSpeakerForgetsEverything(t *testing.T) {
	f := newClearFixture(t, wlanClearInputs{Mechanism: "wpa", WiredIface: "eth0", RequestOnWired: true})
	w, res := doClear(t, "")
	if w.Code != http.StatusOK || res.Status != "ok" {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}

	// The conf: rewritten in full, globals kept, no network block.
	conf := f.read(t, f.paths.wpaConf)
	if conf != "ctrl_interface=/var/run/wpa_supplicant\nupdate_config=1\ndevice_name=SoundTouch\nap_scan=1\n" {
		t.Fatalf("conf:\n%s", conf)
	}
	if !res.ConfRewritten || res.ConfMethod != "direct" {
		t.Fatalf("conf result = %v %q", res.ConfRewritten, res.ConfMethod)
	}

	// The firmware store: every profile gone, the document itself kept.
	store := f.read(t, f.profiles)
	if strings.Contains(store, "<profile") || !strings.Contains(store, "<WiFiProfiles>") || !strings.Contains(store, "</WiFiProfiles>") {
		t.Fatalf("store:\n%s", store)
	}
	if res.FirmwareRemoved != 3 || len(res.RemovedTags) != 3 {
		t.Fatalf("firmware removed = %d tags %v", res.FirmwareRemoved, res.RemovedTags)
	}
	// Tags only, never a network name.
	if strings.Contains(w.Body.String(), "HomeNet") || strings.Contains(w.Body.String(), "secret-pass") {
		t.Fatalf("response leaks a network name or password: %s", w.Body.String())
	}

	// STR's own records: all gone, so no boot path replays the network.
	for _, p := range []string{f.paths.creds, f.paths.creds + ".bak", f.paths.target, f.paths.applyMarker} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s survived the clear", filepath.Base(p))
		}
	}
	if res.STRRecords != 4 {
		t.Fatalf("strRecordsRemoved = %d, want 4", res.STRRecords)
	}

	if res.RunningRemoved != 3 || f.running != 1 {
		t.Fatalf("running supplicant not cleared: %d/%d", res.RunningRemoved, f.running)
	}
	if !res.Rebooting || f.rebooted != 1 {
		t.Fatalf("expected one restart, rebooting=%v count=%d", res.Rebooting, f.rebooted)
	}
	if rec, ok := readWLANForgetRecord(); !ok || rec.Verdict != "pending-reboot" || len(rec.RemovedTags) != 3 {
		t.Fatalf("after-boot record = %+v ok=%v", rec, ok)
	}
}

func TestHandleBoxWLANClear_RebootFalseSkipsTheRestart(t *testing.T) {
	f := newClearFixture(t, wlanClearInputs{Mechanism: "wpa", WiredIface: "eth0", RequestOnWired: true})
	w, res := doClear(t, `{"reboot":false}`)
	if w.Code != http.StatusOK || res.Rebooting || f.rebooted != 0 {
		t.Fatalf("HTTP %d rebooting=%v count=%d", w.Code, res.Rebooting, f.rebooted)
	}
}

func TestHandleBoxWLANClear_UnexpectedStoreShapeChangesNothing(t *testing.T) {
	f := newClearFixture(t, wlanClearInputs{Mechanism: "wpa", WiredIface: "eth0", RequestOnWired: true})
	odd := `<WiFiProfiles><profile SSID="Old" priority="0"><x/></WiFiProfiles>`
	if err := os.WriteFile(f.profiles, []byte(odd), 0o600); err != nil {
		t.Fatal(err)
	}
	w, res := doClear(t, "")
	if w.Code != http.StatusInternalServerError || res.Code != clearUnexpected {
		t.Fatalf("HTTP %d %+v", w.Code, res)
	}
	if f.read(t, f.profiles) != odd || f.read(t, f.paths.wpaConf) != sampleWPAConfWithNetwork {
		t.Fatal("a store was changed although the firmware store could not be parsed")
	}
	if _, err := os.Stat(f.paths.creds); err != nil || f.rebooted != 0 {
		t.Fatal("STR records removed or speaker restarted after an aborted clear")
	}
}

func TestHandleBoxWLAN_OtherMethodsStillRefused(t *testing.T) {
	s := &Server{logger: slog.New(slog.DiscardHandler)}
	r := httptest.NewRequest(http.MethodPost, "/api/box/wlan", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	w := httptest.NewRecorder()
	s.handleBoxWLAN(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST answered %d", w.Code)
	}
}
