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
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
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

// Server is a single discovered DLNA MediaServer on the LAN.
type Server struct {
	// UDN is the unique device identifier (uuid:...), stable across
	// reboots. Used by the desktop app as the dropdown key.
	UDN string
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
}

// DiscoverServers sends an SSDP M-SEARCH for MediaServer devices,
// collects unique responses for the given timeout, then resolves
// each device description in parallel and returns the populated
// Server list. Honors ctx for cancellation.
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
	if timeout <= 0 {
		timeout = defaultDiscover
	}
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	mcAddr, err := net.ResolveUDPAddr("udp4", ssdpAddr)
	if err != nil {
		return nil, fmt.Errorf("ssdp resolve: %w", err)
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

	// Enumerate candidate source IPs. Skip loopback, link-local
	// (169.254.x.x), and any v6 addresses since SSDP here is v4 only.
	ips := candidateIPv4Addrs()
	if len(ips) == 0 {
		Logger.Warn("dlna: no usable IPv4 interfaces, falling back to wildcard")
		ips = []net.IP{net.IPv4zero}
	}
	Logger.Info("dlna: SSDP M-SEARCH starting", "interfaces", len(ips), "timeout", timeout.String())

	locationsMu := sync.Mutex{}
	locations := map[string]struct{}{}

	// collect reads SSDP responses on conn until the discovery deadline and
	// records each unique LOCATION. Shared by the per-interface multicast probes
	// and the same-host loopback unicast probe.
	collect := func(conn *net.UDPConn, label string) {
		if deadline, _ := dctx.Deadline(); !deadline.IsZero() {
			_ = conn.SetReadDeadline(deadline)
		}
		localHits := 0
		buf := make([]byte, 4096)
		for {
			select {
			case <-dctx.Done():
				Logger.Info("dlna: SSDP probe done", "probe", label, "newLocations", localHits)
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
			Logger.Info("dlna: SSDP response", "src", raddr.String(), "st", st, "location", loc)
			locationsMu.Lock()
			if _, dup := locations[loc]; !dup {
				locations[loc] = struct{}{}
				localHits++
			}
			locationsMu.Unlock()
		}
		Logger.Info("dlna: SSDP probe done", "probe", label, "newLocations", localHits)
	}

	var ifaceWg sync.WaitGroup
	for _, ip := range ips {
		ifaceWg.Add(1)
		go func(srcIP net.IP) {
			defer ifaceWg.Done()
			conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: srcIP, Port: 0})
			if err != nil {
				Logger.Warn("dlna: ListenUDP failed", "src", srcIP.String(), "err", err.Error())
				return
			}
			defer conn.Close()
			for i := 0; i < 2; i++ {
				_, _ = conn.WriteToUDP(typedMsg, mcAddr)
				_, _ = conn.WriteToUDP(allMsg, mcAddr)
				time.Sleep(80 * time.Millisecond)
			}
			collect(conn, srcIP.String())
		}(ip)
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
	ifaceWg.Wait()

	Logger.Info("dlna: SSDP M-SEARCH done", "totalLocations", len(locations))
	if len(locations) == 0 {
		return nil, nil
	}

	// Separate context for the description fetches. The discovery
	// context (dctx) is consumed by the SSDP read loop and may be
	// expired by the time we get here; using it for the HTTP
	// fetches would have every fetch fail with deadline exceeded.
	// Parent ctx is still alive (caller's overall budget).
	fctx, fcancel := context.WithTimeout(ctx, 8*time.Second)
	defer fcancel()

	type result struct {
		s   Server
		err error
	}
	results := make(chan result, len(locations))
	for loc := range locations {
		go func(loc string) {
			s, err := fetchDeviceDescription(fctx, loc)
			results <- result{s: s, err: err}
		}(loc)
	}

	out := make([]Server, 0, len(locations))
	seen := map[string]struct{}{}
	for i := 0; i < len(locations); i++ {
		r := <-results
		if r.err != nil || r.s.UDN == "" || r.s.CDSControlURL == "" {
			continue
		}
		if _, dup := seen[r.s.UDN]; dup {
			continue
		}
		seen[r.s.UDN] = struct{}{}
		out = append(out, r.s)
	}
	return out, nil
}

// candidateIPv4Addrs returns the routable IPv4 addresses we should
// send SSDP M-SEARCH from. Excludes loopback, link-local
// (169.254.x.x) and any non-up interface. The result drives the
// per-interface multicast send so a LAN that lives on Wi-Fi 1
// gets probed even when Wi-Fi 2 is connected to a different network
// (e.g. a Bose setup-AP) at the same time.
func candidateIPv4Addrs() []net.IP {
	var out []net.IP
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, iface := range ifaces {
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
			if ip4.IsLoopback() || ip4.IsLinkLocalUnicast() || ip4.IsLinkLocalMulticast() {
				continue
			}
			out = append(out, ip4)
		}
	}
	return out
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
	// 6s (was 4s): a NAS can serve its device.xml slowly. Without logging,
	// a fetch failure made a NAS that DID answer SSDP vanish with no trace (#110).
	client := &http.Client{Timeout: 6 * time.Second}
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
		Logger.Warn("dlna: device description xml parse failed", "location", location, "err", err.Error())
		return Server{}, fmt.Errorf("device xml: %w", err)
	}

	baseURL, _ := url.Parse(location)
	if root.URLBase != "" {
		if u, err := url.Parse(root.URLBase); err == nil {
			baseURL = u
		}
	}

	s := Server{
		FriendlyName: root.Device.FriendlyName,
		Manufacturer: root.Device.Manufacturer,
		ModelName:    root.Device.ModelName,
		UDN:          root.Device.UDN,
		Address:      baseURL.Host,
	}

	// Walk root device + sub-devices to find ContentDirectory and
	// an icon. FRITZ!Box nests MediaServer under a root device.
	var walk func(d device)
	walk = func(d device) {
		if s.CDSControlURL == "" {
			for _, svc := range d.Services {
				if svc.ServiceType == cdsServiceType {
					s.CDSControlURL = absURL(baseURL, svc.ControlURL)
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

	return s, nil
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
	if srv.CDSControlURL == "" {
		return BrowseResult{}, fmt.Errorf("server has no ContentDirectory control URL")
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
		return BrowseResult{}, err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPACTION", `"urn:schemas-upnp-org:service:ContentDirectory:1#Browse"`)

	client := &http.Client{Timeout: 10 * time.Second}
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
		return BrowseResult{}, fmt.Errorf("browse status %d: %s", resp.StatusCode, truncate(string(raw), 240))
	}
	return parseBrowseResponse(raw)
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

func parseBrowseResponse(raw []byte) (BrowseResult, error) {
	var env soapBrowseEnvelope
	if err := xml.Unmarshal(raw, &env); err != nil {
		return BrowseResult{}, fmt.Errorf("soap envelope: %w", err)
	}
	res := env.Body.BrowseResponse.Result
	if res == "" {
		return BrowseResult{
			TotalMatches: env.Body.BrowseResponse.TotalMatches,
			Returned:     env.Body.BrowseResponse.NumberReturned,
		}, nil
	}
	var didl didlLite
	if err := xml.Unmarshal([]byte(res), &didl); err != nil {
		return BrowseResult{}, fmt.Errorf("didl-lite: %w", err)
	}
	out := BrowseResult{
		TotalMatches: env.Body.BrowseResponse.TotalMatches,
		Returned:     env.Body.BrowseResponse.NumberReturned,
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
