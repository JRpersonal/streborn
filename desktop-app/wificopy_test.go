package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/wifiprofiles"
)

func mustNet(t *testing.T, cidr string) *net.IPNet {
	t.Helper()
	ip, n, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}
	n.IP = ip
	return n
}

func TestWifiCopyPrecheck(t *testing.T) {
	home := []*net.IPNet{mustNet(t, "192.0.2.10/24")}
	speaker := net.ParseIP("192.0.2.51").To4()
	cases := []struct {
		name string
		env  wifiCopyEnv
		want string
	}{
		{"same Wi-Fi subnet", wifiCopyEnv{GOOS: "windows", SSID: "ExampleNet", WiFiNets: home, SpeakerIP: speaker}, ""},
		{"linux too", wifiCopyEnv{GOOS: "linux", SSID: "ExampleNet", WiFiNets: home, SpeakerIP: speaker}, ""},
		{"macOS never reads the keychain unasked", wifiCopyEnv{GOOS: "darwin", SSID: "ExampleNet", WiFiNets: home, SpeakerIP: speaker}, wifiCopyMacOS},
		{"computer on a cable, no Wi-Fi", wifiCopyEnv{GOOS: "windows", WiFiNets: home, SpeakerIP: speaker}, wifiCopyNoWiFi},
		{"Wi-Fi addresses unreadable", wifiCopyEnv{GOOS: "windows", SSID: "ExampleNet", SpeakerIP: speaker}, wifiCopyNoInterface},
		{"speaker address unknown", wifiCopyEnv{GOOS: "windows", SSID: "ExampleNet", WiFiNets: home}, wifiCopyNoSpeakerAddr},
		{"speaker reached through another interface", wifiCopyEnv{GOOS: "windows", SSID: "ExampleNet",
			WiFiNets: home, SpeakerIP: net.ParseIP("198.51.100.7").To4()}, wifiCopyOtherSubnet},
		{"neighbouring /24 is another network", wifiCopyEnv{GOOS: "windows", SSID: "ExampleNet",
			WiFiNets: home, SpeakerIP: net.ParseIP("192.0.3.51").To4()}, wifiCopyOtherSubnet},
	}
	for _, c := range cases {
		if got := wifiCopyPrecheck(c.env); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestWifiCopyPasswordProblem(t *testing.T) {
	cases := map[string]string{
		"":                       wifiCopyNoPassword,
		"1234567":                wifiCopyBadPassword,
		"12345678":               "",
		strings.Repeat("a", 64):  "",
		strings.Repeat("a", 65):  wifiCopyBadPassword,
		"abcdefgh\nHIDDEN=1":     wifiCopyBadPassword,
		"correct-horse-battery!": "",
	}
	for pw, want := range cases {
		if got := wifiCopyPasswordProblem(pw); got != want {
			t.Errorf("%q: got %q, want %q", pw, got, want)
		}
	}
}

func TestInterfaceNetsMatchesTheWifiAdapter(t *testing.T) {
	mac := func(s string) net.HardwareAddr { m, _ := net.ParseMAC(s); return m }
	ifaces := []net.Interface{
		{Index: 1, Name: "Ethernet", HardwareAddr: mac("02:00:00:00:00:01")},
		{Index: 2, Name: "WLAN", HardwareAddr: mac("aa:bb:cc:dd:ee:ff")},
	}
	addrs := map[int][]net.Addr{
		1: {mustNet(t, "198.51.100.5/24")},
		2: {mustNet(t, "192.0.2.10/24"), mustNet(t, "fe80::1/64")},
	}
	get := func(i net.Interface) ([]net.Addr, error) { return addrs[i.Index], nil }

	got := interfaceNets(wifiprofiles.Interface{Name: "Wi-Fi", MAC: "AA:BB:CC:DD:EE:FF"}, ifaces, get)
	if len(got) != 1 || got[0].String() != "192.0.2.10/24" {
		t.Errorf("by MAC: got %v, want the Wi-Fi adapter's IPv4 subnet only", got)
	}
	got = interfaceNets(wifiprofiles.Interface{Name: "WLAN"}, ifaces, get)
	if len(got) != 1 || got[0].String() != "192.0.2.10/24" {
		t.Errorf("by name: got %v", got)
	}
	if got := interfaceNets(wifiprofiles.Interface{Name: "nope", MAC: "02:00:00:00:00:99"}, ifaces, get); got != nil {
		t.Errorf("no adapter matched, but got %v", got)
	}
	failing := func(net.Interface) ([]net.Addr, error) { return nil, errors.New("boom") }
	if got := interfaceNets(wifiprofiles.Interface{Name: "WLAN"}, ifaces, failing); got != nil {
		t.Errorf("unreadable addresses gave %v", got)
	}
}

func TestWifiCopyFromAnswer(t *testing.T) {
	cases := []struct {
		name   string
		status int
		ctype  string
		body   string
		want   WiFiCopyResult
	}{
		{"stored", 200, "application/json; charset=utf-8", `{"status":"stored-copy","stored":true}`, WiFiCopyResult{Stored: true, Reason: wifiCopyStored}},
		{"old agent answers its phone page", 200, "text/html; charset=utf-8", `<!doctype html>`, WiFiCopyResult{Reason: wifiCopyOldAgent, Note: true}},
		{"JSON without the status is not a copy", 200, "application/json", `{"status":"rebooting"}`, WiFiCopyResult{Reason: wifiCopyOldAgent, Note: true}},
		{"404", 404, "text/plain", "404 page not found", WiFiCopyResult{Reason: wifiCopyOldAgent, Note: true}},
		{"other network", 409, "application/json", `{"code":"other-network"}`, WiFiCopyResult{Reason: "refused:other-network", Note: true}},
		{"has a copy: no note", 409, "application/json", `{"code":"already-has-copy"}`, WiFiCopyResult{Reason: "refused:already-has-copy"}},
		{"not needed: no note", 409, "application/json", `{"code":"not-needed"}`, WiFiCopyResult{Reason: "refused:not-needed"}},
		{"retired: no note", 409, "application/json", `{"code":"retired"}`, WiFiCopyResult{Reason: "refused:retired"}},
		{"busy", 409, "application/json", `{"code":"busy"}`, WiFiCopyResult{Reason: "refused:busy", Note: true}},
		{"invalid", 400, "application/json", `{"code":"invalid"}`, WiFiCopyResult{Reason: "refused:invalid", Note: true}},
	}
	for _, c := range cases {
		got := wifiCopyFromAnswer(c.status, c.ctype, strings.NewReader(c.body))
		if got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

// The typed copy reaches the agent's store-only path with exactly the
// owner's network, and never the switch endpoint.
func TestStoreSpeakerWiFiCopyUsesTheStoreOnlyPath(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"stored-copy","stored":true}`))
	}))
	defer srv.Close()
	a := newTestApp()
	res := a.StoreSpeakerWiFiCopy("127.0.0.1", listenPort(t, srv), " ExampleNet ", "correct-horse-battery")
	if !res.Stored || res.Reason != wifiCopyStored {
		t.Fatalf("got %+v", res)
	}
	if gotPath != "/api/box/wlan/copy" || gotMethod != http.MethodPut {
		t.Errorf("request went to %s %s", gotMethod, gotPath)
	}
	if gotBody["ssid"] != "ExampleNet" || gotBody["password"] != "correct-horse-battery" || gotBody["source"] != "typed" {
		t.Errorf("body %v", gotBody)
	}
	// A bad password never leaves the computer.
	gotPath = ""
	if res := a.StoreSpeakerWiFiCopy("127.0.0.1", listenPort(t, srv), "ExampleNet", "short"); res.Stored || res.Reason != wifiCopyBadPassword {
		t.Errorf("short password: %+v", res)
	}
	if gotPath != "" {
		t.Error("a refused password was sent to the speaker")
	}
}

func TestWifiCopyNeedsNote(t *testing.T) {
	for _, r := range []string{wifiCopyMacOS, wifiCopyNoWiFi, wifiCopyOtherSubnet, wifiCopyNoPassword, wifiCopyOldAgent, wifiCopyUnreachable, "refused:other-network", "refused:speaker-in-setup"} {
		if !wifiCopyNeedsNote(r) {
			t.Errorf("%s: no note, but the speaker still has no copy", r)
		}
	}
}

// A speaker that does not report a missing copy is left alone before the
// computer's Wi-Fi or password is even looked at, and gets no note.
func TestKeepSpeakerWiFiCopyAsksTheSpeakerFirst(t *testing.T) {
	var paths []string
	answer := `{"version":"v1.6.3"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(answer))
	}))
	defer srv.Close()
	a := newTestApp()
	res := a.KeepSpeakerWiFiCopy("127.0.0.1", listenPort(t, srv), "install")
	if res.Stored || res.Reason != wifiCopyNotMissing || res.Note {
		t.Errorf("got %+v, want not-missing without a note", res)
	}
	if len(paths) != 1 || paths[0] != "GET /api/agent/version" {
		t.Errorf("requests %v, want only the version read", paths)
	}
	answer = `{"version":"v1.6.3","wlanCreds":"missing"}`
	if missing, known := a.agentReportsWiFiCopyMissing("127.0.0.1", listenPort(t, srv)); !missing || !known {
		t.Errorf("wlanCreds missing: missing=%v known=%v", missing, known)
	}
	answer = `<!doctype html>`
	if _, known := a.agentReportsWiFiCopyMissing("127.0.0.1", listenPort(t, srv)); known {
		t.Error("an HTML page counted as the agent's answer")
	}
}
