package webui

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSplitWiredAddrs(t *testing.T) {
	wlan := func(name string) bool { return name == "wlan0" }
	cases := []struct {
		name      string
		addrs     []lanIfaceAddr
		wantAll   []string
		wantWired []string
	}{
		{
			name:      "cable and Wi-Fi: the eth0 address is the cable",
			addrs:     []lanIfaceAddr{{"wlan0", "10.0.0.21"}, {"eth0", "10.0.0.20"}},
			wantAll:   []string{"10.0.0.20", "10.0.0.21"},
			wantWired: []string{"10.0.0.20"},
		},
		{
			// A radio handed to Linux as eth0 has no wireless marker; with no
			// positively wireless address the speaker cannot tell, so it says
			// nothing rather than calling its Wi-Fi a cable.
			name:    "no visible radio: no wired claim",
			addrs:   []lanIfaceAddr{{"eth0", "10.0.0.30"}},
			wantAll: []string{"10.0.0.30"},
		},
		{
			name:    "Wi-Fi only",
			addrs:   []lanIfaceAddr{{"wlan0", "10.0.0.40"}},
			wantAll: []string{"10.0.0.40"},
		},
		{
			name:    "one address on both kinds is ambiguous",
			addrs:   []lanIfaceAddr{{"wlan0", "10.0.0.50"}, {"br0", "10.0.0.50"}},
			wantAll: []string{"10.0.0.50"},
		},
		{
			name:  "empty",
			addrs: nil,
		},
	}
	for _, c := range cases {
		all, wired := splitWiredAddrs(c.addrs, wlan)
		if !reflect.DeepEqual(all, c.wantAll) {
			t.Errorf("%s: all = %v, want %v", c.name, all, c.wantAll)
		}
		if !reflect.DeepEqual(wired, c.wantWired) {
			t.Errorf("%s: wired = %v, want %v", c.name, wired, c.wantWired)
		}
	}
}

func TestIfaceIsWireless(t *testing.T) {
	dir := t.TempDir()
	proc := filepath.Join(dir, "wireless")
	sys := filepath.Join(dir, "net")
	if err := os.WriteFile(proc, []byte("Inter-| sta-|   Quality\n face | tus | link level noise\n wlan0: 0000   50.  -60.  -256\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sys, "wlan1", "phy80211"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sys, "eth0"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldProc, oldSys := procNetWirelessPath, sysClassNetDir
	procNetWirelessPath, sysClassNetDir = proc, sys
	t.Cleanup(func() { procNetWirelessPath, sysClassNetDir = oldProc, oldSys })
	for name, want := range map[string]bool{"wlan0": true, "wlan1": true, "eth0": false, "": false} {
		if got := ifaceIsWireless(name); got != want {
			t.Errorf("ifaceIsWireless(%q) = %v, want %v", name, got, want)
		}
	}
}
