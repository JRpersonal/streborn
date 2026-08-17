// Package mdnshost answers mDNS address queries for exactly one name: the
// label STR gives this speaker.
//
// Why this exists. The Spotify engine advertises _spotify-connect._tcp with an
// SRV target taken from the box's Linux hostname, which on SoundTouch hardware
// is the Bose chassis codename: mojo on a SoundTouch 30, rhino on a 10, taigan
// on the Portable. Nothing on the network answers a query for that name. The
// mDNS library the engine uses only handles questions about its own service
// names and has no branch for a host name at all, so the address travels only
// as an extra record bundled into a service answer, with a lifetime of 120
// seconds while the service records carry 3200. A client that keeps the bundled
// address works; a client that looks the name up again gets silence. That is
// why the same speaker appears in Spotify on one network and not on another,
// and why it can drop out minutes later rather than immediately.
//
// The codename is also not unique. Measured on a five speaker network on
// 2026-08-17: three SoundTouch 10s all claimed rhino.local with three different
// addresses, and every one of those answers came from the router's cache, never
// from a speaker.
//
// So STR gives the engine a name of its own, derived from the device ID, and
// this responder is what makes that name mean something.
//
// What it deliberately does NOT do. It answers address questions for one label
// and nothing else: no service enumeration, no PTR, no SRV, no TXT, and never
// for the box's own hostname or for any name the Bose firmware publishes. The
// firmware's own records (Wohnzimmer.local, Bose-SM2-<mac>.local, its
// _soundtouch._tcp and AirPlay adverts) are left exactly as they are. Claiming
// a name someone else already owns would be the one way to make this worse than
// the bug it fixes.
package mdnshost

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

const (
	mdnsAddr = "224.0.0.251"
	mdnsPort = 5353
	// The lifetime we hand out. Short enough that a speaker which drops off
	// the network stops being pointed at, long enough that we are not
	// answering the same question every few seconds.
	ttlSeconds = 120
)

// labelRE is what a name has to look like before we will publish it. A label
// that mDNS rejects is not a cosmetic problem: the engine treats a failed
// registration as fatal and exits, which would cost Spotify entirely rather
// than just the advert. Hence: lowercase ASCII, digits and hyphens, starting on
// an alphanumeric, no dot, no space.
var labelRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidLabel reports whether name is safe to publish and to hand to the engine.
func ValidLabel(name string) bool { return labelRE.MatchString(name) }

// LabelFor builds this speaker's label from its device ID. The device ID is
// used rather than the friendly name on purpose: two speakers can share a
// friendly name, a rename would move the label out from under the engine, and a
// name like "BüroPortable" is not a legal label at all. The Bose firmware hits
// that last one itself and publishes the Portable as "B-roPortable.local".
func LabelFor(deviceID string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 32
		}
		return -1
	}, deviceID)
	if len(clean) > 6 {
		clean = clean[len(clean)-6:]
	}
	if clean == "" {
		return ""
	}
	return "str-" + clean
}

// Responder answers address questions for one name.
type Responder struct {
	label  string // "str-96488d"
	fqdn   string // "str-96488d.local."
	logger *slog.Logger

	conn *net.UDPConn
	once sync.Once
	done chan struct{}

	mu sync.RWMutex
	ip net.IP

	// Counters, exposed in the diagnostic bundle. They exist for one open
	// question: a SoundTouch 30 here announces its Spotify entry so rarely that
	// four browses in a row find nothing, while its engine runs, is logged in
	// and answers on its own port. Either the questions never reach that box or
	// its answers never leave it, and no amount of reasoning tells the two
	// apart. This responder sits on the same socket and can say which.
	seenQueries   int64 // mDNS questions of any kind that reached us
	ownQueries    int64 // questions about our name
	answersSent   int64
	announcements int64
}

// Start brings the responder up. It returns an error rather than a disabled
// responder when anything goes wrong, because the caller has to know: the
// engine may only be pointed at this name once the name is actually answerable.
//
// A third listener on port 5353 is not obviously safe, so this is the part to
// watch. Two responders already share that port on the box, the agent's own
// announcer and the engine's, which is why the socket is opened with the
// multicast helper that sets address reuse.
func Start(ctx context.Context, logger *slog.Logger, label string, addr net.IP) (*Responder, error) {
	if !ValidLabel(label) {
		return nil, fmt.Errorf("mdnshost: refusing to publish the label %q", label)
	}
	if addr == nil || addr.To4() == nil {
		return nil, fmt.Errorf("mdnshost: no routable IPv4 address to publish for %q", label)
	}
	iface, err := ifaceFor(addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenMulticastUDP("udp4", iface, &net.UDPAddr{
		IP:   net.ParseIP(mdnsAddr),
		Port: mdnsPort,
	})
	if err != nil {
		return nil, fmt.Errorf("mdnshost: cannot join the mDNS group on %s: %w", iface.Name, err)
	}
	r := &Responder{
		label:  label,
		fqdn:   dns.Fqdn(label + ".local"),
		logger: logger,
		conn:   conn,
		done:   make(chan struct{}),
		ip:     addr.To4(),
	}
	go r.serve()
	go r.announce(ctx)
	logger.Info("mdns host: answering address queries for this speaker's own name",
		"name", r.fqdn, "address", addr.String(), "iface", iface.Name)
	return r, nil
}

// Name is the fully qualified name this responder answers for, without the
// trailing dot, i.e. what the engine should advertise as its SRV target.
func (r *Responder) Name() string { return strings.TrimSuffix(r.fqdn, ".") }

// Label is the bare label, which is what the engine's environment variable
// wants: its mDNS library appends the domain itself, so handing it a name that
// already ends in ".local" produces "<name>.local.local".
func (r *Responder) Label() string { return r.label }

// SetAddress updates the address handed out, for a speaker that changes network.
func (r *Responder) SetAddress(ip net.IP) {
	if ip == nil || ip.To4() == nil {
		return
	}
	r.mu.Lock()
	changed := !r.ip.Equal(ip.To4())
	r.ip = ip.To4()
	r.mu.Unlock()
	if changed {
		r.logger.Info("mdns host: address changed, announcing again", "name", r.fqdn, "address", ip.String())
		r.sendAnnouncement()
	}
}

// Close stops the responder.
func (r *Responder) Close() error {
	var err error
	r.once.Do(func() {
		close(r.done)
		err = r.conn.Close()
	})
	return err
}

func (r *Responder) address() net.IP {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.ip
}

func (r *Responder) serve() {
	buf := make([]byte, 1500)
	for {
		select {
		case <-r.done:
			return
		default:
		}
		n, src, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-r.done:
				return
			default:
			}
			continue
		}
		var msg dns.Msg
		if err := msg.Unpack(buf[:n]); err != nil {
			continue
		}
		if msg.Response {
			continue
		}
		r.mu.Lock()
		r.seenQueries += int64(len(msg.Question))
		r.mu.Unlock()
		r.handle(&msg, src)
	}
}

// handle answers only what this responder owns. Every other question on the
// wire, and there are many, is left to whoever it belongs to.
func (r *Responder) handle(msg *dns.Msg, src *net.UDPAddr) {
	var answers []dns.RR
	unicast := false
	for _, q := range msg.Question {
		if !strings.EqualFold(q.Name, r.fqdn) {
			continue
		}
		if q.Qtype != dns.TypeA && q.Qtype != dns.TypeANY {
			continue
		}
		// The top bit of the class carries the querier's request for a unicast
		// answer. Honouring it keeps a burst of lookups off the multicast group.
		if q.Qclass&0x8000 != 0 {
			unicast = true
		}
		answers = append(answers, r.record())
	}
	if len(answers) == 0 {
		return
	}
	r.mu.Lock()
	r.ownQueries += int64(len(answers))
	r.answersSent++
	r.mu.Unlock()
	resp := new(dns.Msg)
	resp.SetReply(msg)
	resp.Question = nil
	resp.Answer = answers
	resp.Authoritative = true
	packed, err := resp.Pack()
	if err != nil {
		return
	}
	dst := &net.UDPAddr{IP: net.ParseIP(mdnsAddr), Port: mdnsPort}
	if unicast && src != nil {
		dst = src
	}
	_, _ = r.conn.WriteToUDP(packed, dst)
}

// record is the one record this responder owns. The cache-flush bit is set
// because the name is ours alone: it is derived from this speaker's device ID,
// so no other speaker and nothing in the Bose firmware can be publishing it.
func (r *Responder) record() dns.RR {
	return &dns.A{
		Hdr: dns.RR_Header{
			Name:   r.fqdn,
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET | 0x8000,
			Ttl:    ttlSeconds,
		},
		A: r.address(),
	}
}

// announce sends the record unasked, twice at the start and then rarely. A
// client that browses right after the speaker boots then has the address
// without having to ask for it, which is the case the bundled-record lifetime
// used to cover and no longer did.
func (r *Responder) announce(ctx context.Context) {
	for i := 0; i < 2; i++ {
		select {
		case <-r.done:
			return
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(i+1) * time.Second):
			r.sendAnnouncement()
		}
	}
	t := time.NewTicker(15 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-r.done:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			r.sendAnnouncement()
		}
	}
}

func (r *Responder) sendAnnouncement() {
	msg := new(dns.Msg)
	msg.Response = true
	msg.Authoritative = true
	msg.Answer = []dns.RR{r.record()}
	packed, err := msg.Pack()
	if err != nil {
		return
	}
	_, _ = r.conn.WriteToUDP(packed, &net.UDPAddr{IP: net.ParseIP(mdnsAddr), Port: mdnsPort})
	r.mu.Lock()
	r.announcements++
	r.mu.Unlock()
}

// Stats reports what this responder has seen, for the diagnostic bundle. The
// telling number is seenQueries: a speaker whose adverts never show up while
// this counter climbs is being asked and failing to be heard, and one where it
// stays at zero is never being asked in the first place. Those are different
// faults with different fixes.
func (r *Responder) Stats() map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return map[string]any{
		"name":             r.fqdn,
		"address":          r.ip.String(),
		"queriesSeen":      r.seenQueries,
		"queriesForUs":     r.ownQueries,
		"answersSent":      r.answersSent,
		"announcementsOut": r.announcements,
	}
}

// ifaceFor finds the interface carrying the given address, so the multicast
// group is joined on the one the speaker is actually reachable on. On these
// boxes that matters: a SoundTouch 30 reports an ethernet port that is present
// and disconnected while it runs on Wi-Fi.
func ifaceFor(addr net.IP) (*net.Interface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("mdnshost: cannot list interfaces: %w", err)
	}
	for i := range ifaces {
		in := &ifaces[i]
		if in.Flags&net.FlagUp == 0 || in.Flags&net.FlagMulticast == 0 {
			continue
		}
		addrs, err := in.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			if ipnet.IP.To4().Equal(addr.To4()) {
				return in, nil
			}
		}
	}
	return nil, fmt.Errorf("mdnshost: no up multicast interface carries %s", addr)
}
