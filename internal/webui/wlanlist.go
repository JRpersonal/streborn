package webui

// Which networks is this speaker actually configured for?
//
// The theory this exists to test: a speaker that keeps joining the guest
// network is not misbehaving, it simply has more than one network configured
// and picks whichever it sees first. Plausible, repeatedly suspected, and until
// now not testable from a diagnostic bundle at all.
//
// Two things had to be learned before it could be answered.
//
// The bundle read /mnt/nv/wpa_supplicant.conf while the code that writes the
// file uses /etc/wpa_supplicant.conf, so every bundle ever collected carried
// "no such file or directory" for this and nobody noticed. Fixed separately.
//
// And even the right file is not the answer on every chassis. On a rhino ST10
// (live, 2026-08-05) /etc/wpa_supplicant.conf is the untouched vendor template
// with NO network block at all, while the speaker is happily on Wi-Fi. It runs
// with update_config=1 and a control interface, which means the network was
// added at runtime through wpa_cli and never written back to disk. So the file
// tells you what was persisted, and the control interface tells you what the
// speaker is actually using. Both are worth having, and they disagree.
//
// Read-only on purpose. Removing a network is the point of the exercise, but it
// is also a way to lock a speaker out of the only network it can reach, so it
// waits until this has reported real data from real speakers.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// wlanNetwork is one configured network as wpa_supplicant reports it.
type wlanNetwork struct {
	ID      int    `json:"id"`
	SSID    string `json:"ssid"`
	BSSID   string `json:"bssid,omitempty"`
	Flags   string `json:"flags,omitempty"`
	Current bool   `json:"current"`
}

// wlanConfigured is the whole picture for one speaker.
type wlanConfigured struct {
	// Tool is empty when wpa_cli is not on this box, which is itself the
	// answer for that chassis and must be visible rather than looking like
	// "no networks".
	Tool      string        `json:"tool"`
	Interface string        `json:"interface,omitempty"`
	Networks  []wlanNetwork `json:"networks"`
	// FileBlocks counts network={} blocks in the persisted config, so the two
	// sources can be compared at a glance.
	FileBlocks int    `json:"fileBlocks"`
	Err        string `json:"err,omitempty"`
}

var wlanNetLine = regexp.MustCompile(`^(\d+)\t([^\t]*)\t([^\t]*)\t?(.*)$`)

// wlanInterfaces are tried in order. taigan calls its wireless interface eth0,
// which is the sort of thing that makes a hardcoded wlan0 quietly report
// nothing on a whole model line.
var wlanInterfaces = []string{"wlan0", "eth0", "wlan1", "ra0"}

// listConfiguredWLANs asks wpa_supplicant what it is configured for.
func listConfiguredWLANs(ctx context.Context, confPath string) wlanConfigured {
	out := wlanConfigured{Networks: []wlanNetwork{}}
	if b, err := os.ReadFile(confPath); err == nil {
		txt := string(b)
		out.FileBlocks = strings.Count(txt, "network={") + strings.Count(txt, "network = {")
	}

	tool, err := exec.LookPath("wpa_cli")
	if err != nil {
		// No wpa_cli means a coprocessor chassis (scm: taigan Portable, mojo
		// ST30), where the radio is not driven by wpa_supplicant at all and
		// the stored profiles live in BoseApp's own files instead. Measured
		// 2026-08-05: an ST30 and a Portable both report no wpa_cli while
		// /networkInfo says wifiProfileCount=2 and 1. So fall through to the
		// files rather than reporting "no networks", which would be a lie of
		// exactly the shape that hides a second, unwanted network.
		out.Tool = "BoseApp-Persistence"
		out.Networks = append(out.Networks, bcoStoredProfiles()...)
		if len(out.Networks) == 0 {
			out.Err = "no wpa_cli and no stored profile files on this speaker"
		}
		return out
	}
	out.Tool = tool

	for _, iface := range wlanInterfaces {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		raw, err := exec.CommandContext(cctx, tool, "-i", iface, "list_networks").CombinedOutput()
		cancel()
		if err != nil || strings.Contains(string(raw), "Failed to connect") {
			continue
		}
		out.Interface = iface
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimRight(line, "\r")
			if line == "" || strings.HasPrefix(line, "network id") || strings.HasPrefix(line, "Selected") {
				continue
			}
			m := wlanNetLine.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			id, cerr := strconv.Atoi(m[1])
			if cerr != nil {
				continue
			}
			flags := strings.TrimSpace(m[4])
			out.Networks = append(out.Networks, wlanNetwork{
				ID: id, SSID: m[2], BSSID: strings.TrimSpace(m[3]), Flags: flags,
				Current: strings.Contains(flags, "[CURRENT]"),
			})
		}
		return out
	}
	out.Err = "wpa_cli found no control interface (tried " + strings.Join(wlanInterfaces, ", ") + ")"
	return out
}

// bcoStoredProfiles lists the networks a coprocessor speaker has on file.
//
// Two files carry them, and they are read in the same order provisionedSSID
// uses, because that order is what the box itself prefers:
//
//	NetworkProfiles.xml      NetManager's own record, one <profile> per network
//	AirplayConfiguration.xml the WAC onboarding outcome, PersistentWifiProfile
//
// Nothing is deduplicated across the two on purpose: seeing the same SSID from
// both sources is information, and seeing DIFFERENT ones is the finding this
// whole exercise is for.
func bcoStoredProfiles() []wlanNetwork {
	var out []wlanNetwork
	seen := map[string]bool{}
	add := func(ssid, origin string) {
		ssid = strings.TrimSpace(ssid)
		if ssid == "" || seen[origin+"|"+ssid] {
			return
		}
		seen[origin+"|"+ssid] = true
		out = append(out, wlanNetwork{ID: len(out), SSID: ssid, Flags: origin})
	}
	for _, spec := range []struct{ glob, origin string }{
		{"/mnt/nv/BoseApp-Persistence/*/NetworkProfiles.xml", "NetworkProfiles"},
		{"/mnt/nv/BoseApp-Persistence/*/AirplayConfiguration.xml", "AirplayConfiguration"},
	} {
		matches, _ := filepath.Glob(spec.glob)
		for _, f := range matches {
			b, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			for _, ssid := range allSSIDs(string(b)) {
				add(ssid, spec.origin)
			}
		}
	}
	return out
}

// ssidAttr matches every ssid="..." attribute, wherever it sits. Deliberately
// not anchored to an element name: the two files spell the surrounding element
// differently and a third one would be missed by an anchored parse.
var ssidAttr = regexp.MustCompile(`(?i)ssid\s*=\s*"([^"]*)"`)

func allSSIDs(s string) []string {
	var out []string
	for _, m := range ssidAttr.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}
