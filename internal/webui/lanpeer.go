// Deciding whether an address the caller handed us is a speaker on this LAN.

package webui

import "net"

// isLANPeer reports whether host is a plain IP address on the local network,
// i.e. something that can legitimately be another SoundTouch speaker.
//
// It exists because several agent endpoints take a peer address from the
// request body and then dial it. That is a caller-controlled destination, and
// without a check the speaker can be aimed at any host reachable from where it
// sits, which on a home network is everything. The endpoints in question all
// have the same true answer: a peer is another speaker, and another speaker is
// on the LAN.
//
// Deliberately strict about what it accepts:
//
//   - A hostname is refused. Names resolve, and what they resolve to can change
//     between this check and the dial, so allowing them would make the check
//     decorative rather than real.
//   - Loopback is refused. The agent's own services live there, and a peer that
//     is "us" is never a stereo partner; allowing it would turn a peer field
//     into a way to reach services bound to localhost.
//   - Link-local and unique-local addresses are accepted alongside RFC1918:
//     IPv6-only home networks are ordinary, and a speaker there is still a
//     speaker.
func isLANPeer(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false // not a bare IP: a name, an empty string, or junk
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	return ip.IsPrivate() || ip.IsLinkLocalUnicast()
}
