package webui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/JRpersonal/streborn/internal/netutil"
)

// The addresses this speaker is reachable at, and which of them sit on the
// network cable (#165). A speaker on a cable that also has Wi-Fi configured
// answers on two addresses, and the desktop app found it twice. The app merges
// the two by deviceID, but only the speaker can say which address is the
// cable, so the version envelope carries the answer.
//
// The interface NAME cannot decide it. A wireless chassis can hand its radio to
// Linux as eth0 (the Wi-Fi module does its own networking), so "eth0 = cable"
// would call a Portable wired. The kernel's own wireless markers decide it
// instead, and an address is reported as wired only when ANOTHER interface
// carrying an address is positively wireless. A speaker without a visible
// Linux radio therefore reports no wired address at all, and the app falls
// back to its reachability pick, which is the honest answer when the speaker
// itself cannot tell.

// lanIfaceAddr is one usable LAN address and the interface carrying it.
type lanIfaceAddr struct {
	iface string
	ip    string
}

// splitWiredAddrs returns every address in addrs (deduped, sorted) and the
// subset on a wired interface. isWireless classifies an interface by name.
// wired stays empty unless at least one address sits on a wireless interface,
// see the file comment.
func splitWiredAddrs(addrs []lanIfaceAddr, isWireless func(string) bool) (all, wired []string) {
	seenAll := map[string]bool{}
	seenWired := map[string]bool{}
	var wiredCand []string
	haveWireless := false
	for _, a := range addrs {
		if a.ip == "" {
			continue
		}
		if !seenAll[a.ip] {
			seenAll[a.ip] = true
			all = append(all, a.ip)
		}
		if isWireless(a.iface) {
			haveWireless = true
			continue
		}
		if !seenWired[a.ip] {
			seenWired[a.ip] = true
			wiredCand = append(wiredCand, a.ip)
		}
	}
	sort.Strings(all)
	if !haveWireless {
		return all, nil
	}
	// An address on BOTH a wireless and a wired interface (a bridge) is not a
	// cable-only address; leave it out rather than guess.
	for _, ip := range wiredCand {
		ambiguous := false
		for _, a := range addrs {
			if a.ip == ip && isWireless(a.iface) {
				ambiguous = true
				break
			}
		}
		if !ambiguous {
			wired = append(wired, ip)
		}
	}
	sort.Strings(wired)
	return all, wired
}

// ifaceIsWireless reports whether the kernel marks the interface as a WLAN:
// listed in /proc/net/wireless, or carrying a "wireless" or "phy80211" entry in
// sysfs. Same two views as wirelessIface, asked about one name.
func ifaceIsWireless(name string) bool {
	if name == "" {
		return false
	}
	if b, err := os.ReadFile(procNetWirelessPath); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if i := strings.IndexByte(line, ':'); i > 0 && strings.TrimSpace(line[:i]) == name {
				return true
			}
		}
	}
	for _, marker := range []string{"wireless", "phy80211"} {
		if _, err := os.Stat(filepath.Join(sysClassNetDir, name, marker)); err == nil {
			return true
		}
	}
	return false
}

// speakerLANAddrs is the live answer for the version envelope: every address
// the speaker can be reached at and the wired subset, each comma-joined.
func speakerLANAddrs() (all, wired string) {
	var addrs []lanIfaceAddr
	for _, c := range netutil.LANIfaceAddrs(false) {
		addrs = append(addrs, lanIfaceAddr{iface: c.Iface.Name, ip: c.IP.String()})
	}
	a, w := splitWiredAddrs(addrs, ifaceIsWireless)
	return strings.Join(a, ","), strings.Join(w, ",")
}
