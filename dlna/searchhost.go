package dlna

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"
)

// searchHostPort is the SSDP port a unicast search targets. A variable only
// so the test can stand in a responder on an ephemeral port; real callers
// always hit 1900.
var searchHostPort = 1900

// SearchHost probes ONE host for a MediaServer with a unicast M-SEARCH to
// its :1900. UPnP 1.1 defines unicast search exactly for this: it reaches a
// server whose multicast traffic never arrives, and APs or routers that
// filter multicast between Wi-Fi and wire are precisely the networks a
// speaker sits in while the desktop app on the wire still sees the server
// (#726). The caller supplies the host; on the speaker that is the firmware's
// own discovery cache (/listMediaServers), which learns addresses from the
// server's periodic NOTIFY announcements and so survives filters that
// swallow the agent's own M-SEARCH round.
func SearchHost(ctx context.Context, host string, timeout time.Duration) ([]Server, error) {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil {
		return nil, fmt.Errorf("searchhost: not an IPv4 address: %q", host)
	}
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return nil, fmt.Errorf("searchhost: listen: %w", err)
	}
	defer conn.Close()

	target := &net.UDPAddr{IP: ip.To4(), Port: searchHostPort}
	hostPort := net.JoinHostPort(ip.To4().String(), strconv.Itoa(searchHostPort))
	mkMsg := func(st string) []byte {
		return []byte("M-SEARCH * HTTP/1.1\r\n" +
			"HOST: " + hostPort + "\r\n" +
			"MAN: \"ssdp:discover\"\r\n" +
			fmt.Sprintf("MX: %d\r\n", defaultMXSecs) +
			"ST: " + st + "\r\n" +
			"USER-AGENT: STR/1 UPnP/1.0\r\n\r\n")
	}
	// Typed AND broad, same as the multicast sweep: some servers only answer
	// one of the two. Sent twice because it is one unacknowledged datagram.
	for i := 0; i < 2; i++ {
		_, _ = conn.WriteToUDP(mkMsg(mediaServerST), target)
		_, _ = conn.WriteToUDP(mkMsg("ssdp:all"), target)
		time.Sleep(80 * time.Millisecond)
	}

	if deadline, ok := dctx.Deadline(); ok {
		_ = conn.SetReadDeadline(deadline)
	}
	locations := map[string]struct{}{}
	buf := make([]byte, 4096)
	for {
		n, raddr, rerr := conn.ReadFromUDP(buf)
		if rerr != nil {
			break
		}
		if loc := headerValue(buf[:n], "LOCATION"); loc != "" {
			Logger.Info("dlna: unicast SSDP response", "src", raddr.String(), "location", loc)
			locations[loc] = struct{}{}
		}
	}
	Logger.Info("dlna: unicast M-SEARCH done", "host", host, "locations", len(locations))
	if len(locations) == 0 {
		return nil, nil
	}

	// Fresh, generous budget for the description fetch. This path is the
	// targeted fallback for ONE server the multicast round already missed, and
	// on a slow WD Twonky that miss is exactly the too-short-timeout it exists
	// to recover (#733), so it gets 15s where the bulk sweep shares 12s.
	fctx, fcancel := context.WithTimeout(ctx, 15*time.Second)
	defer fcancel()
	out := make([]Server, 0, len(locations))
	seen := map[string]struct{}{}
	for loc := range locations {
		s, ferr := fetchDeviceDescription(fctx, loc)
		if ferr != nil || s.UDN == "" || s.CDSControlURL == "" {
			continue
		}
		if _, dup := seen[s.UDN]; dup {
			continue
		}
		seen[s.UDN] = struct{}{}
		out = append(out, s)
	}
	return out, nil
}
