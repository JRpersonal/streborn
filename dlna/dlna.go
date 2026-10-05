// Package dlna is a minimal DLNA / UPnP MediaServer client. It
// discovers MediaServer devices on the LAN via SSDP, fetches their
// device description, and walks the ContentDirectory tree via
// Browse SOAP calls.
//
// Used by the desktop app's Library tab so users can play music
// from FRITZ!Box, Synology, Plex and similar servers on their
// SoundTouch without typing URLs. Playback itself goes through
// internal/upnp on the renderer side; this package is only the
// browse half.
//
// Top-level package (not internal/) so a future PWA / second
// frontend can reuse the same code, mirroring discovery/.
package dlna

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/JRpersonal/streborn/internal/netutil"
	"golang.org/x/net/ipv4"
)

const (
	ssdpAddr       = "239.255.255.250:1900"
	mediaServerST  = "urn:schemas-upnp-org:device:MediaServer:1"
	cdsServiceType = "urn:schemas-upnp-org:service:ContentDirectory:1"
	// MX (seconds the device may wait before answering) and the listen window
	// are deliberately generous: a NAS (Asustor/MiniDLNA/Plex) is slower to
	// compose and return its SSDP response than a router, and ListMediaServers
	// is user-initiated, so a couple of extra seconds is acceptable (#110).
	defaultMXSecs   = 3
	defaultDiscover = 5 * time.Second
)

// SOAPClientCeiling is the last-resort timeout on a ContentDirectory SOAP call.
// It exists ONLY for a caller that passed a ctx with no deadline. Every real
// caller sets one, and the caller's deadline is what should decide, because only
// the caller knows whether this is one user tap or one share of a fan-out.
//
// So it has to stay ABOVE the longest caller budget, or it silently overrides
// it. That is the bug it is named for: a hard-coded 10s cap here cut a library
// browse off at 10.1s while internal/webui deliberately allowed 70s for exactly
// the slow NAS the reporter had, and the failure came back as Go's own
// client-timeout text, which reads like the media server giving up (#960).
// DescribeServer had the same cap and got the same fix for a WD Twonky (#733);
// Browse and Search were missed then.
const SOAPClientCeiling = 90 * time.Second

// Server is a single discovered DLNA MediaServer on the LAN.
type Server struct {
	// UDN is the unique device identifier (uuid:...), stable across
	// reboots. Used by the desktop app as the dropdown key.
	UDN string
	// Location is the device-description URL this record was resolved
	// from. Persisted by the desktop app (known-servers.json) so a later
	// scan can re-probe the server directly even when SSDP stays silent:
	// without it a same-PC server was invisible until its next periodic
	// NOTIFY, up to 30 minutes after an app start (#341).
	Location string
	// FriendlyName as advertised by the device, e.g. "FRITZ!Box 7590".
	FriendlyName string
	// Manufacturer / ModelName let the UI show a useful subtitle.
	Manufacturer string
	ModelName    string
	// Address is "host:port" of the device description endpoint.
	Address string
	// CDSControlURL is the fully resolved URL to call ContentDirectory
	// SOAP actions against. Empty if the server does not expose CDS
	// (in which case it is unusable for browse).
	CDSControlURL string
	// IconURL is the first usable icon URL the device advertised.
	IconURL string
	// CDSDeviceType is the deviceType of the (sub-)device that owns the
	// ContentDirectory service, e.g. urn:schemas-upnp-org:device:MediaServer:1.
	// A NAS can advertise ContentDirectory from a device that is not a media
	// server at all (a QNAP's QTS web server answers as NAS:1 and returns an
	// HTML 404 to every Browse), so discovery only offers such a device after
	// a validation browse proved it answers (see acceptServer).
	CDSDeviceType string `json:",omitempty"`
}

// DiscoverServers sends an SSDP M-SEARCH for MediaServer devices, collects
// unique responses for the given timeout, resolving each device description
// in parallel as the answers arrive, and returns the populated Server list
// once the listen window closes. Honors ctx for cancellation.
//
// Implementation notes:
//   - Multi-NIC: enumerates all non-loopback IPv4 interfaces with an
//     address, opens one UDP socket per interface, and sends the
//     M-SEARCH from each. An earlier wildcard-only variant
//     (net.IPv4zero) sent on whichever interface Windows picked by
//     route priority, which lost media servers on hosts with two
//     active wifi adapters (home wifi + a Bose-Setup-AP USB dongle
//     was the original failure mode; the same applies any time the
//     user has Wi-Fi 1 + Wi-Fi 2 connected to different LANs).
//   - Sends BOTH a typed M-SEARCH (ST: MediaServer:1) AND a broad
//     one (ST: ssdp:all). Some servers (and some firmware bugs)
//     only respond to one of the two. Cheap to send both.
//   - Filters responses on LOCATION presence; the server type
//     filter happens at device-description fetch time because a
//     root device may host the MediaServer as a sub-device.
//
// Set Logger before calling for visibility into per-interface
// behavior — DLNA scans that surface zero results in the UI are
// otherwise indistinguishable from "no servers on LAN".
var Logger *slog.Logger = slog.Default()

func DiscoverServers(ctx context.Context, timeout time.Duration) ([]Server, error) {
	found, _, err := discoverServers(ctx, timeout, nil)
	return found, err
}

// FindServer runs the same sweep as DiscoverServers but resolves device
// descriptions AS the SSDP answers arrive and returns the moment match says
// yes, instead of sitting out the full listen window first. A browse tap that
// resolves ONE registered server gets its answer in the one or two seconds
// that server actually needs; before, the full listen window was paid on every
// first browse after an agent restart, healthy servers included (#733). The
// full window is still paid when the server never answers, which is exactly
// the case that needs the evidence: the returned list then carries every
// server that IS live, for the name rematch and for telling the user what was
// reachable instead.
func FindServer(ctx context.Context, timeout time.Duration, match func(Server) bool) (Server, bool, []Server, error) {
	found, m, err := discoverServers(ctx, timeout, match)
	if m != nil {
		return *m, true, found, err
	}
	return Server{}, false, found, err
}

func discoverServers(ctx context.Context, timeout time.Duration, match func(Server) bool) ([]Server, *Server, error) {
	if timeout <= 0 {
		timeout = defaultDiscover
	}
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	mcAddr, err := net.ResolveUDPAddr("udp4", ssdpAddr)
	if err != nil {
		return nil, nil, fmt.Errorf("ssdp resolve: %w", err)
	}

	mkMsg := func(st, host string) []byte {
		return []byte(strings.Join([]string{
			"M-SEARCH * HTTP/1.1",
			"HOST: " + host,
			"MAN: \"ssdp:discover\"",
			fmt.Sprintf("MX: %d", defaultMXSecs),
			"ST: " + st,
			"USER-AGENT: STR/1 UPnP/1.0",
			"", "",
		}, "\r\n"))
	}
	typedMsg := mkMsg(mediaServerST, ssdpAddr)
	allMsg := mkMsg("ssdp:all", ssdpAddr)

	// Enumerate candidate source interfaces/IPs. Skip loopback,
	// link-local (169.254.x.x), and any v6 addresses since SSDP here
	// is v4 only.
	cands := candidateIPv4Interfaces()
	if len(cands) == 0 {
		Logger.Warn("dlna: no usable IPv4 interfaces, falling back to wildcard")
		cands = []ifaceIPv4{{ip: net.IPv4zero}}
	}
	Logger.Info("dlna: SSDP M-SEARCH starting", "interfaces", len(cands), "timeout", timeout.String())

	locationsMu := sync.Mutex{}
	locations := map[string]struct{}{}

	// Describe as they arrive: every NEW location spawns its description fetch
	// immediately instead of after the listen window closes, so a match can end
	// the sweep early and a full sweep overlaps fetch time with listen time.
	// fparent bounds the whole fetch fleet and is cancelled on early exit.
	fparent, fcancel := context.WithCancel(ctx)
	defer fcancel()
	type result struct {
		s   Server
		err error
	}
	resCh := make(chan result, 256)
	var pending sync.WaitGroup
	enqueue := func(loc string) bool { // reports whether loc was new
		locationsMu.Lock()
		if _, dup := locations[loc]; dup {
			locationsMu.Unlock()
			return false
		}
		locations[loc] = struct{}{}
		locationsMu.Unlock()
		pending.Add(1)
		go func() {
			defer pending.Done()
			// Per-fetch deadline instead of the old one shared 12 s window
			// after the listen phase: a slow NAS (WD Twonky, #733) keeps its
			// headroom, the fast ones answer in well under a second.
			fctx, c := context.WithTimeout(fparent, 12*time.Second)
			defer c()
			s, err := fetchDeviceDescription(fctx, loc)
			if err == nil {
				err = acceptServer(fctx, s)
			}
			select {
			case resCh <- result{s: s, err: err}:
			case <-fparent.Done():
			}
		}()
		return true
	}

	// collect reads SSDP responses on conn until the discovery deadline and
	// records each unique LOCATION. Shared by the per-interface multicast probes
	// and the same-host loopback unicast probe.
	collect := func(conn *net.UDPConn, label string) {
		if deadline, _ := dctx.Deadline(); !deadline.IsZero() {
			_ = conn.SetReadDeadline(deadline)
		}
		localHits := 0
		// One line per distinct answer, not per packet. A single media server
		// answers each M-SEARCH several times from several ephemeral ports, and
		// across the probe rounds that added up to 665 log lines out of 776 in
		// one field diagnostic (2026-09-28): the repetition crowded out the
		// lines a diagnosis actually needs, across all three retained log
		// generations. The question this logging exists to answer - did the NAS
		// answer at all, from which interface - is answered by the first
		// sighting; the rest is the same fact again. The count goes into the
		// probe-done line so the volume is still visible.
		seen := map[string]bool{}
		responses := 0
		buf := make([]byte, 4096)
		for {
			select {
			case <-dctx.Done():
				Logger.Info("dlna: SSDP probe done", "probe", label, "newLocations", localHits, "responses", responses, "distinct", len(seen))
				return
			default:
			}
			n, raddr, err := conn.ReadFromUDP(buf)
			if err != nil {
				break
			}
			loc := headerValue(buf[:n], "LOCATION")
			if loc == "" {
				continue
			}
			st := headerValue(buf[:n], "ST")
			// Log every response that carries a LOCATION so a "no media servers
			// found" report is debuggable (did the NAS even answer, from which
			// interface). No ST filter: many MediaServers (Asustor/MiniDLNA/Plex)
			// answer ssdp:all with an ST of ContentDirectory or a vendor URN and
			// were silently dropped here despite serving a valid MediaServer
			// device.xml. The real gate is the post-fetch CDSControlURL check (#110).
			responses++
			// Keyed WITHOUT the source port: the port is a fresh ephemeral one
			// per answer and would make every repeat look distinct.
			if key := raddr.IP.String() + "|" + st + "|" + loc; !seen[key] {
				seen[key] = true
				Logger.Info("dlna: SSDP response", "src", raddr.IP.String(), "st", st, "location", loc)
			}
			if enqueue(loc) {
				localHits++
			}
		}
		Logger.Info("dlna: SSDP probe done", "probe", label, "newLocations", localHits, "responses", responses, "distinct", len(seen))
	}

	var ifaceWg sync.WaitGroup
	for _, cand := range cands {
		ifaceWg.Add(1)
		go func(cand ifaceIPv4) {
			defer ifaceWg.Done()
			srcIP := cand.ip
			conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: srcIP, Port: 0})
			if err != nil {
				Logger.Warn("dlna: ListenUDP failed", "src", srcIP.String(), "err", err.Error())
				return
			}
			defer conn.Close()
			// Pin multicast egress to THIS interface (Windows otherwise
			// routes the send by route priority, defeating the whole
			// per-interface sweep) and enable multicast loopback so a
			// server on this same host at least has a chance to hear the
			// search. Best-effort: a failure falls back to OS defaults.
			p := ipv4.NewPacketConn(conn)
			if cand.iface != nil {
				if err := p.SetMulticastInterface(cand.iface); err != nil {
					Logger.Debug("dlna: SetMulticastInterface failed", "src", srcIP.String(), "err", err.Error())
				}
			}
			if err := p.SetMulticastLoopback(true); err != nil {
				Logger.Debug("dlna: SetMulticastLoopback failed", "src", srcIP.String(), "err", err.Error())
			}
			// Same-host servers: besides the multicast search, unicast the
			// search to our own LAN IP on :1900. NOTE: on Windows UDP :1900
			// is shared (SO_REUSEADDR) with the SSDP Discovery service
			// (SSDPSRV), and a unicast datagram to a shared port reaches
			// only ONE of the bound sockets, usually SSDPSRV, so this probe
			// (like the loopback one below) is best-effort at most. The
			// NOTIFY listener (announce.go) is the authoritative same-host
			// discovery path (#341).
			selfAddr := &net.UDPAddr{IP: srcIP, Port: 1900}
			selfHost := net.JoinHostPort(srcIP.String(), "1900")
			selfTyped := mkMsg(mediaServerST, selfHost)
			selfAll := mkMsg("ssdp:all", selfHost)
			for i := 0; i < 2; i++ {
				_, _ = conn.WriteToUDP(typedMsg, mcAddr)
				_, _ = conn.WriteToUDP(allMsg, mcAddr)
				_, _ = conn.WriteToUDP(selfTyped, selfAddr)
				_, _ = conn.WriteToUDP(selfAll, selfAddr)
				time.Sleep(80 * time.Millisecond)
			}
			collect(conn, srcIP.String())
		}(cand)
	}

	// A media server on the SAME PC as STR is missed by the multicast probes
	// above: SSDP multicast does not reliably loop back to a server on the same
	// host, so the user's own PC media server does not show up even though other
	// LAN devices see it (#222). Probe it directly with a UNICAST M-SEARCH to the
	// loopback, which a server bound to 0.0.0.0:1900 answers.
	ifaceWg.Add(1)
	go func() {
		defer ifaceWg.Done()
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if err != nil {
			Logger.Warn("dlna: loopback ListenUDP failed", "err", err.Error())
			return
		}
		defer conn.Close()
		loopAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1900}
		loTyped := mkMsg(mediaServerST, "127.0.0.1:1900")
		loAll := mkMsg("ssdp:all", "127.0.0.1:1900")
		for i := 0; i < 2; i++ {
			_, _ = conn.WriteToUDP(loTyped, loopAddr)
			_, _ = conn.WriteToUDP(loAll, loopAddr)
			time.Sleep(80 * time.Millisecond)
		}
		collect(conn, "loopback")
	}()

	// Merge everything the passive NOTIFY listener has heard, UP FRONT: these
	// addresses are known now, and describing them immediately is what lets a
	// same-host or M-SEARCH-deaf server (announce.go, #341) satisfy an early
	// match too. Includes expired-but-retained announcements: the description
	// fetch filters the ones that are genuinely gone.
	announced := 0
	for _, loc := range announces.candidateLocations(time.Now()) {
		if enqueue(loc) {
			announced++
		}
	}
	if announced > 0 {
		Logger.Info("dlna: merged NOTIFY announcements", "newLocations", announced)
	}

	// Close resCh once the listen window is over and every spawned fetch has
	// reported, so the loop below has a definite end.
	go func() {
		ifaceWg.Wait()
		locationsMu.Lock()
		total := len(locations)
		locationsMu.Unlock()
		Logger.Info("dlna: SSDP M-SEARCH done", "totalLocations", total)
		pending.Wait()
		close(resCh)
	}()

	out := make([]Server, 0, 8)
	seen := map[string]struct{}{}
	for r := range resCh {
		if r.err != nil || r.s.UDN == "" || r.s.CDSControlURL == "" {
			continue
		}
		if _, dup := seen[r.s.UDN]; dup {
			continue
		}
		seen[r.s.UDN] = struct{}{}
		out = append(out, r.s)
		if match != nil && match(r.s) {
			m := r.s
			Logger.Info("dlna: discovery matched early", "server", m.FriendlyName, "described", len(out))
			cancel()
			fcancel()
			return out, &m, nil
		}
	}
	return out, nil, nil
}

// ifaceIPv4 pairs an eligible interface with one of its routable IPv4
// addresses. The M-SEARCH sweep opens one send socket per pair; the
// NOTIFY listener joins the multicast group once per interface.
type ifaceIPv4 struct {
	iface *net.Interface
	ip    net.IP
}

// candidateIPv4Interfaces returns the (interface, IPv4 address) pairs
// we should run SSDP on. Excludes loopback, link-local (169.254.x.x)
// and any non-up interface. The result drives the per-interface
// multicast send so a LAN that lives on Wi-Fi 1 gets probed even when
// Wi-Fi 2 is connected to a different network (e.g. a Bose setup-AP)
// at the same time.
func candidateIPv4Interfaces() []ifaceIPv4 {
	var out []ifaceIPv4
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for i := range ifaces {
		iface := &ifaces[i]
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil {
				continue
			}
			// The USB gadget is up and carries a routable looking address, so
			// without this it became a candidate and the speaker could offer
			// media at an address no client on the home network can reach.
			if !netutil.UsableLANIPv4(ip4) {
				continue
			}
			out = append(out, ifaceIPv4{iface: iface, ip: ip4})
		}
	}
	return out
}

// DescribeServer fetches and parses the UPnP device description at
// location and returns it as the same Server shape DiscoverServers
// produces. Used by the desktop app's manual "add server by address"
// fallback (#341) for servers that neither answer M-SEARCH nor send
// NOTIFY announcements the app can hear. Errors when the description
// is unreachable, unparseable, or describes a device without a
// ContentDirectory service (i.e. not browsable as a media server).
func DescribeServer(ctx context.Context, location string) (Server, error) {
	s, err := fetchDeviceDescription(ctx, location)
	if err != nil {
		return Server{}, err
	}
	if s.UDN == "" {
		return Server{}, fmt.Errorf("device description at %s has no UDN", location)
	}
	if s.CDSControlURL == "" {
		return Server{}, fmt.Errorf("device at %s exposes no ContentDirectory service", location)
	}
	if err := acceptServer(ctx, s); err != nil {
		return Server{}, err
	}
	return s, nil
}

// mediaServerTypePrefix matches every version of the standard MediaServer
// device type.
const mediaServerTypePrefix = "urn:schemas-upnp-org:device:MediaServer:"

// IsMediaServerType reports whether deviceType is a standard UPnP MediaServer
// device (any version).
func IsMediaServerType(deviceType string) bool {
	return strings.HasPrefix(strings.TrimSpace(deviceType), mediaServerTypePrefix)
}

// validateBrowseTimeout bounds the one-item probe browse that decides whether a
// ContentDirectory owned by something other than a MediaServer device is real.
const validateBrowseTimeout = 5 * time.Second

// ErrNotBrowsable is returned for a device that advertises a ContentDirectory
// service but is not a MediaServer device and does not answer a Browse with a
// valid BrowseResponse.
var ErrNotBrowsable = errors.New("device advertises ContentDirectory but does not answer Browse")

// acceptServer decides whether a described device may be offered as a media
// server. A ContentDirectory owned by a MediaServer device is accepted as is.
// Anything else is only accepted when a one-item root Browse comes back as a
// real BrowseResponse.
//
// Field case (QNAP NAS, desktop v1.0.3): besides its minidlna media server the
// NAS announces its QTS web server as a NAS:1 device that lists
// ContentDirectory:1 but answers every Browse with an HTML 404 page. With the
// real media server refusing that PC, the NAS:1 device was the only QNAP entry
// in the Library and every browse of it failed. A device a user already
// enabled on a speaker that genuinely browses keeps working, because the probe
// passes for it.
func acceptServer(ctx context.Context, s Server) error {
	if s.CDSControlURL == "" || IsMediaServerType(s.CDSDeviceType) {
		return nil
	}
	vctx, cancel := context.WithTimeout(ctx, validateBrowseTimeout)
	defer cancel()
	raw, status, err := browseRaw(vctx, s, "0", 0, 1)
	if err == nil && status == http.StatusOK && isBrowseResponse(raw) {
		Logger.Debug("dlna: non-MediaServer ContentDirectory accepted after validation browse",
			"location", s.Location, "deviceType", s.CDSDeviceType)
		return nil
	}
	var detail string
	switch {
	case err != nil:
		detail = err.Error()
	case status != http.StatusOK:
		detail = fmt.Sprintf("HTTP %d", status)
	default:
		detail = "no BrowseResponse in the answer"
	}
	if logLimiter.allow("drop|" + s.Location) {
		Logger.Info("dlna: ignoring device that advertises ContentDirectory but cannot be browsed",
			"location", s.Location, "deviceType", s.CDSDeviceType, "name", s.FriendlyName,
			"controlURL", s.CDSControlURL, "probe", detail, "body", bodySnippet(raw))
	}
	return fmt.Errorf("%w: %s (%s): %s", ErrNotBrowsable, s.FriendlyName, s.CDSDeviceType, detail)
}

// isBrowseResponse reports whether raw is a SOAP envelope carrying a
// BrowseResponse element (as opposed to a fault, or an HTML page).
func isBrowseResponse(raw []byte) bool {
	var env struct {
		Body struct {
			BrowseResponse *struct{} `xml:"BrowseResponse"`
		} `xml:"Body"`
	}
	if xmlRootName(raw) != "Envelope" {
		return false
	}
	if err := xml.Unmarshal(stripIllegalXMLChars(raw), &env); err != nil {
		return false
	}
	return env.Body.BrowseResponse != nil
}

func headerValue(packet []byte, header string) string {
	lines := bytes.Split(packet, []byte("\r\n"))
	prefix := strings.ToLower(header) + ":"
	for _, l := range lines {
		if len(l) <= len(prefix) {
			continue
		}
		if strings.EqualFold(string(l[:len(prefix)]), prefix) {
			return strings.TrimSpace(string(l[len(prefix):]))
		}
	}
	return ""
}

// rootDevice is the relevant subset of an upnp:rootDevice
// description XML.
type rootDevice struct {
	XMLName xml.Name `xml:"root"`
	URLBase string   `xml:"URLBase"`
	Device  device   `xml:"device"`
}

type device struct {
	DeviceType   string    `xml:"deviceType"`
	FriendlyName string    `xml:"friendlyName"`
	Manufacturer string    `xml:"manufacturer"`
	ModelName    string    `xml:"modelName"`
	UDN          string    `xml:"UDN"`
	Icons        []icon    `xml:"iconList>icon"`
	Services     []service `xml:"serviceList>service"`
	SubDevices   []device  `xml:"deviceList>device"`
}

type icon struct {
	MimeType string `xml:"mimetype"`
	Width    int    `xml:"width"`
	URL      string `xml:"url"`
}

type service struct {
	ServiceType string `xml:"serviceType"`
	ControlURL  string `xml:"controlURL"`
}

func fetchDeviceDescription(ctx context.Context, location string) (Server, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return Server{}, err
	}
	// The per-fetch deadline comes from ctx, so a caller can give a known-slow
	// server (a WD Twonky answers its device.xml far slower than a FRITZ!Box,
	// #733) more room than a bulk discovery sweep grants each candidate. A
	// hard-coded 6s client timeout used to cut the Twonky off with
	// "context deadline exceeded" while faster servers on the same LAN came
	// through, so its native STORED_MUSIC source worked but the phone browse
	// reported it offline. The 20s ceiling only guards a caller that passed a
	// deadline-less ctx; every real caller sets one.
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		Logger.Warn("dlna: device description fetch failed", "location", location, "err", err.Error())
		return Server{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		Logger.Warn("dlna: device description read failed", "location", location, "err", err.Error())
		return Server{}, err
	}
	var root rootDevice
	if err := xml.Unmarshal(body, &root); err != nil {
		// A SOAP envelope where the description belongs is the server refusing
		// this client rather than a broken server: a QNAP media server answered
		// every rootDesc.xml fetch from one PC with an envelope, while the
		// speaker on the same LAN was served the description normally. A
		// receiver allow-list on the server is the likely cause.
		refused := xmlRootName(body) == "Envelope"
		if logLimiter.allow("desc|" + location) {
			Logger.Warn("dlna: device description xml parse failed", "location", location,
				"status", resp.StatusCode, "contentType", resp.Header.Get("Content-Type"),
				"soapEnvelope", refused, "body", bodySnippet(body), "err", err.Error())
		}
		if refused {
			rerr := &DescriptionRefusedError{Location: location, Detail: soapFaultMessage(body, resp.StatusCode)}
			refusals.note(location, rerr.Detail, time.Now())
			return Server{}, rerr
		}
		return Server{}, fmt.Errorf("device xml: %w", err)
	}
	refusals.clear(location)

	baseURL, _ := url.Parse(location)
	if root.URLBase != "" {
		if u, err := url.Parse(root.URLBase); err == nil {
			baseURL = u
		}
	}

	s := Server{
		Location:     location,
		FriendlyName: root.Device.FriendlyName,
		Manufacturer: root.Device.Manufacturer,
		ModelName:    root.Device.ModelName,
		UDN:          root.Device.UDN,
		Address:      baseURL.Host,
	}

	// Walk root device + sub-devices to find ContentDirectory and
	// an icon. FRITZ!Box nests MediaServer under a root device.
	rawControl := ""
	var walk func(d device)
	walk = func(d device) {
		if s.CDSControlURL == "" {
			for _, svc := range d.Services {
				if svc.ServiceType == cdsServiceType {
					s.CDSControlURL = absURL(baseURL, svc.ControlURL)
					s.CDSDeviceType = strings.TrimSpace(d.DeviceType)
					rawControl = svc.ControlURL
					break
				}
			}
		}
		if s.IconURL == "" && len(d.Icons) > 0 {
			best := d.Icons[0]
			for _, ic := range d.Icons {
				if ic.Width > best.Width {
					best = ic
				}
			}
			s.IconURL = absURL(baseURL, best.URL)
		}
		if s.FriendlyName == "" {
			s.FriendlyName = d.FriendlyName
		}
		if s.UDN == "" {
			s.UDN = d.UDN
		}
		for _, sub := range d.SubDevices {
			walk(sub)
		}
	}
	walk(root.Device)

	Logger.Debug("dlna: device description resolved", "location", location,
		"urlBase", root.URLBase, "rootDeviceType", strings.TrimSpace(root.Device.DeviceType),
		"cdsDeviceType", s.CDSDeviceType, "controlURL", rawControl, "resolvedControlURL", s.CDSControlURL)
	return s, nil
}

// xmlRootName returns the local name of the first element in body, or "" when
// there is none (a body that is not XML at all, an empty body).
func xmlRootName(body []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = false
	for i := 0; i < 64; i++ {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local
		}
	}
	return ""
}

// bodySnippet is the first 300 bytes of a server response for a log line, cut
// on a rune boundary, with control characters flattened to spaces.
func bodySnippet(b []byte) string {
	const maxLen = 300
	if len(b) > maxLen {
		b = b[:maxLen]
		for len(b) > 0 && !utf8.Valid(b) {
			b = b[:len(b)-1]
		}
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, string(b))
}

// DescriptionRefusedError is returned when a server answers the device
// description request with a SOAP envelope (usually a fault) instead of the
// description. Seen from a media server whose client allow-list did not
// include the asking computer: the server is there, it just will not talk to
// this one.
type DescriptionRefusedError struct {
	Location string
	Detail   string
}

func (e *DescriptionRefusedError) Error() string {
	return fmt.Sprintf("the media server at %s refuses this computer: it answered the device description request with a SOAP fault (%s); allow this computer in the media server's settings",
		hostOf(e.Location), e.Detail)
}

func hostOf(location string) string {
	if u, err := url.Parse(location); err == nil && u.Host != "" {
		return u.Host
	}
	return location
}

// Refusal is one media server that refused to describe itself to this
// computer during a recent scan.
type Refusal struct {
	// Location is the device-description URL that was refused.
	Location string
	// Address is the host:port part of Location.
	Address string
	// Detail is the server's fault, made readable.
	Detail string
	// At is when the refusal was last seen.
	At time.Time
}

// refusalTTL is how long a refusal is reported after it was last seen.
const refusalTTL = 10 * time.Minute

type refusalBook struct {
	mu sync.Mutex
	m  map[string]Refusal
}

var refusals = &refusalBook{m: map[string]Refusal{}}

func (b *refusalBook) note(location, detail string, now time.Time) {
	b.mu.Lock()
	b.m[location] = Refusal{Location: location, Address: hostOf(location), Detail: detail, At: now}
	b.mu.Unlock()
}

func (b *refusalBook) clear(location string) {
	b.mu.Lock()
	delete(b.m, location)
	b.mu.Unlock()
}

func (b *refusalBook) recent(now time.Time) []Refusal {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Refusal, 0, len(b.m))
	for k, r := range b.m {
		if now.Sub(r.At) > refusalTTL {
			delete(b.m, k)
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Location < out[j].Location })
	return out
}

// RecentRefusals lists the media servers that answered their device
// description request with a SOAP envelope instead of the description within
// the last ten minutes, so a UI can say "this server refuses this computer"
// instead of silently not listing it. A later successful description of the
// same location clears the entry.
func RecentRefusals() []Refusal {
	return refusals.recent(time.Now())
}

// logLimiter keeps the per-location warnings that every scan would otherwise
// repeat down to one line per location per interval.
var logLimiter = &rateLimiter{every: 10 * time.Minute, last: map[string]time.Time{}}

type rateLimiter struct {
	mu    sync.Mutex
	every time.Duration
	last  map[string]time.Time
}

func (l *rateLimiter) allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if t, ok := l.last[key]; ok && now.Sub(t) < l.every {
		return false
	}
	if len(l.last) > 512 {
		l.last = map[string]time.Time{}
	}
	l.last[key] = now
	return true
}

func absURL(base *url.URL, ref string) string {
	if ref == "" || base == nil {
		return ref
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return base.ResolveReference(u).String()
}

// === Browse ===

// BrowseResult holds one page of a ContentDirectory:Browse response.
type BrowseResult struct {
	Containers   []Container
	Items        []Item
	TotalMatches int
	Returned     int
}

// Container is a folder / album / playlist node.
type Container struct {
	ID         string
	ParentID   string
	Title      string
	ChildCount int
}

// Item is a single playable object (track, photo, video). For the
// MVP the desktop app filters to audio items only.
type Item struct {
	ID          string
	ParentID    string
	Title       string
	Artist      string
	Album       string
	Class       string
	MimeType    string
	StreamURL   string
	AlbumArtURL string
	DurationSec int
}

// Browse calls ContentDirectory:Browse on the server. objectID "0"
// is the server root. start is the offset for paging, count the
// page size (0 means server default).
func Browse(ctx context.Context, srv Server, objectID string, start, count int) (BrowseResult, error) {
	raw, status, err := browseRaw(ctx, srv, objectID, start, count)
	if err != nil {
		return BrowseResult{}, err
	}
	if status != http.StatusOK {
		Logger.Warn("dlna: browse answered non-200", "status", status,
			"location", srv.Location, "controlURL", srv.CDSControlURL,
			"deviceType", srv.CDSDeviceType, "body", bodySnippet(raw))
		return BrowseResult{}, fmt.Errorf("browse failed: %s", soapFaultMessage(raw, status))
	}
	return parseBrowseResponse(raw)
}

// browseRaw posts one ContentDirectory:Browse and returns the raw answer with
// its HTTP status, leaving the interpretation to the caller.
func browseRaw(ctx context.Context, srv Server, objectID string, start, count int) ([]byte, int, error) {
	if srv.CDSControlURL == "" {
		return nil, 0, fmt.Errorf("server has no ContentDirectory control URL")
	}
	if objectID == "" {
		objectID = "0"
	}
	if count <= 0 {
		count = 50
	}
	body := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:Browse xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1"><ObjectID>%s</ObjectID><BrowseFlag>BrowseDirectChildren</BrowseFlag><Filter>*</Filter><StartingIndex>%d</StartingIndex><RequestedCount>%d</RequestedCount><SortCriteria></SortCriteria></u:Browse></s:Body></s:Envelope>`,
		xmlEscape(objectID), start, count)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.CDSControlURL, strings.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPACTION", `"urn:schemas-upnp-org:service:ContentDirectory:1#Browse"`)

	client := &http.Client{Timeout: SOAPClientCeiling}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return raw, resp.StatusCode, nil
}

// Search calls ContentDirectory:Search on the server, looking for audio items
// whose title, artist OR album contains query. Not every server implements the
// Search action (it is optional in the ContentDirectory spec); those answer a
// SOAP fault, surfaced here as an error, and the caller falls back to a bounded
// browse walk. The search always starts at container "0", the whole library.
//
// The criteria used to be title-only, which made the phone remote answer the
// same question differently from the desktop Library: the desktop loads a
// folder and filters it over title, artist and album (#666). Measured against
// the FRITZ!Box 6690 media server, a query for the artist "AVM" returned zero
// hits with the title-only criteria and one hit with this one, although the
// track's upnp:artist is "AVM GmbH".
func Search(ctx context.Context, srv Server, query string, count int) (BrowseResult, error) {
	q := escapeCriteriaValue(query)
	return searchWithCriteria(ctx, srv,
		`upnp:class derivedfrom "object.item.audioItem" and (dc:title contains "`+q+
			`" or upnp:artist contains "`+q+`" or upnp:album contains "`+q+`")`, count)
}

// SearchTitleOnly is the narrow form of Search, for servers that index titles
// and nothing else. Those reject the widened criteria outright (UPnPError 708,
// "unsupported or invalid search criteria") rather than answering it, so a
// caller whose widened Search failed asks again this way before falling back to
// the far more expensive browse walk.
func SearchTitleOnly(ctx context.Context, srv Server, query string, count int) (BrowseResult, error) {
	return searchWithCriteria(ctx, srv,
		`upnp:class derivedfrom "object.item.audioItem" and dc:title contains "`+
			escapeCriteriaValue(query)+`"`, count)
}

// escapeCriteriaValue guards the criteria string itself: the value is quoted
// inside the criteria, which is in turn XML-escaped into the SOAP body, so a
// quote the user typed must not be able to terminate the criteria string.
//
// The backslash goes first, and it is not decoration. UPnP criteria use the
// backslash as the escape character, so escaping only the quote turns a query
// ending in a backslash into `contains "x\"`, whose closing quote is now read
// as an escaped one: the criteria is unterminated and the server faults on a
// query the user simply typed. Escaping the backslash first makes that `x\\`,
// which closes correctly.
func escapeCriteriaValue(query string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(query)
}

func searchWithCriteria(ctx context.Context, srv Server, criteria string, count int) (BrowseResult, error) {
	if srv.CDSControlURL == "" {
		return BrowseResult{}, fmt.Errorf("server has no ContentDirectory control URL")
	}
	if count <= 0 {
		count = 50
	}
	body := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:Search xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1"><ContainerID>0</ContainerID><SearchCriteria>%s</SearchCriteria><Filter>*</Filter><StartingIndex>0</StartingIndex><RequestedCount>%d</RequestedCount><SortCriteria></SortCriteria></u:Search></s:Body></s:Envelope>`,
		xmlEscape(criteria), count)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.CDSControlURL, strings.NewReader(body))
	if err != nil {
		return BrowseResult{}, err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPACTION", `"urn:schemas-upnp-org:service:ContentDirectory:1#Search"`)

	client := &http.Client{Timeout: SOAPClientCeiling}
	resp, err := client.Do(req)
	if err != nil {
		return BrowseResult{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return BrowseResult{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return BrowseResult{}, fmt.Errorf("search failed: %s", soapFaultMessage(raw, resp.StatusCode))
	}
	return parseSearchResponse(raw)
}

// soapBrowseEnvelope is the relevant subset of a Browse SOAP
// response.
type soapBrowseEnvelope struct {
	XMLName xml.Name `xml:"Envelope"`
	Body    struct {
		BrowseResponse struct {
			Result         string `xml:"Result"`
			NumberReturned int    `xml:"NumberReturned"`
			TotalMatches   int    `xml:"TotalMatches"`
		} `xml:"BrowseResponse"`
	} `xml:"Body"`
}

// soapSearchEnvelope mirrors soapBrowseEnvelope for the Search action; the
// embedded DIDL-Lite payload is identical.
type soapSearchEnvelope struct {
	XMLName xml.Name `xml:"Envelope"`
	Body    struct {
		SearchResponse struct {
			Result         string `xml:"Result"`
			NumberReturned int    `xml:"NumberReturned"`
			TotalMatches   int    `xml:"TotalMatches"`
		} `xml:"SearchResponse"`
	} `xml:"Body"`
}

// parseSearchResponse decodes a Search SOAP response. Same shape as Browse,
// different response element; the DIDL conversion is shared (didlToResult).
func parseSearchResponse(raw []byte) (BrowseResult, error) {
	raw = stripIllegalXMLChars(raw)
	var env soapSearchEnvelope
	if err := xml.Unmarshal(raw, &env); err != nil {
		return BrowseResult{}, fmt.Errorf("soap envelope: %w", err)
	}
	r := env.Body.SearchResponse
	return didlToResult(r.Result, r.TotalMatches, r.NumberReturned)
}

// didlLite mirrors the embedded DIDL-Lite XML returned in <Result>.
type didlLite struct {
	XMLName    xml.Name        `xml:"DIDL-Lite"`
	Containers []didlContainer `xml:"container"`
	Items      []didlItem      `xml:"item"`
}

type didlContainer struct {
	ID         string `xml:"id,attr"`
	ParentID   string `xml:"parentID,attr"`
	ChildCount int    `xml:"childCount,attr"`
	Title      string `xml:"title"`
	Class      string `xml:"class"`
}

type didlItem struct {
	ID       string  `xml:"id,attr"`
	ParentID string  `xml:"parentID,attr"`
	Title    string  `xml:"title"`
	Class    string  `xml:"class"`
	Artist   string  `xml:"artist"`
	Album    string  `xml:"album"`
	AlbumArt string  `xml:"albumArtURI"`
	Res      []didlR `xml:"res"`
}

type didlR struct {
	ProtocolInfo string `xml:"protocolInfo,attr"`
	Duration     string `xml:"duration,attr"`
	Value        string `xml:",chardata"`
}

// stripIllegalXMLChars drops bytes/runes that are not valid characters in XML
// 1.0, keeping tab, newline and carriage return. DLNA servers that surface ID3
// metadata verbatim can leak control characters into their SOAP/DIDL responses
// (#262); Go's xml parser then rejects the entire document. Removing only the
// offending characters preserves the rest of the listing. Invalid UTF-8 is left
// as the replacement rune (U+FFFD), which is itself valid XML.
func stripIllegalXMLChars(b []byte) []byte {
	if utf8.Valid(b) && !hasIllegalXMLByte(b) {
		return b // common case: nothing to strip, no copy
	}
	out := make([]byte, 0, len(b))
	for _, r := range string(b) {
		if r == 0x09 || r == 0x0A || r == 0x0D ||
			(r >= 0x20 && r <= 0xD7FF) ||
			(r >= 0xE000 && r <= 0xFFFD) ||
			(r >= 0x10000 && r <= 0x10FFFF) {
			out = utf8.AppendRune(out, r)
		}
	}
	return out
}

// hasIllegalXMLByte is a cheap scan for the ASCII control bytes that are illegal
// in XML 1.0, so the all-valid common case avoids a full rune walk + copy.
func hasIllegalXMLByte(b []byte) bool {
	for _, c := range b {
		if c < 0x20 && c != 0x09 && c != 0x0A && c != 0x0D {
			return true
		}
	}
	return false
}

func parseBrowseResponse(raw []byte) (BrowseResult, error) {
	// Some DLNA servers embed raw control characters (e.g. U+0001 out of an ID3
	// comment or genre tag) into the metadata they return, which makes Go's strict
	// XML parser reject the WHOLE response ("illegal character code U+0001", #262).
	// Strip the characters that are illegal in XML 1.0 so the rest of the library
	// listing still parses, instead of failing the entire folder.
	raw = stripIllegalXMLChars(raw)
	var env soapBrowseEnvelope
	if err := xml.Unmarshal(raw, &env); err != nil {
		return BrowseResult{}, fmt.Errorf("soap envelope: %w", err)
	}
	r := env.Body.BrowseResponse
	return didlToResult(r.Result, r.TotalMatches, r.NumberReturned)
}

// didlToResult converts the DIDL-Lite payload embedded in a Browse or Search
// response into a BrowseResult.
func didlToResult(res string, total, returned int) (BrowseResult, error) {
	out := BrowseResult{TotalMatches: total, Returned: returned}
	if res == "" {
		return out, nil
	}
	var didl didlLite
	// The DIDL-Lite inside <Result> can carry the same bad characters once it is
	// un-escaped, so sanitise it too before the second parse.
	if err := xml.Unmarshal(stripIllegalXMLChars([]byte(res)), &didl); err != nil {
		return BrowseResult{}, fmt.Errorf("didl-lite: %w", err)
	}
	for _, c := range didl.Containers {
		out.Containers = append(out.Containers, Container{
			ID: c.ID, ParentID: c.ParentID, Title: c.Title,
			ChildCount: c.ChildCount,
		})
	}
	for _, it := range didl.Items {
		stream := ""
		mime := ""
		duration := 0
		if len(it.Res) > 0 {
			r := pickPlayableRes(it.Res)
			stream = r.Value
			mime = mimeFromProtocolInfo(r.ProtocolInfo)
			duration = parseHMS(r.Duration)
		}
		out.Items = append(out.Items, Item{
			ID: it.ID, ParentID: it.ParentID, Title: it.Title,
			Class: it.Class, Artist: it.Artist, Album: it.Album,
			AlbumArtURL: it.AlbumArt, StreamURL: stream,
			MimeType: mime, DurationSec: duration,
		})
	}
	return out, nil
}

// pickPlayableRes chooses the best <res> for a track. DLNA servers, Synology in
// particular, often expose several res entries per item: the original file AND
// one or more on-the-fly transcodes, in a server-decided order. STR used to take
// Res[0] blindly, which on some tracks is a transcoded or non-HTTP entry the
// Bose renderer cannot consume: it then sat at "stream starting" forever, while
// the same track played in the Bose app (which picks the original) and other
// tracks on the same server worked because their original happened to be first
// (#139). We score each res and prefer a directly-playable HTTP audio res that
// is the original rather than a transcode (DLNA.ORG_CI=0 or no CI flag),
// preserving server order among equals, and fall back to Res[0] so a server that
// lists only an unusual single res is never made worse.
func pickPlayableRes(rs []didlR) didlR {
	if len(rs) == 0 {
		return didlR{}
	}
	best, bestScore := -1, -1
	for i, r := range rs {
		if strings.TrimSpace(r.Value) == "" {
			continue
		}
		pi := strings.ToLower(r.ProtocolInfo)
		score := 0
		// Must be fetchable over HTTP by the speaker.
		if strings.HasPrefix(pi, "http-get:") || strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Value)), "http") {
			score += 4
		}
		// An actual audio stream (skips thumbnail/subtitle res some servers list).
		if strings.Contains(pi, ":audio/") {
			score += 2
		}
		// Prefer the original file over an on-the-fly transcode (DLNA.ORG_CI=1),
		// matching what the Bose app picks.
		if !strings.Contains(pi, "dlna.org_ci=1") {
			score++
		}
		if score > bestScore { // first max wins -> stable server order among equals
			best, bestScore = i, score
		}
	}
	if best < 0 {
		return rs[0]
	}
	return rs[best]
}

func mimeFromProtocolInfo(pi string) string {
	// protocolInfo is "http-get:*:audio/mpeg:*". We only need the
	// third field.
	parts := strings.Split(pi, ":")
	if len(parts) < 3 {
		return ""
	}
	return parts[2]
}

func parseHMS(d string) int {
	if d == "" {
		return 0
	}
	// "0:03:42" or "0:03:42.000"
	if idx := strings.Index(d, "."); idx >= 0 {
		d = d[:idx]
	}
	parts := strings.Split(d, ":")
	if len(parts) != 3 {
		return 0
	}
	h, m, s := 0, 0, 0
	fmt.Sscanf(parts[0], "%d", &h)
	fmt.Sscanf(parts[1], "%d", &m)
	fmt.Sscanf(parts[2], "%d", &s)
	return h*3600 + m*60 + s
}

func xmlEscape(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// upnpErrorText names the error codes a media server actually answers with, so
// the message says what happened rather than what number happened. Anything not
// listed keeps its bare code, which is still infinitely more use than a cut off
// XML envelope.
//
// From the ContentDirectory:1 spec plus the two every server produces in the
// field: 701 when the app asks for a container the server does not have, and
// 708 from servers whose Search only accepts a title criterion.
var upnpErrorText = map[string]string{
	"401": "invalid action",
	"402": "invalid arguments",
	"501": "action failed",
	"600": "argument value invalid",
	"701": "no such object",
	"709": "unsupported sort criteria",
	"708": "unsupported search criteria",
	"710": "no such container",
	"720": "cannot process the request",
}

// soapFaultMessage turns a server's SOAP fault into one readable line.
//
// A UPnP fault carries its meaning in <errorCode>, which sits at the very end
// of the envelope. Reporting the first 240 characters of the raw XML therefore
// showed the reader everything except the answer, and the error dialog then
// truncated even that (#929: a Synology DS918+ fault that stopped at
// "<UPnPError xmlns=..."). Falls back to the raw text when there is no fault to
// read, e.g. a plain HTML error page from something that is not a media server.
func soapFaultMessage(raw []byte, status int) string {
	var env struct {
		Code string `xml:"Body>Fault>detail>UPnPError>errorCode"`
		Desc string `xml:"Body>Fault>detail>UPnPError>errorDescription"`
		Str  string `xml:"Body>Fault>faultstring"`
	}
	if err := xml.Unmarshal(raw, &env); err == nil && env.Code != "" {
		desc := strings.TrimSpace(env.Desc)
		if desc == "" {
			desc = upnpErrorText[env.Code]
		}
		if desc != "" {
			return fmt.Sprintf("the server answered UPnP error %s (%s)", env.Code, desc)
		}
		return fmt.Sprintf("the server answered UPnP error %s", env.Code)
	}
	if s := strings.TrimSpace(env.Str); s != "" {
		return fmt.Sprintf("the server answered %q (HTTP %d)", s, status)
	}
	return fmt.Sprintf("HTTP %d: %s", status, truncate(strings.TrimSpace(string(raw)), 240))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// IsAudioItem reports whether the item is an audio track that the
// SoundTouch renderer can play. Photos, videos, m3u playlists and
// unrecognized items are filtered out by the Library UI.
func (it Item) IsAudioItem() bool {
	if strings.HasPrefix(strings.ToLower(it.MimeType), "audio/") {
		return true
	}
	c := strings.ToLower(it.Class)
	return strings.Contains(c, "audioitem") || strings.Contains(c, "musictrack")
}
