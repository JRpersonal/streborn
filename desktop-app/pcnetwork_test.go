package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The LAN every test below sits on: a PC with a normal Wi-Fi address next to
// the speaker, in the documentation range.
const (
	testSpeaker = "192.0.2.34"
	testPCAddr  = "192.0.2.22"
)

var testWiFi = localIface{Name: "Wi-Fi", CIDR: testPCAddr + "/24", Up: true}

// withFakePC swaps the interface list and the routing table for the test.
func withFakePC(t *testing.T, ifaces []localIface, r pcRoutes) {
	t.Helper()
	oldIfaces, oldRoutes := listPCIfaces, probePCRoutes
	listPCIfaces = func() []localIface { return ifaces }
	probePCRoutes = func(string) pcRoutes { return r }
	t.Cleanup(func() { listPCIfaces, probePCRoutes = oldIfaces, oldRoutes })
}

func TestTunnelIfaceRecognisesVPNAdaptersAndNothingElse(t *testing.T) {
	tunnels := []localIface{
		{Name: "utun4", CIDR: "10.8.0.6/32"},
		{Name: "tun0", CIDR: "10.8.0.6/24"},
		{Name: "tap0", CIDR: "10.8.0.6/24"},
		{Name: "wg0", CIDR: "10.66.0.2/32"},
		{Name: "ppp0", CIDR: "10.64.64.64/32"},
		{Name: "NordLynx", CIDR: "10.5.0.2/32"},
		{Name: "OpenVPN TAP-Windows6", CIDR: "10.8.0.6/24"},
		{Name: "ProtonVPN TUN", CIDR: "10.2.0.2/32"},
		{Name: "WireGuard Tunnel", CIDR: "10.66.0.2/32"},
		{Name: "Mullvad", CIDR: "10.64.0.2/32"},
		{Name: "Ethernet 3", CIDR: "10.0.0.9/24", Virtual: true},
		{Name: "Ethernet 4", CIDR: "100.101.102.103/32"},
	}
	for _, in := range tunnels {
		if !tunnelIface(in) {
			t.Errorf("%q (%s) must read as a tunnel", in.Name, in.CIDR)
		}
	}
	normal := []localIface{
		{Name: "Wi-Fi", CIDR: "192.0.2.22/24"},
		{Name: "WLAN", CIDR: "192.0.2.22/24"},
		{Name: "Ethernet", CIDR: "192.0.2.22/24"},
		{Name: "en0", CIDR: "192.0.2.22/24"},
		{Name: "wlan0", CIDR: "192.0.2.22/24"},
		{Name: "enp0s31f6", CIDR: "192.0.2.22/24"},
		{Name: "vEthernet (WSL)", CIDR: "172.28.16.1/20"},
		{Name: "bridge0", CIDR: "192.0.2.1/24"},
	}
	for _, in := range normal {
		if tunnelIface(in) {
			t.Errorf("%q must not read as a tunnel", in.Name)
		}
	}
}

func TestFindVPNOnlyBlamesATunnelThatHoldsTheRoute(t *testing.T) {
	nord := localIface{Name: "NordLynx", CIDR: "10.5.0.2/32", Up: true, Virtual: true}
	cases := []struct {
		name         string
		ifaces       []localIface
		routes       pcRoutes
		want         bool
		speakerRoute bool
	}{
		{
			// The mail case: the speaker route still looks local, but the
			// VPN holds the default route and its kill switch drops the LAN.
			name:   "VPN holds the default route",
			ifaces: []localIface{testWiFi, nord},
			routes: pcRoutes{SpeakerSrc: testPCAddr, DefaultSrc: "10.5.0.2"},
			want:   true,
		},
		{
			name:         "VPN carries the speaker connection itself",
			ifaces:       []localIface{testWiFi, nord},
			routes:       pcRoutes{SpeakerSrc: "10.5.0.2", DefaultSrc: "10.5.0.2"},
			want:         true,
			speakerRoute: true,
		},
		{
			// Tailscale without an exit node: a /32 on a utun, no default
			// route. Common, harmless, never to be blamed.
			name:   "Tailscale /32 without default route",
			ifaces: []localIface{testWiFi, {Name: "utun5", CIDR: "100.101.102.103/32", Up: true, Virtual: true}},
			routes: pcRoutes{SpeakerSrc: testPCAddr, DefaultSrc: testPCAddr},
		},
		{
			name:   "macOS system utun interfaces, no IPv4 route",
			ifaces: []localIface{testWiFi, {Name: "utun0", Up: true, Virtual: true}, {Name: "utun1", Up: true, Virtual: true}},
			routes: pcRoutes{SpeakerSrc: testPCAddr, DefaultSrc: testPCAddr},
		},
		{
			name:   "tunnel adapter that is down",
			ifaces: []localIface{testWiFi, {Name: "NordLynx", CIDR: "10.5.0.2/32", Up: false, Virtual: true}},
			routes: pcRoutes{SpeakerSrc: testPCAddr, DefaultSrc: "10.5.0.2"},
		},
		{
			// A TAP bridged into the home LAN, or an odd adapter without a
			// MAC: it IS the speaker's network, not a VPN taking traffic away.
			name:   "tunnel-shaped adapter on the speaker's own network",
			ifaces: []localIface{{Name: "tap0", CIDR: testPCAddr + "/24", Up: true, Virtual: true}},
			routes: pcRoutes{SpeakerSrc: testPCAddr, DefaultSrc: testPCAddr},
		},
		{
			name:   "no route information at all",
			ifaces: []localIface{testWiFi, nord},
		},
	}
	for _, c := range cases {
		v, ok := findVPN(testSpeaker, c.ifaces, c.routes)
		if ok != c.want {
			t.Errorf("%s: found=%v, want %v", c.name, ok, c.want)
			continue
		}
		if ok && v.SpeakerRoute != c.speakerRoute {
			t.Errorf("%s: SpeakerRoute=%v, want %v", c.name, v.SpeakerRoute, c.speakerRoute)
		}
	}
}

func TestVPNMayExplainMatchesTheErrorsAVPNProduces(t *testing.T) {
	def := vpnFinding{Iface: localIface{Name: "NordLynx"}}
	route := vpnFinding{Iface: localIface{Name: "NordLynx"}, SpeakerRoute: true}
	explains := []string{
		"dial tcp 192.0.2.34:8888: i/o timeout",
		"context deadline exceeded (Client.Timeout exceeded while awaiting headers)",
		"dial tcp 192.0.2.34:8888: connect: can't assign requested address",
		"dial tcp 192.0.2.34:8888: connect: network is unreachable",
		"dial tcp 192.0.2.34:8888: connect: no route to host",
		"dial tcp 192.0.2.34:8888: connectex: An attempt was made to access a socket in a way forbidden by its access permissions.",
	}
	for _, m := range explains {
		if !vpnMayExplain(errors.New(m), def) {
			t.Errorf("a default-route VPN should explain %q", m)
		}
		if !vpnMayExplain(errors.New(m), route) {
			t.Errorf("a speaker-route VPN should explain %q", m)
		}
	}
	answered := []string{
		"dial tcp 192.0.2.34:8888: connect: connection refused",
		"read tcp 192.0.2.22:5000->192.0.2.34:8888: read: connection reset by peer",
	}
	for _, m := range answered {
		if vpnMayExplain(errors.New(m), def) || vpnMayExplain(errors.New(m), route) {
			t.Errorf("an answer from the speaker (%q) rules the VPN out", m)
		}
	}
	// Something unrelated is not the default-route VPN's doing; with the
	// speaker route inside the tunnel, any failure short of an answer is.
	odd := errors.New("unexpected EOF")
	if vpnMayExplain(odd, def) {
		t.Error("a default-route VPN must not explain an unrelated error")
	}
	if !vpnMayExplain(odd, route) {
		t.Error("a VPN carrying the speaker connection explains any failure short of an answer")
	}
}

func TestPCSideAdviceNamesTheMacLocalNetworkPermission(t *testing.T) {
	enetunreach := errors.New("dial tcp 192.0.2.34:8888: connect: network is unreachable")
	ehostunreach := errors.New("dial tcp 192.0.2.34:8888: connect: no route to host")
	mac := pcRoutes{OS: "darwin", SpeakerSrc: testPCAddr, DefaultSrc: testPCAddr}
	en0 := []localIface{{Name: "en0", CIDR: testPCAddr + "/24", Up: true}}

	for _, err := range []error{enetunreach, ehostunreach} {
		if adv, _ := pcSideAdvice(testSpeaker, err, en0, mac); adv != macLocalNetworkAdvice {
			t.Errorf("darwin, same subnet, %q: want the Local Network advice, got %q", err, adv)
		}
	}

	// Not a Mac: the permission does not exist there.
	win := mac
	win.OS = "windows"
	if adv, _ := pcSideAdvice(testSpeaker, enetunreach, en0, win); adv != "" {
		t.Errorf("windows must not get the macOS advice, got %q", adv)
	}
	// The speaker is in another network: "no route" is then plausibly true.
	if adv, _ := pcSideAdvice("198.51.100.7", enetunreach, en0, mac); adv != "" {
		t.Errorf("a speaker outside the Mac's networks is not the permission, got %q", adv)
	}
	// The Wi-Fi is down: that is a Mac without network, the old verdict.
	down := []localIface{{Name: "en0", CIDR: testPCAddr + "/24", Up: false}}
	if adv, _ := pcSideAdvice(testSpeaker, enetunreach, down, mac); adv != "" {
		t.Errorf("a down interface is no network, got %q", adv)
	}
	// Other errors are not the permission's fingerprint.
	for _, m := range []string{"connect: host is down", "i/o timeout", "connect: connection refused"} {
		if adv, _ := pcSideAdvice(testSpeaker, errors.New(m), en0, mac); adv != "" {
			t.Errorf("%q is not the permission fingerprint, got %q", m, adv)
		}
	}
	// A VPN holding the route outranks the permission: granting it would
	// change nothing while the tunnel takes the traffic.
	withVPN := append([]localIface{}, en0...)
	withVPN = append(withVPN, localIface{Name: "utun6", CIDR: "10.5.0.2/32", Up: true, Virtual: true})
	vpnMac := mac
	vpnMac.DefaultSrc = "10.5.0.2"
	adv, why := pcSideAdvice(testSpeaker, ehostunreach, withVPN, vpnMac)
	if !strings.HasPrefix(adv, vpnAdvice) || !strings.Contains(adv, "utun6") {
		t.Errorf("a Mac with a VPN holding the default route: want the VPN advice naming utun6, got %q", adv)
	}
	if !strings.Contains(why, "utun6") {
		t.Errorf("the log reason must name the adapter, got %q", why)
	}
}

func TestReachabilityHintForUsesThePCSideVerdicts(t *testing.T) {
	a := &App{}

	t.Run("NordVPN default route, speaker times out", func(t *testing.T) {
		withFakePC(t,
			[]localIface{testWiFi, {Name: "NordLynx", CIDR: "10.5.0.2/32", Up: true, Virtual: true}},
			pcRoutes{OS: "windows", SpeakerSrc: testPCAddr, DefaultSrc: "10.5.0.2"})
		got := a.reachabilityHintFor(testSpeaker, errors.New("dial tcp 192.0.2.34:8888: i/o timeout")).Error()
		if !strings.Contains(got, vpnAdvice) || !strings.Contains(got, "NordLynx") {
			t.Fatalf("want the VPN advice naming NordLynx, got %q", got)
		}
		if strings.Contains(got, firewallAdvice) {
			t.Error("the VPN verdict replaces the firewall paragraph")
		}
	})

	t.Run("EADDRNOTAVAIL with a VPN names the VPN", func(t *testing.T) {
		withFakePC(t,
			[]localIface{testWiFi, {Name: "utun4", CIDR: "10.8.0.6/24", Up: true, Virtual: true}},
			pcRoutes{OS: "darwin", DefaultSrc: "10.8.0.6"})
		got := a.reachabilityHintFor(testSpeaker, errors.New("dial tcp 192.0.2.34:8888: connect: can't assign requested address")).Error()
		if !strings.Contains(got, vpnAdvice) || strings.Contains(got, noLocalRouteAdvice) {
			t.Fatalf("want the specific VPN advice, got %q", got)
		}
	})

	t.Run("Mac without Local Network permission", func(t *testing.T) {
		withFakePC(t,
			[]localIface{{Name: "en0", CIDR: testPCAddr + "/24", Up: true}},
			pcRoutes{OS: "darwin", SpeakerSrc: testPCAddr, DefaultSrc: testPCAddr})
		err := a.reachabilityHintFor(testSpeaker, errors.New("dial tcp 192.0.2.34:8888: connect: network is unreachable"))
		got := err.Error()
		if !strings.Contains(got, macLocalNetworkAdvice) {
			t.Fatalf("want the Local Network advice, got %q", got)
		}
		if strings.Contains(got, noNetworkAdvice) {
			t.Error("a Mac with a LAN address must not be told it has no network connection")
		}
		// ota.go still recognises the error family, so it still declines
		// the SSH escalation (which macOS would refuse just the same).
		if !noNetworkHere(err) {
			t.Error("the wrapped error must keep its ENETUNREACH text")
		}
	})

	t.Run("Tailscale is not blamed", func(t *testing.T) {
		withFakePC(t,
			[]localIface{testWiFi, {Name: "Tailscale", CIDR: "100.101.102.103/32", Up: true, Virtual: true}},
			pcRoutes{OS: "windows", SpeakerSrc: testPCAddr, DefaultSrc: testPCAddr})
		got := a.reachabilityHintFor(testSpeaker, errors.New("dial tcp 192.0.2.34:8888: i/o timeout")).Error()
		if strings.Contains(got, vpnAdvice) {
			t.Fatalf("a mesh VPN that holds no route must not be blamed, got %q", got)
		}
		if !strings.Contains(got, firewallAdvice) {
			t.Errorf("without a PC-side cause the old advice stays, got %q", got)
		}
	})

	t.Run("an answer is never the PC's fault", func(t *testing.T) {
		withFakePC(t,
			[]localIface{testWiFi, {Name: "NordLynx", CIDR: "10.5.0.2/32", Up: true, Virtual: true}},
			pcRoutes{OS: "windows", SpeakerSrc: "10.5.0.2", DefaultSrc: "10.5.0.2"})
		got := a.reachabilityHintFor(testSpeaker, errors.New("status 400 Bad Request body=\"\"")).Error()
		if strings.Contains(got, vpnAdvice) || !strings.Contains(got, answeredNotSTRAdvice) {
			t.Fatalf("a speaker that answered keeps the answered advice, got %q", got)
		}
	})

	t.Run("no host and cancel keep the old behaviour", func(t *testing.T) {
		withFakePC(t,
			[]localIface{{Name: "en0", CIDR: testPCAddr + "/24", Up: true}},
			pcRoutes{OS: "darwin", SpeakerSrc: testPCAddr})
		if got := a.reachabilityHintFor("", errors.New("connect: network is unreachable")).Error(); !strings.Contains(got, noNetworkAdvice) {
			t.Errorf("without a host there is no subnet to compare, got %q", got)
		}
		if err := a.reachabilityHintFor(testSpeaker, context.Canceled); !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), macLocalNetworkAdvice) {
			t.Errorf("a cancelled request gets no advice, got %v", err)
		}
	})
}

func TestPCSideAdviceTextFollowsTheWordingRules(t *testing.T) {
	for _, s := range []string{vpnAdvice, vpnAdapterNote, macLocalNetworkAdvice} {
		for _, bad := range []string{"—", "–", " - ", "2.4", "5 GHz", "WPA"} {
			if strings.Contains(s, bad) {
				t.Errorf("advice contains %q: %q", bad, s)
			}
		}
	}
	if !strings.Contains(macLocalNetworkAdvice, "System Settings > Privacy & Security > Local Network") ||
		!strings.Contains(macLocalNetworkAdvice, "ST Reborn") {
		t.Error("the macOS advice must name the exact settings path and the app")
	}
	if !strings.Contains(vpnAdvice, "split tunnelling") || !strings.Contains(vpnAdvice, "Disconnect the VPN") {
		t.Error("the VPN advice must name both ways out")
	}
}
