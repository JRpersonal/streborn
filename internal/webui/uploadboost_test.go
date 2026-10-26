package webui

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// procWirelessWithWlan0 is /proc/net/wireless with one associated interface.
const procWirelessWithWlan0 = "Inter-| sta-|   Quality        |   Discarded packets               | Missed | WE\n" +
	" face | tus | link level noise |  nwid  crypt   frag  retry   misc | beacon | 22\n" +
	" wlan0: 0000   54.  -56.  -256        0      0      0      0      0        0\n"

// procWirelessHeadersOnly is what a driver without wireless extensions leaves.
const procWirelessHeadersOnly = "Inter-| sta-|   Quality        |   Discarded packets               | Missed | WE\n" +
	" face | tus | link level noise |  nwid  crypt   frag  retry   misc | beacon | 22\n"

// wirelessFixture points wirelessIface at a fake /proc file and /sys tree.
// proc == "" leaves the proc file absent; sysIfaces maps an interface name to
// the marker entry it carries ("" for a plain Ethernet interface).
func wirelessFixture(t *testing.T, proc string, sysIfaces map[string]string) {
	t.Helper()
	root := t.TempDir()
	origProc, origSys := procNetWirelessPath, sysClassNetDir
	t.Cleanup(func() { procNetWirelessPath, sysClassNetDir = origProc, origSys })
	procNetWirelessPath = filepath.Join(root, "wireless")
	if proc != "" {
		if err := os.WriteFile(procNetWirelessPath, []byte(proc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sysClassNetDir = filepath.Join(root, "net")
	for name, marker := range sysIfaces {
		dir := filepath.Join(sysClassNetDir, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if marker != "" {
			if err := os.MkdirAll(filepath.Join(dir, marker), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(sysIfaces) == 0 {
		if err := os.MkdirAll(sysClassNetDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWirelessIfaceLookup(t *testing.T) {
	cases := []struct {
		name      string
		proc      string
		sys       map[string]string
		wantIface string
		wantHow   string
	}{
		{"proc lists wlan0", procWirelessWithWlan0, map[string]string{"wlan0": "wireless"}, "wlan0", "proc"},
		{"no WEXT, sysfs wireless", procWirelessHeadersOnly, map[string]string{"lo": "", "wlan0": "wireless"}, "wlan0", "sysfs wireless"},
		{"no proc file, sysfs phy80211", "", map[string]string{"wlan1": "phy80211"}, "wlan1", "proc unreadable"},
		{"Wi-Fi module seen as Ethernet", procWirelessHeadersOnly, map[string]string{"lo": "", "eth0": ""}, "", "sysfs has no wireless interface"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wirelessFixture(t, c.proc, c.sys)
			iface, how := wirelessIface()
			if iface != c.wantIface || !strings.Contains(how, c.wantHow) {
				t.Errorf("wirelessIface() = %q, %q; want %q with %q", iface, how, c.wantIface, c.wantHow)
			}
		})
	}
}

// boostRig is a Server whose log is captured, with a scripted source and
// scripted power-save tools.
func boostRig(t *testing.T, source string, toolOK map[string]bool) (*Server, *bytes.Buffer, *[]string) {
	t.Helper()
	var buf bytes.Buffer
	s := &Server{logger: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}
	s.boxNowReadFn = scriptNowPlaying(source, "", "")
	var calls []string
	orig := powerSaveCmd
	t.Cleanup(func() { powerSaveCmd = orig })
	powerSaveCmd = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if toolOK[name] {
			return nil, nil
		}
		return []byte("not found"), errors.New("exec: not found")
	}
	return s, &buf, &calls
}

// Every early return says why, at Info, with what it saw: the 2026-10-25 logs
// had nothing at all.
func TestBoostUploadThroughputLogsEveryBranch(t *testing.T) {
	t.Run("no wireless interface", func(t *testing.T) {
		wirelessFixture(t, procWirelessHeadersOnly, map[string]string{"eth0": ""})
		s, buf, calls := boostRig(t, "STANDBY", nil)
		s.boostUploadThroughput("agent-update")()
		if !strings.Contains(buf.String(), "no Linux wireless interface") || !strings.Contains(buf.String(), "lookup=") {
			t.Errorf("missing reason line: %s", buf)
		}
		if len(*calls) != 0 {
			t.Errorf("no tool may run without an interface, ran %v", *calls)
		}
	})
	t.Run("awake speaker", func(t *testing.T) {
		wirelessFixture(t, procWirelessWithWlan0, nil)
		s, buf, calls := boostRig(t, "UPNP", map[string]bool{"iw": true})
		s.boostUploadThroughput("agent-update")()
		if !strings.Contains(buf.String(), "not in standby") || !strings.Contains(buf.String(), "source=UPNP") || !strings.Contains(buf.String(), "iface=wlan0") {
			t.Errorf("missing reason line: %s", buf)
		}
		if len(*calls) != 0 {
			t.Errorf("an awake speaker is left alone, ran %v", *calls)
		}
	})
	t.Run("unreadable source", func(t *testing.T) {
		wirelessFixture(t, procWirelessWithWlan0, nil)
		s, buf, _ := boostRig(t, "", nil)
		s.boostUploadThroughput("agent-update")()
		if !strings.Contains(buf.String(), "could not read the speaker's source") || !strings.Contains(buf.String(), "connection refused") {
			t.Errorf("missing reason line: %s", buf)
		}
	})
	t.Run("standby, iw works", func(t *testing.T) {
		wirelessFixture(t, procWirelessHeadersOnly, map[string]string{"wlan0": "phy80211"})
		s, buf, calls := boostRig(t, "STANDBY", map[string]bool{"iw": true})
		restore := s.boostUploadThroughput("agent-update")
		if !strings.Contains(buf.String(), "tool=iwconfig") || !strings.Contains(buf.String(), "power-save off for the transfer") {
			t.Errorf("want the failed iwconfig and the iw success logged: %s", buf)
		}
		restore()
		want := []string{"iwconfig wlan0 power off", "iw dev wlan0 set power_save off", "iw dev wlan0 set power_save on"}
		if strings.Join(*calls, "|") != strings.Join(want, "|") {
			t.Errorf("tool calls %v, want %v", *calls, want)
		}
		if !strings.Contains(buf.String(), "power-save restored") {
			t.Errorf("restore not logged: %s", buf)
		}
	})
	t.Run("standby, no tool", func(t *testing.T) {
		wirelessFixture(t, procWirelessWithWlan0, nil)
		s, buf, _ := boostRig(t, "STANDBY", nil)
		s.boostUploadThroughput("agent-update")()
		if !strings.Contains(buf.String(), "no wireless power-save toggle in this firmware") {
			t.Errorf("missing reason line: %s", buf)
		}
	})
}

// A driver that adds a P2P or AP interface next to its station interface puts
// p2p0 first in directory order. The station interface is the one that is up,
// and failing that, the one with a station name.
func TestWirelessIfacePrefersTheStationInterface(t *testing.T) {
	setOper := func(t *testing.T, name, state string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(sysClassNetDir, name, "operstate"), []byte(state+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("by name", func(t *testing.T) {
		wirelessFixture(t, procWirelessHeadersOnly, map[string]string{"ap0": "phy80211", "p2p0": "phy80211", "wlan0": "phy80211"})
		iface, how := wirelessIface()
		if iface != "wlan0" || !strings.Contains(how, "picked from ap0,p2p0,wlan0") {
			t.Errorf("wirelessIface() = %q, %q; want wlan0 and the candidates named", iface, how)
		}
	})
	t.Run("by operstate", func(t *testing.T) {
		wirelessFixture(t, procWirelessHeadersOnly, map[string]string{"p2p0": "phy80211", "uap0": "phy80211", "wlan0": "phy80211"})
		setOper(t, "p2p0", "down")
		setOper(t, "uap0", "up")
		setOper(t, "wlan0", "down")
		if iface, how := wirelessIface(); iface != "uap0" {
			t.Errorf("wirelessIface() = %q, %q; want the interface that is up", iface, how)
		}
	})
	t.Run("up station wins", func(t *testing.T) {
		wirelessFixture(t, procWirelessHeadersOnly, map[string]string{"p2p0": "phy80211", "wlan0": "phy80211"})
		setOper(t, "p2p0", "up")
		setOper(t, "wlan0", "up")
		if iface, _ := wirelessIface(); iface != "wlan0" {
			t.Errorf("wirelessIface() = %q, want wlan0", iface)
		}
	})
}
