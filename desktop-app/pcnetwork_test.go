package main

import (
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
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
		{
			// The classic subnet clash: the VPN pushes the same /24 as the
			// home network, and the routing table sends the speaker into
			// the tunnel although the Wi-Fi sits right next to it.
			name:         "VPN pushes the home network's own range",
			ifaces:       []localIface{testWiFi, {Name: "utun6", CIDR: "192.0.2.50/24", Up: true, Virtual: true}},
			routes:       pcRoutes{SpeakerSrc: "192.0.2.50", DefaultSrc: testPCAddr},
			want:         true,
			speakerRoute: true,
		},
		{
			// A down Wi-Fi in the same range is no clash: the tunnel-shaped
			// adapter is then the only way onto that network.
			name:   "tunnel-shaped adapter on the speaker's network, Wi-Fi down",
			ifaces: []localIface{{Name: "Wi-Fi", CIDR: testPCAddr + "/24", Up: false}, {Name: "tap0", CIDR: "192.0.2.50/24", Up: true, Virtual: true}},
			routes: pcRoutes{SpeakerSrc: "192.0.2.50", DefaultSrc: "192.0.2.50"},
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
		if !vpnMayExplain(errors.New(m), def, pcEvidence{}) {
			t.Errorf("a default-route VPN should explain %q", m)
		}
		if !vpnMayExplain(errors.New(m), route, pcEvidence{}) {
			t.Errorf("a speaker-route VPN should explain %q", m)
		}
	}
	answered := []string{
		"dial tcp 192.0.2.34:8888: connect: connection refused",
		"read tcp 192.0.2.22:5000->192.0.2.34:8888: read: connection reset by peer",
	}
	for _, m := range answered {
		if vpnMayExplain(errors.New(m), def, pcEvidence{}) || vpnMayExplain(errors.New(m), route, pcEvidence{}) {
			t.Errorf("an answer from the speaker (%q) rules the VPN out", m)
		}
	}
	// Something unrelated is not the default-route VPN's doing; with the
	// speaker route inside the tunnel, any failure short of an answer is.
	odd := errors.New("unexpected EOF")
	if vpnMayExplain(odd, def, pcEvidence{}) {
		t.Error("a default-route VPN must not explain an unrelated error")
	}
	if !vpnMayExplain(odd, route, pcEvidence{}) {
		t.Error("a VPN carrying the speaker connection explains any failure short of an answer")
	}

	// A full-tunnel VPN that lets the home network through looks the same
	// in the routing table. With another speaker answering, a timeout or
	// "no route to host" is this speaker's own silence (off, asleep), not
	// the VPN; the errors only a local block or a missing route produce
	// still count.
	other := pcEvidence{OtherAnswered: true}
	for _, m := range []string{
		"dial tcp 192.0.2.34:8888: i/o timeout",
		"dial tcp 192.0.2.34:8888: connect: no route to host",
	} {
		if vpnMayExplain(errors.New(m), def, other) {
			t.Errorf("another speaker answered: a default-route VPN must not explain %q", m)
		}
		if !vpnMayExplain(errors.New(m), route, other) {
			t.Errorf("a VPN carrying the speaker connection still explains %q", m)
		}
	}
	for _, m := range []string{
		"dial tcp 192.0.2.34:8888: connectex: An attempt was made to access a socket in a way forbidden by its access permissions.",
		"dial tcp 192.0.2.34:8888: connect: network is unreachable",
	} {
		if !vpnMayExplain(errors.New(m), def, other) {
			t.Errorf("a local block or missing route is the VPN's even with another speaker answering: %q", m)
		}
	}
}

// localizedErr is an OS error the way a non-English Windows renders it: the
// number is there, the English words are not.
type localizedErr struct {
	errno syscall.Errno
	text  string
}

func (e localizedErr) Error() string { return e.text }
func (e localizedErr) Unwrap() error { return e.errno }

func TestErrorFamiliesAreReadFromTheErrnoOnANonEnglishWindows(t *testing.T) {
	wrap := func(n syscall.Errno, text string) error {
		return &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connectex", Err: localizedErr{n, text}}}
	}
	kill := wrap(wsaEACCES, "Der Zugriff auf einen Socket war aufgrund der Zugriffsrechte des Sockets unzulässig.")
	if !blockedLocally(kill) {
		t.Error("WSAEACCES in German must read as a local block")
	}
	if !vpnMayExplain(kill, vpnFinding{}, pcEvidence{OtherAnswered: true}) {
		t.Error("a German kill-switch block must reach the VPN verdict")
	}
	if !netUnreachable(wrap(wsaENETUNREACH, "Ein Socketvorgang bezog sich auf ein nicht verfügbares Netzwerk.")) {
		t.Error("WSAENETUNREACH in German must read as network unreachable")
	}
	if !noRouteToHost(wrap(wsaEHOSTUNREACH, "Ein Socketvorgang bezog sich auf einen nicht verfügbaren Host.")) {
		t.Error("WSAEHOSTUNREACH in German must read as no route to host")
	}
	if !noLocalAddr(wrap(wsaEADDRNOTAVAIL, "Die angeforderte Adresse ist in diesem Kontext ungültig.")) {
		t.Error("WSAEADDRNOTAVAIL in German must read as no local address")
	}
	refused := wrap(wsaECONNREFUSED, "Es konnte keine Verbindung hergestellt werden, da der Zielcomputer die Verbindung verweigerte.")
	if !refusedOrReset(refused) || vpnMayExplain(refused, vpnFinding{SpeakerRoute: true}, pcEvidence{}) {
		t.Error("a German refusal is an answer from the speaker and rules the VPN out")
	}
	// boxDo hands back allPortsErr; the number of its first port counts.
	ae := allPortsError([]portFailure{
		{port: 8888, err: &url.Error{Op: "Get", URL: "http://192.0.2.34:8888/", Err: kill}},
		{port: 17008, err: errors.New("i/o timeout")},
	})
	if !blockedLocally(ae) {
		t.Error("the errno must be read through allPortsErr and url.Error")
	}
	// The Unix numbers work the same way.
	if !noRouteToHost(&os.SyscallError{Syscall: "connect", Err: syscall.EHOSTUNREACH}) ||
		!hostIsDown(&os.SyscallError{Syscall: "connect", Err: syscall.EHOSTDOWN}) {
		t.Error("the Unix error numbers must be recognised too")
	}
}

func TestPCSideAdviceNamesTheMacLocalNetworkPermission(t *testing.T) {
	enetunreach := errors.New("dial tcp 192.0.2.34:8888: connect: network is unreachable")
	ehostunreach := errors.New("dial tcp 192.0.2.34:8888: connect: no route to host")
	mac := pcRoutes{OS: "darwin", SpeakerSrc: testPCAddr, DefaultSrc: testPCAddr}
	en0 := []localIface{{Name: "en0", CIDR: testPCAddr + "/24", Up: true}}

	alive := pcEvidence{TargetAlive: true}
	for _, err := range []error{enetunreach, ehostunreach} {
		if adv, _ := pcSideAdvice(testSpeaker, err, en0, mac, alive); adv != macLocalNetworkAdvice {
			t.Errorf("darwin, same subnet, speaker alive, %q: want the Local Network advice, got %q", err, adv)
		}
	}
	// "network is unreachable" inside the Mac's own network needs nothing
	// beside it: no switched-off speaker produces that.
	if adv, _ := pcSideAdvice(testSpeaker, enetunreach, en0, mac, pcEvidence{}); adv != macLocalNetworkAdvice {
		t.Errorf("ENETUNREACH on its own must name the permission, got %q", adv)
	}
	// "no route to host" alone is also what macOS says for a speaker that is
	// off or asleep: without a sign of life it is not the permission.
	if adv, _ := pcSideAdvice(testSpeaker, ehostunreach, en0, mac, pcEvidence{}); adv != "" {
		t.Errorf("EHOSTUNREACH with no sign the speaker is alive must not blame the permission, got %q", adv)
	}
	// The permission covers the whole app: another speaker answering rules
	// it out whatever the error.
	other := pcEvidence{OtherAnswered: true, TargetAlive: true}
	for _, err := range []error{enetunreach, ehostunreach} {
		if adv, _ := pcSideAdvice(testSpeaker, err, en0, mac, other); adv != "" {
			t.Errorf("another speaker answered, %q: not the permission, got %q", err, adv)
		}
	}
	// Every port has to say "no route": one that found the host down is a
	// speaker that is off, even when the summary clause says "no route".
	mixed := allPortsError([]portFailure{
		{port: 8888, err: errors.New("dial tcp 192.0.2.34:8888: connect: host is down")},
		{port: 17008, err: errors.New("dial tcp 192.0.2.34:17008: connect: no route to host")},
	})
	if adv, _ := pcSideAdvice(testSpeaker, mixed, en0, mac, alive); adv != "" {
		t.Errorf("a port that found the host down is not the permission, got %q", adv)
	}
	both := allPortsError([]portFailure{
		{port: 8888, err: errors.New("dial tcp 192.0.2.34:8888: connect: no route to host")},
		{port: 17008, err: errors.New("dial tcp 192.0.2.34:17008: connect: no route to host")},
	})
	if adv, _ := pcSideAdvice(testSpeaker, both, en0, mac, alive); adv != macLocalNetworkAdvice {
		t.Errorf("every port refused by macOS, speaker alive: want the permission, got %q", adv)
	}

	// Not a Mac: the permission does not exist there.
	win := mac
	win.OS = "windows"
	if adv, _ := pcSideAdvice(testSpeaker, enetunreach, en0, win, pcEvidence{}); adv != "" {
		t.Errorf("windows must not get the macOS advice, got %q", adv)
	}
	// The speaker is in another network: "no route" is then plausibly true.
	if adv, _ := pcSideAdvice("198.51.100.7", enetunreach, en0, mac, pcEvidence{}); adv != "" {
		t.Errorf("a speaker outside the Mac's networks is not the permission, got %q", adv)
	}
	// The Wi-Fi is down: that is a Mac without network, the old verdict.
	down := []localIface{{Name: "en0", CIDR: testPCAddr + "/24", Up: false}}
	if adv, _ := pcSideAdvice(testSpeaker, enetunreach, down, mac, pcEvidence{}); adv != "" {
		t.Errorf("a down interface is no network, got %q", adv)
	}
	// Other errors are not the permission's fingerprint.
	for _, m := range []string{"connect: host is down", "i/o timeout", "connect: connection refused"} {
		if adv, _ := pcSideAdvice(testSpeaker, errors.New(m), en0, mac, alive); adv != "" {
			t.Errorf("%q is not the permission fingerprint, got %q", m, adv)
		}
	}
	// A VPN holding the route outranks the permission: granting it would
	// change nothing while the tunnel takes the traffic.
	withVPN := append([]localIface{}, en0...)
	withVPN = append(withVPN, localIface{Name: "utun6", CIDR: "10.5.0.2/32", Up: true, Virtual: true})
	vpnMac := mac
	vpnMac.DefaultSrc = "10.5.0.2"
	adv, why := pcSideAdvice(testSpeaker, ehostunreach, withVPN, vpnMac, pcEvidence{})
	if !strings.HasPrefix(adv, vpnAdvice) || !strings.Contains(adv, "utun6") {
		t.Errorf("a Mac with a VPN holding the default route: want the VPN advice naming utun6, got %q", adv)
	}
	if !strings.Contains(why, "utun6") {
		t.Errorf("the log reason must name the adapter, got %q", why)
	}
	// The VPN pushes the home network's own range: the tunnel holds the
	// speaker route although en0 sits next to the speaker. That is the VPN,
	// not the permission.
	clash := append([]localIface{}, en0...)
	clash = append(clash, localIface{Name: "utun6", CIDR: "192.0.2.50/24", Up: true, Virtual: true})
	clashMac := mac
	clashMac.SpeakerSrc = "192.0.2.50"
	if adv, _ := pcSideAdvice(testSpeaker, ehostunreach, clash, clashMac, alive); !strings.HasPrefix(adv, vpnAdvice) {
		t.Errorf("a VPN clashing with the home subnet: want the VPN advice, got %q", adv)
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

	t.Run("default-route VPN, another speaker answered, this one times out", func(t *testing.T) {
		withFakePC(t,
			[]localIface{testWiFi, {Name: "NordLynx", CIDR: "10.5.0.2/32", Up: true, Virtual: true}},
			pcRoutes{OS: "windows", SpeakerSrc: testPCAddr, DefaultSrc: "10.5.0.2"})
		b := &App{}
		b.rememberPort("192.0.2.35", 8888)
		got := b.reachabilityHintFor(testSpeaker, errors.New("dial tcp 192.0.2.34:8888: i/o timeout")).Error()
		if strings.Contains(got, vpnAdvice) || !strings.Contains(got, firewallAdvice) {
			t.Fatalf("the home network works through this VPN, so the old advice stays, got %q", got)
		}
		// An answer older than the window proves nothing about now.
		b.portMu.Lock()
		b.portAt["192.0.2.35"] = time.Now().Add(-2 * recentAnswerWindow)
		b.portMu.Unlock()
		if got := b.reachabilityHintFor(testSpeaker, errors.New("dial tcp 192.0.2.34:8888: i/o timeout")).Error(); !strings.Contains(got, vpnAdvice) {
			t.Fatalf("a stale answer must not hide the VPN, got %q", got)
		}
	})

	t.Run("Mac: no route to host for a speaker that is off", func(t *testing.T) {
		withFakePC(t,
			[]localIface{{Name: "en0", CIDR: testPCAddr + "/24", Up: true}},
			pcRoutes{OS: "darwin", SpeakerSrc: testPCAddr, DefaultSrc: testPCAddr})
		got := a.reachabilityHintFor(testSpeaker, errors.New("dial tcp 192.0.2.34:8888: connect: no route to host")).Error()
		if strings.Contains(got, macLocalNetworkAdvice) {
			t.Fatalf("no sign the speaker is alive: not the permission, got %q", got)
		}
	})

	t.Run("Mac: no route to host for a speaker discovery just saw", func(t *testing.T) {
		withFakePC(t,
			[]localIface{{Name: "en0", CIDR: testPCAddr + "/24", Up: true}},
			pcRoutes{OS: "darwin", SpeakerSrc: testPCAddr, DefaultSrc: testPCAddr})
		b := &App{discCache: map[string]discEntry{testSpeaker: {box: BoxInfo{Host: testSpeaker}, seen: time.Now()}}}
		got := b.reachabilityHintFor(testSpeaker, errors.New("dial tcp 192.0.2.34:8888: connect: no route to host")).Error()
		if !strings.Contains(got, macLocalNetworkAdvice) {
			t.Fatalf("a speaker on the network that macOS will not let the app reach is the permission, got %q", got)
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
