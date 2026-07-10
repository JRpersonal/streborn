package dlna

// SSDP NOTIFY (ssdp:alive) listener.
//
// DiscoverServers' M-SEARCH sweep only hears servers that answer a
// direct search request. A media server running on the SAME PC as the
// desktop app is structurally invisible to that sweep on Windows
// (#341, reopen of #222):
//
//   - UDP port 1900 is shared (SO_REUSEADDR) with the always-running
//     Windows "SSDP Discovery" service (SSDPSRV). A UNICAST datagram
//     to a shared port is delivered to only ONE of the sockets bound
//     to it, in practice SSDPSRV, so the loopback/self M-SEARCH probes
//     never reach the server's socket.
//   - Some servers (Windows Media Player sharing among them) do not
//     answer same-host M-SEARCH at all.
//
// MULTICAST delivery has no such single-receiver limitation: every
// socket that joined 239.255.255.250 on the port gets a copy. So we
// passively join the SSDP group and harvest the periodic NOTIFY
// ssdp:alive announcements every UPnP server is required to send.
// This is the authoritative same-host discovery path; the M-SEARCH
// probes remain for fast first-scan results from remote devices.
//
// The listener maintains a location -> expiry cache honoring
// CACHE-CONTROL max-age. DiscoverServers merges the cache entries into
// its result before the description-fetch stage; entries past their
// max-age are kept as candidates for a retention window because the
// description fetch already filters servers that are genuinely gone.

import (
	"bytes"
	"context"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/ipv4"
)

const (
	// defaultAnnounceMaxAge applies when a NOTIFY carries no usable
	// CACHE-CONTROL max-age. The UPnP spec minimum is 1800s and that
	// is also what most servers advertise.
	defaultAnnounceMaxAge = 1800 * time.Second
	// maxAnnounceMaxAge caps advertised lifetimes so a server with a
	// bogus huge max-age cannot pin a long-gone device in the list
	// for days.
	maxAnnounceMaxAge = 4 * time.Hour
	// announceRejoinInterval is how often the listener re-enumerates
	// interfaces and joins the SSDP group on ones that appeared (or
	// recovered) since the last pass, e.g. after a Wi-Fi reconnect.
	announceRejoinInterval = 3 * time.Minute
	// announceExpiredRetention is how long past its advertised lifetime
	// an announcement is still offered as a merge CANDIDATE. Expiry used
	// to hard-drop the entry, but a server that merely skipped a
	// re-announce (host asleep, one lost multicast datagram) then fell
	// out of the Library list even though it was still up. The
	// description fetch is the real liveness filter: a stale candidate
	// costs one failed HTTP GET, a dropped live server costs the user
	// their library (#341). byebye still retires entries immediately.
	announceExpiredRetention = 24 * time.Hour
)

type announceEntry struct {
	usn     string
	expires time.Time
}

// announceCache is the thread-safe LOCATION -> announcement store fed
// by the NOTIFY listener and drained by DiscoverServers.
type announceCache struct {
	mu      sync.Mutex
	entries map[string]announceEntry
}

var announces = &announceCache{entries: map[string]announceEntry{}}

// handlePacket ingests one SSDP datagram. Only NOTIFY packets are
// considered: ssdp:alive upserts the LOCATION with its advertised
// lifetime, ssdp:byebye drops every location announced under the same
// device uuid. Returns "alive", "byebye", or "" for ignored packets
// (the return value drives logging at the call site).
func (c *announceCache) handlePacket(pkt []byte, now time.Time) string {
	if !bytes.HasPrefix(pkt, []byte("NOTIFY")) {
		return ""
	}
	nts := strings.ToLower(headerValue(pkt, "NTS"))
	switch {
	case strings.Contains(nts, "ssdp:alive"):
		loc := headerValue(pkt, "LOCATION")
		if loc == "" {
			return ""
		}
		c.mu.Lock()
		c.entries[loc] = announceEntry{
			usn:     headerValue(pkt, "USN"),
			expires: now.Add(parseMaxAge(headerValue(pkt, "CACHE-CONTROL"))),
		}
		c.mu.Unlock()
		return "alive"
	case strings.Contains(nts, "ssdp:byebye"):
		// byebye carries no LOCATION, only a USN. Match on the device
		// uuid (the part before "::") so one byebye retires every
		// service/device variant the box announced.
		usn := headerValue(pkt, "USN")
		uuid := usn
		if i := strings.Index(usn, "::"); i >= 0 {
			uuid = usn[:i]
		}
		if uuid == "" {
			return ""
		}
		c.mu.Lock()
		for loc, e := range c.entries {
			if strings.HasPrefix(e.usn, uuid) {
				delete(c.entries, loc)
			}
		}
		c.mu.Unlock()
		return "byebye"
	}
	return ""
}

// candidateLocations returns every location worth a description probe:
// entries within their advertised lifetime plus expired ones still
// inside announceExpiredRetention (the fetch filters the dead ones).
// Entries expired for longer than the retention are forgotten.
func (c *announceCache) candidateLocations(now time.Time) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.entries))
	for loc, e := range c.entries {
		if now.After(e.expires.Add(announceExpiredRetention)) {
			delete(c.entries, loc)
			continue
		}
		out = append(out, loc)
	}
	return out
}

// parseMaxAge extracts max-age seconds from a CACHE-CONTROL header
// value ("max-age=1800", possibly with extra directives). Falls back
// to defaultAnnounceMaxAge and caps at maxAnnounceMaxAge.
func parseMaxAge(cc string) time.Duration {
	for _, part := range strings.Split(cc, ",") {
		part = strings.TrimSpace(strings.ToLower(part))
		if !strings.HasPrefix(part, "max-age") {
			continue
		}
		_, val, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		secs, err := strconv.Atoi(strings.TrimSpace(val))
		if err != nil || secs <= 0 {
			continue
		}
		d := time.Duration(secs) * time.Second
		if d > maxAnnounceMaxAge {
			return maxAnnounceMaxAge
		}
		return d
	}
	return defaultAnnounceMaxAge
}

var announceOnce sync.Once

// StartAnnounceListener starts the passive SSDP NOTIFY listener that
// feeds the announce cache merged by DiscoverServers. It joins the
// 239.255.255.250:1900 multicast group on every eligible IPv4
// interface, re-checking periodically so interfaces that appear later
// (Wi-Fi reconnect, docking) are picked up. Idempotent: only the
// first call does anything. Returns immediately; all work happens in
// background goroutines bound to ctx.
func StartAnnounceListener(ctx context.Context) {
	announceOnce.Do(func() {
		go runAnnounceListener(ctx)
	})
}

func runAnnounceListener(ctx context.Context) {
	group := &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}
	// joined tracks interfaces with a live reader, keyed by interface
	// index; a reader that dies removes itself so the next pass rejoins.
	var joinedMu sync.Mutex
	joined := map[int]bool{}

	joinPass := func() {
		for _, cand := range candidateIPv4Interfaces() {
			iface := cand.iface
			joinedMu.Lock()
			if joined[iface.Index] {
				joinedMu.Unlock()
				continue
			}
			// net.ListenMulticastUDP sets SO_REUSEADDR/SO_REUSEPORT, so this
			// coexists with the OS SSDP service (SSDPSRV on Windows) on :1900.
			conn, err := net.ListenMulticastUDP("udp4", iface, group)
			if err != nil {
				joinedMu.Unlock()
				Logger.Debug("dlna: SSDP NOTIFY join failed", "iface", iface.Name, "err", err.Error())
				continue
			}
			// CRITICAL for the same-host case: Go's ListenMulticastUDP
			// disables IP_MULTICAST_LOOP on this socket, and on Windows
			// that option acts on the RECEIVE path, silently discarding
			// every multicast datagram that originates on this host. Turn
			// it back on or a media server on this PC is never heard
			// (verified live on Windows 11, #341).
			if err := ipv4.NewPacketConn(conn).SetMulticastLoopback(true); err != nil {
				Logger.Debug("dlna: NOTIFY SetMulticastLoopback failed", "iface", iface.Name, "err", err.Error())
			}
			joined[iface.Index] = true
			joinedMu.Unlock()
			Logger.Info("dlna: SSDP NOTIFY listener joined", "iface", iface.Name)
			go func() {
				defer func() {
					conn.Close()
					joinedMu.Lock()
					delete(joined, iface.Index)
					joinedMu.Unlock()
				}()
				// Unblock the read loop when ctx ends.
				stop := context.AfterFunc(ctx, func() { conn.Close() })
				defer stop()
				buf := make([]byte, 4096)
				for {
					n, raddr, err := conn.ReadFromUDP(buf)
					if err != nil {
						if ctx.Err() == nil {
							Logger.Debug("dlna: SSDP NOTIFY read ended", "iface", iface.Name, "err", err.Error())
						}
						return
					}
					if action := announces.handlePacket(buf[:n], time.Now()); action != "" {
						Logger.Debug("dlna: SSDP NOTIFY", "action", action,
							"src", raddr.String(), "location", headerValue(buf[:n], "LOCATION"),
							"usn", headerValue(buf[:n], "USN"))
					}
				}
			}()
		}
	}

	joinPass()
	ticker := time.NewTicker(announceRejoinInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			joinPass()
		}
	}
}
