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
//   1. TCP :22 with a 1.2s budget. Cheap; if sshd is not answering
//      the install will fail downstream anyway, so save the user that
//      detour.
//   2. HTTP GET :8090/info with a 2s budget for friendly-name +
//      model. Best-effort; failure is fine, we still surface the box.
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
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", sshPort))
	conn, err := net.DialTimeout("tcp", addr, sshTimeout)
	if err != nil {
		return BoxInfo{}, false
	}
	_ = conn.Close()

	box := BoxInfo{
		Host:         host,
		Port:         infoPort,
		Kind:         "stock",
		FriendlyName: "Bose SoundTouch (setup AP)",
		Model:        "SoundTouch",
	}

	// Best-effort enrichment from /info. Pulled separately so a slow
	// or absent BoseApp does not block the probe: we already know
	// the speaker is there because SSH answered.
	ctx, cancel := context.WithTimeout(context.Background(), infoTimeout)
	defer cancel()
	if name, model, deviceID, ok := fetchBoseInfoAt(ctx, host, infoPort); ok {
		if name != "" {
			box.FriendlyName = name
		}
		if model != "" {
			box.Model = model
		}
		box.DeviceID = deviceID
	}
	return box, true
}

var (
	boseInfoNameRe   = regexp.MustCompile(`<name>([^<]+)</name>`)
	boseInfoTypeRe   = regexp.MustCompile(`<type>([^<]+)</type>`)
	boseInfoDevIDRe  = regexp.MustCompile(`deviceID="([0-9A-Fa-f]+)"`)
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
