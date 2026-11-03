package main

// Causes of "cannot reach the speaker" that live on this PC and are neither
// the speaker, the router nor a firewall, and that the app used to misread:
//
//   - A VPN on the PC. Seven mail users had one (a NordVPN adapter on
//     10.5.0.2 among them) whose tunnel carried the connection away from the
//     home network, or whose kill switch dropped it. The advice they read
//     named a firewall, or "this computer has no network connection", and
//     the VPN only came up after a round of mails. noLocalRouteAdvice
//     mentions a VPN, but only for EADDRNOTAVAIL, which is the rarest of the
//     errors a VPN produces.
//
//   - The macOS Local Network permission. Two Mac users whose Mac found the
//     speakers through discovery reached none of them: macOS refuses the
//     connection with "no route to host" or "network is unreachable" when the
//     app has no Local Network permission, and the app answered that with
//     "This computer has no network connection", while the Mac had a perfectly
//     good address on the speaker's network.
//
// Both verdicts come from facts the PC can read about itself in a millisecond
// and without sending a packet: its interface list, and which local address
// the routing table picks for the speaker and for the internet.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"runtime"
	"strings"
	"time"
)

// pcRoutes is what this PC's routing table says, read without sending
// anything. A UDP "connect" only asks the kernel to choose a route and a
// source address; no packet leaves the machine.
type pcRoutes struct {
	// OS is runtime.GOOS. The Local Network permission exists only on macOS.
	OS string
	// SpeakerSrc is the local address the PC would send from to reach the
	// speaker, empty when the routing table offers no route at all.
	SpeakerSrc string
	// DefaultSrc is the same for an address on the internet, i.e. the source
	// of the default route. Empty when the PC has no default route.
	DefaultSrc string
}

// defaultRouteProbe is any address only the default route covers. A
// documentation address (TEST-NET-3) on purpose: nothing is ever sent to it,
// and it is nobody's server.
const defaultRouteProbe = "203.0.113.1"

// probePCRoutes reads the routes for one speaker. A variable so tests can hand
// in a fake routing table.
var probePCRoutes = func(host string) pcRoutes {
	return pcRoutes{
		OS:         runtime.GOOS,
		SpeakerSrc: routeSource(host),
		DefaultSrc: routeSource(defaultRouteProbe),
	}
}

// listPCIfaces is localIPv4Ifaces behind a variable, for the same reason.
var listPCIfaces = localIPv4Ifaces

// routeSource returns the local IPv4 address the routing table picks for host,
// or "" when there is none (or host is not an IPv4 literal).
func routeSource(host string) string {
	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil || ip.To4() == nil {
		return ""
	}
	c, err := net.DialTimeout("udp4", net.JoinHostPort(ip.String(), "9"), 500*time.Millisecond)
	if err != nil {
		return ""
	}
	defer c.Close()
	ua, ok := c.LocalAddr().(*net.UDPAddr)
	if !ok || ua.IP.To4() == nil || ua.IP.IsUnspecified() {
		return ""
	}
	return ua.IP.String()
}

// cgnatNet is 100.64.0.0/10, the shared address space that Tailscale,
// NordLynx's meshnet, and several other VPNs hand their tunnel interfaces. A
// home router never gives a PC an address from it.
var cgnatNet = func() *net.IPNet {
	_, n, _ := net.ParseCIDR("100.64.0.0/10")
	return n
}()

// vpnNamePrefixes and vpnNameParts are the interface names VPN software uses.
// Prefixes cover the Unix kernel names (utun3, tun0, tap0, wg0, ppp0, ipsec0,
// gpd0 for GlobalProtect); the parts cover the Windows adapter names the
// clients create ("NordLynx", "OpenVPN TAP-Windows6", "ProtonVPN TUN",
// "WireGuard Tunnel", "Mullvad", "OpenVPN Wintun").
var (
	vpnNamePrefixes = []string{"utun", "tun", "tap", "wg", "ppp", "ipsec", "gpd", "nordlynx"}
	vpnNameParts    = []string{"vpn", "nordlynx", "wireguard", "wintun", "tap-windows", "mullvad", "tailscale", "zerotier", "tunnel"}
)

// tunnelIface reports whether an interface looks like a VPN tunnel: by its
// name, by its shape (point-to-point or no hardware address), or by a CGNAT
// address. Looking like one blames nothing yet; findVPN decides whether it
// carries the traffic that matters.
func tunnelIface(in localIface) bool {
	name := strings.ToLower(strings.TrimSpace(in.Name))
	for _, p := range vpnNamePrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	for _, p := range vpnNameParts {
		if strings.Contains(name, p) {
			return true
		}
	}
	if in.Virtual {
		return true
	}
	if ip, _, err := net.ParseCIDR(strings.TrimSpace(in.CIDR)); err == nil && cgnatNet.Contains(ip) {
		return true
	}
	return false
}

// vpnFinding is a VPN that carries traffic the speaker connection depends on.
type vpnFinding struct {
	Iface localIface
	// SpeakerRoute is true when the connection to the speaker itself goes
	// into the tunnel; false when the tunnel "only" holds the default route
	// (which is how a VPN with a kill switch or without LAN access keeps the
	// app off the home network while the route to it still looks local).
	SpeakerRoute bool
}

// describe names the finding the way the report prints it.
func (v vpnFinding) describe() string {
	what := "holds this PC's default route"
	if v.SpeakerRoute {
		what = "carries the connection to the speaker"
	}
	return fmt.Sprintf("%s %s %s", v.Iface.Name, v.Iface.CIDR, what)
}

// ifaceByIP finds the interface that owns a local address.
func ifaceByIP(ip string, ifaces []localIface) (localIface, bool) {
	if ip == "" {
		return localIface{}, false
	}
	for _, in := range ifaces {
		a, _, err := net.ParseCIDR(strings.TrimSpace(in.CIDR))
		if err == nil && a.String() == ip {
			return in, true
		}
	}
	return localIface{}, false
}

// ifaceCovers reports whether host lies in the interface's own network. A
// tunnel-shaped interface that does is the speaker's own network (a bridged
// TAP into the home LAN, an odd driver without a MAC), not a VPN taking the
// traffic elsewhere.
func ifaceCovers(in localIface, host string) bool {
	ip := net.ParseIP(strings.TrimSpace(host))
	_, n, err := net.ParseCIDR(strings.TrimSpace(in.CIDR))
	if ip == nil || err != nil {
		return false
	}
	ones, bits := n.Mask.Size()
	return bits-ones > 1 && n.Contains(ip)
}

// findVPN decides whether a VPN on this PC is in the way of the speaker.
//
// Only a tunnel that holds the route to the speaker or the default route
// counts. A tunnel that holds neither is the common harmless case and must
// never be blamed: Tailscale (or any mesh VPN) with its single /32 address and
// no exit node, and the utun interfaces macOS keeps up for its own services.
func findVPN(host string, ifaces []localIface, r pcRoutes) (vpnFinding, bool) {
	if in, ok := ifaceByIP(r.SpeakerSrc, ifaces); ok && in.Up && tunnelIface(in) && !ifaceCovers(in, host) {
		return vpnFinding{Iface: in, SpeakerRoute: true}, true
	}
	if in, ok := ifaceByIP(r.DefaultSrc, ifaces); ok && in.Up && tunnelIface(in) && !ifaceCovers(in, host) {
		return vpnFinding{Iface: in}, true
	}
	return vpnFinding{}, false
}

// noRouteToHost reports EHOSTUNREACH ("no route to host"; Windows words
// WSAEHOSTUNREACH as "attempted to an unreachable host"). Kept apart from
// noNetworkHere on purpose, see there: on its own it mostly means a speaker
// that is switched off. Only the VPN and macOS checks give it more weight.
func noRouteToHost(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no route to host") || strings.Contains(msg, "unreachable host")
}

// blockedLocally reports a connection this PC itself refused to make:
// WSAEACCES on Windows ("forbidden by its access permissions"), EACCES/EPERM
// elsewhere. That is the signature of a packet filter on the PC, which with a
// VPN up is its kill switch.
func blockedLocally(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "forbidden by its access permissions") ||
		strings.Contains(msg, "permission denied") ||
		strings.Contains(msg, "operation not permitted")
}

// refusedOrReset reports an answer from the far end. A refused or reset
// connection got there, so whatever is wrong, the VPN did not stop it.
func refusedOrReset(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "refused") || strings.Contains(msg, "reset by peer") ||
		strings.Contains(msg, "forcibly closed")
}

// vpnMayExplain reports whether a failed speaker request is one a VPN in the
// given position produces. A tunnel that carries the speaker connection
// explains any failure short of an answer; a tunnel that only holds the
// default route explains the errors that mean "never got onto the home
// network": a local block, no usable route, or silence.
func vpnMayExplain(err error, v vpnFinding) bool {
	if err == nil || refusedOrReset(err) {
		return false
	}
	if v.SpeakerRoute {
		return true
	}
	return noLocalRoute(err) || noNetworkHere(err) || noRouteToHost(err) ||
		blockedLocally(err) || isTimeoutErr(err)
}

// isTimeoutErr matches a timed-out request the way briefTransportErr does.
func isTimeoutErr(err error) bool {
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") || strings.Contains(msg, "timed out") ||
		strings.Contains(msg, "deadline exceeded")
}

// macLocalNetworkDenied is the macOS Local Network permission fingerprint: the
// system says there is no route ("no route to host" or "network is
// unreachable") to an address that lies inside a network this Mac is on right
// now. A Mac with a working address in that network has a route by definition,
// so the refusal came from macOS itself.
func macLocalNetworkDenied(host string, err error, ifaces []localIface, r pcRoutes) bool {
	if r.OS != "darwin" || err == nil || !(noNetworkHere(err) || noRouteToHost(err)) {
		return false
	}
	known, same, _ := sameSubnet(host, upIfaces(ifaces))
	return known && same
}

// upIfaces keeps the interfaces that are up. An address on a down adapter is
// no route.
func upIfaces(ifaces []localIface) []localIface {
	var out []localIface
	for _, in := range ifaces {
		if in.Up {
			out = append(out, in)
		}
	}
	return out
}

// pcSideAdvice returns the advice for a PC-side cause of a failed speaker
// request, or "" when the facts point at none. The VPN is checked first: with
// a tunnel holding the route, a Mac's "no route" is the tunnel's doing and
// granting a permission would change nothing.
func pcSideAdvice(host string, err error, ifaces []localIface, r pcRoutes) (advice, why string) {
	if v, ok := findVPN(host, ifaces, r); ok && vpnMayExplain(err, v) {
		return vpnAdviceFor(v), "vpn: " + v.describe()
	}
	if macLocalNetworkDenied(host, err, ifaces, r) {
		return macLocalNetworkAdvice, "macOS refused a route inside its own network"
	}
	return "", ""
}

// vpnAdviceFor is vpnAdvice with the adapter named after it, so the user knows
// which of the icons in the tray is meant. The name goes AFTER the constant so
// the failure report still recognises the paragraph by its prefix.
func vpnAdviceFor(v vpnFinding) string {
	name := strings.TrimSpace(v.Iface.Name)
	if name == "" {
		return vpnAdvice
	}
	return vpnAdvice + " " + fmt.Sprintf(vpnAdapterNote, name)
}

// reachabilityHintFor is reachabilityHint with the speaker's address, which
// lets it check this PC for a VPN in the way and, on a Mac, for the Local
// Network permission before falling back to the generic verdicts.
func (a *App) reachabilityHintFor(host string, err error) error {
	if err == nil || host == "" || answeredNotSTR(err) || errors.Is(err, context.Canceled) {
		return reachabilityHint(err)
	}
	ifaces := listPCIfaces()
	routes := probePCRoutes(host)
	if adv, why := pcSideAdvice(host, err, ifaces, routes); adv != "" {
		logger := slog.Default()
		if a != nil && a.logger != nil {
			logger = a.logger
		}
		logger.Info("speaker unreachable: PC-side cause found", "host", host, "why", why,
			"speakerSrc", routes.SpeakerSrc, "defaultSrc", routes.DefaultSrc, "err", err)
		return fmt.Errorf("%w\n\n%s", err, adv)
	}
	return reachabilityHint(err)
}

// The advice for the two PC-side causes. Constants for the same reason as the
// others in app_transport.go: the failure report recognises them by prefix.
const (
	vpnAdvice = "A VPN is active on this PC, and it probably keeps ST Reborn from reaching your speaker: VPN apps often send the connection out through the VPN, or block devices at home, instead of letting it into your home network. Disconnect the VPN, or turn on its setting for local network access (often called LAN access, local network sharing or split tunnelling), then try again. If it still fails with the VPN off, check next whether a firewall or antivirus blocks ST Reborn."

	// vpnAdapterNote names the adapter. %s is its name as the system shows it.
	vpnAdapterNote = "The VPN adapter on this PC is called %s."

	macLocalNetworkAdvice = "Your Mac is on the same network as the speaker, but macOS did not let ST Reborn reach it. That is usually the Local Network permission. Open System Settings > Privacy & Security > Local Network and switch ST Reborn on. Then quit ST Reborn and open it again. If ST Reborn is already switched on there, switch it off and on once, and check that the speaker is powered and awake."
)
