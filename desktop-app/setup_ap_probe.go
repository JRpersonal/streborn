// Setup-AP probe: a factory-fresh Bose SoundTouch Portable (variant
// "taigan", BCO chassis with no Ethernet jack) cannot join the user's
// home Wi-Fi until STR's stick install runs once. Until then the
// speaker broadcasts its own setup AP ("Bose SoundTouch Wi-Fi
// Network") at 192.168.1.1 and is unreachable from the user's home
// LAN. mDNS-only DiscoverBoxes never sees it.
//
// This file adds the smallest possible piece of UX-glue: when the
// user temporarily joins their laptop to the Bose setup AP (handled
// outside the app — Windows/macOS Wi-Fi picker), the Setup tab probes
// 192.168.1.1:22 every few seconds in parallel with mDNS. On a hit it
// reads /info to learn the speaker model and surfaces a synthetic
// BoxInfo that the existing install flow consumes unchanged.
//
// Deliberately NOT a Wi-Fi-network scan: PR #80 ripped out the
// netsh-wlan-show-networks path because Windows 11 24H2 ties it to
// the Location permission, and a music app prompting for location is
// a real trust hit. A targeted TCP dial against a known IP needs no
// such permission and is invisible from the OS perspective.

package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"time"
)

// SetupAPHost is the IP the Bose firmware uses for its captive setup
// AP on every SoundTouch variant we have seen. Documented in the
// Bose firmware's network_default.lua dhcpd block; matches the IP a
// laptop joined to "Bose SoundTouch Wi-Fi Network" gets as its
// gateway.
const SetupAPHost = "192.168.1.1"

// ProbeSetupAP does a fast TCP probe on the well-known Bose setup-AP
// IP and, on hit, returns a BoxInfo the Setup tab can feed straight
// into InstallSTROnBox. Returns ok=false silently when the address
// is unreachable — that's the common case (user not joined to the
// setup AP) and must not look like an error.
//
// Probe order:
//  1. HTTP GET :8090/info with a 2s budget. If the body contains
//     Bose XML we have found a setup-AP, regardless of whether sshd
//     is up. SSH used to be the gate here, but live-verified
//     2026-05-30 on a factory-reset taigan: sshd is NOT running on
//     the box's first boot until the stick's remote_services file
//     has been read by shelby_local, which only happens on REBOOT
//     with a successfully-mounted stick. On a Portable with a
//     defective USB-C adapter the mount never happens, sshd never
//     starts, and the SSH-only gate hid the box entirely from the
//     Setup tab. The Setup-AP WLAN push flow does not need sshd —
//     it just needs /info to answer — so :8090 is the right gate.
//  2. SSH probe is now a secondary signal that the caller uses to
//     decide whether the post-push "install STR" step is possible
//     from here, but it is not required for surfacing the box.
func (a *App) ProbeSetupAP() (BoxInfo, bool) {
	return probeSetupAPAt(SetupAPHost, 1200*time.Millisecond, 2*time.Second)
}

func probeSetupAPAt(host string, sshTimeout, infoTimeout time.Duration) (BoxInfo, bool) {
	return probeSetupAPAtPorts(host, 22, 8090, sshTimeout, infoTimeout)
}

// probeSetupAPAtPorts is the testable core. Production callers go
// through probeSetupAPAt with the well-known Bose ports; tests pass
// ephemeral httptest ports to avoid needing privileged sockets.
func probeSetupAPAtPorts(host string, sshPort, infoPort int, sshTimeout, infoTimeout time.Duration) (BoxInfo, bool) {
	// /info on :8090 is the discriminator: it returns Bose XML
	// whenever the box is up. The earlier port-22 probe rejected
	// every factory-reset Portable because sshd is not running on
	// first boot.
	ctx, cancel := context.WithTimeout(context.Background(), infoTimeout)
	defer cancel()
	name, model, deviceID, ok := fetchBoseInfoAt(ctx, host, infoPort)
	if !ok {
		return BoxInfo{}, false
	}

	// Best-effort SSH probe so the caller can tell "install possible
	// from here" vs "WLAN push only". Surfaced through ReachableSSH
	// on the BoxInfo (used by the Setup tab UX).
	sshOK := false
	sshAddr := net.JoinHostPort(host, fmt.Sprintf("%d", sshPort))
	if conn, err := net.DialTimeout("tcp", sshAddr, sshTimeout); err == nil {
		_ = conn.Close()
		sshOK = true
	}
	_ = sshOK // reserved for future BoxInfo enrichment

	box := BoxInfo{
		Host:         host,
		Port:         infoPort,
		Kind:         "stock",
		FriendlyName: "Bose SoundTouch (setup AP)",
		Model:        "SoundTouch",
	}
	if name != "" {
		box.FriendlyName = name
	}
	if model != "" {
		box.Model = model
	}
	box.DeviceID = deviceID
	return box, true
}

var (
	boseInfoNameRe  = regexp.MustCompile(`<name>([^<]+)</name>`)
	boseInfoTypeRe  = regexp.MustCompile(`<type>([^<]+)</type>`)
	boseInfoDevIDRe = regexp.MustCompile(`deviceID="([0-9A-Fa-f]+)"`)
)

func fetchBoseInfo(ctx context.Context, host string) (name, model, deviceID string, ok bool) {
	return fetchBoseInfoAt(ctx, host, 8090)
}

func fetchBoseInfoAt(ctx context.Context, host string, port int) (name, model, deviceID string, ok bool) {
	url := fmt.Sprintf("http://%s:%d/info", host, port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", "", false
	}
	client := http.Client{Timeout: 0} // ctx carries the deadline
	resp, err := client.Do(req)
	if err != nil {
		return "", "", "", false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if err != nil {
		return "", "", "", false
	}
	if m := boseInfoNameRe.FindSubmatch(body); len(m) == 2 {
		name = string(m[1])
	}
	if m := boseInfoTypeRe.FindSubmatch(body); len(m) == 2 {
		model = string(m[1])
	}
	if m := boseInfoDevIDRe.FindSubmatch(body); len(m) == 2 {
		deviceID = string(m[1])
	}
	return name, model, deviceID, true
}
