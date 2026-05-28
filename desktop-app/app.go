// STR Desktop App: findet alle Sticks im LAN via mDNS, listet sie
// und steuert sie via REST API. Wails App, Backend ist Go, Frontend HTML/JS.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/discovery"
	"github.com/JRpersonal/streborn/dlna"
	"github.com/JRpersonal/streborn/sticksetup"
	"github.com/JRpersonal/streborn/wifiprofiles"
	"streborn-app/agentbin"
)

// App ist die zentrale State Struktur.
type App struct {
	ctx        context.Context
	logger     *slog.Logger
	logFile    *os.File // kept so ExportDiagnosticLogs can Sync before reading
	httpClient *http.Client

	// libraryServers caches the result of the most recent
	// ListMediaServers call so subsequent BrowseLibrary calls can
	// resolve a UDN to a Server without a fresh SSDP sweep on every
	// folder click. Cleared and rebuilt on ListMediaServers.
	libraryMu      sync.Mutex
	libraryServers map[string]dlna.Server
}

// NewApp erstellt eine neue App Instance.
func NewApp() *App {
	logger, logFile := newFileLogger(slog.LevelInfo)
	return &App{
		logger:         logger,
		logFile:        logFile,
		httpClient:     &http.Client{Timeout: 6 * time.Second},
		libraryServers: map[string]dlna.Server{},
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	// Verbose startup line so users always see SOMETHING in the
	// log when they hit "Save diagnostic logs", even on a session
	// where they did not poke any features that emit further logs.
	a.logger.Info("Desktop App startet",
		"version", appVersion,
		"build", appBuild,
		"logFile", LogFilePath(),
		"agentbinAvailable", agentbin.Available())
}

// BoxInfo is the speaker entry passed to the frontend for selection.
// Kind distinguishes STR-equipped speakers from stock Bose speakers
// that still need a USB-stick install.
type BoxInfo struct {
	Name         string `json:"name"`
	Host         string `json:"host"` // IPv4 for the REST API
	Port         int    `json:"port"` // typically 8888 for STR, 8090 for stock
	DeviceID     string `json:"deviceID"`
	FriendlyName string `json:"friendlyName"`
	Model        string `json:"model"`
	Version      string `json:"version"`
	// Build is the agent's build stamp (YYYY-MM-DD-HHMM) as
	// announced via mDNS TXT. Empty if the speaker runs an older
	// agent that does not yet broadcast build, or if Kind == "stock".
	// Used by the frontend update indicators to flag stamp drift
	// even when version strings match.
	Build string `json:"build"`
	// SerialNumber is the human-readable Bose PackagedProduct serial
	// (the sticker on the bottom of the speaker, e.g.
	// "069236P60560580AE"). Pulled from /info on 8090; empty if the
	// box was not reachable on that port during discovery. Used by
	// the Setup-target picker so users with two or three identical
	// speakers on the same LAN can tell them apart by something other
	// than the Bose-default friendly name "SoundTouch 20".
	SerialNumber string `json:"serialNumber"`
	// Kind is "str" for speakers running an STR agent, "stock" for
	// vanilla Bose SoundTouch speakers that the desktop app can
	// offer to flash. Frontend renders the two kinds differently.
	Kind string `json:"kind"`
}

// DiscoverBoxes durchsucht das LAN nach Sticks via mDNS. Wenn mDNS
// nichts findet (z.B. Windows Firewall blockt 5353, oder die Stock
// Firmware announct unter einem Service Namen den wir noch nicht
// kennen), startet ein einmaliger leichter HTTP Probe Sweep auf Port
// 8090 als Fallback. Der Fallback laeuft NICHT bei jeder Discovery
// und nur auf einem Port, damit ein erfolgreicher mDNS Lauf kein
// Portscan auf dem lokalen Netz triggert.
func (a *App) DiscoverBoxes(timeoutSec int) ([]BoxInfo, error) {
	if timeoutSec <= 0 {
		timeoutSec = 6
	}
	ctx, cancel := context.WithTimeout(a.ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	// mDNS gets the bulk of the budget. The fallback probe only fires
	// if mDNS came back empty.
	mdnsBudget := time.Duration(timeoutSec) * 8 * time.Second / 10
	mdnsCtx, mdnsCancel := context.WithTimeout(ctx, mdnsBudget)
	defer mdnsCancel()

	results, err := discovery.Browse(mdnsCtx, a.logger)
	if err != nil {
		return nil, fmt.Errorf("browse: %w", err)
	}

	seen := map[string]BoxInfo{}
	upsert := func(b BoxInfo) {
		if b.Host == "" {
			return
		}
		// Dedup primary key is host (the IPv4 address). Two records
		// at the same IP are the same physical device, regardless
		// of which port they expose (STR runs on 8888, stock Bose
		// API on 8090). Using DeviceID was fragile because some
		// Bose mDNS announcements (the stock _soundtouch._tcp
		// service surfaced through our v0.4.1 scan) do not include
		// MAC in their TXT, so STR and stock records for the same
		// speaker landed under different keys and the user saw the
		// box listed twice.
		key := b.Host
		prev, exists := seen[key]
		if exists {
			// STR announcement always wins over a stock entry for
			// the same physical device. If both are STR (or both
			// stock), the richer record wins (longer FriendlyName,
			// non-empty Version).
			if prev.Kind == "str" && b.Kind == "stock" {
				return
			}
			if prev.Kind == b.Kind {
				if len(prev.FriendlyName) >= len(b.FriendlyName) && prev.Version != "" {
					return
				}
			}
		}
		seen[key] = b
	}

	for inst := range results {
		host := pickReachableIP(inst.IPv4)
		if host == "" {
			continue
		}
		kind := string(inst.Kind)
		if kind == "" {
			kind = "str"
		}
		upsert(BoxInfo{
			Name:         inst.Name,
			Host:         host,
			Port:         inst.Port,
			DeviceID:     inst.DeviceID,
			FriendlyName: inst.FriendlyName,
			Model:        inst.Model,
			Version:      inst.Version,
			Build:        inst.Build,
			Kind:         kind,
		})
	}

	// Fallback only when mDNS turned up nothing. Probes two well-
	// known ports per host: 8090 (stock Bose web API) and 8888 (STR
	// agent web UI). Two ports across a /24 still stays well below
	// "this looks like a portscan" thresholds; we need both because
	// an STR-flashed speaker stops answering :8090 with the stock
	// /info shape and an unflashed Portable in setup-AP mode does
	// not announce mDNS at all on the home LAN. Observed live
	// 2026-05-23 on a Windows laptop where zeroconf-go returned 0
	// instances despite the ST10 on the same LAN answering :8888
	// with HTTP 200 — see #69-followup.
	mdnsHits := len(seen)
	a.logger.Info("discovery: mDNS phase done", "instancesFromMDNS", mdnsHits)

	// TCP fallback ALWAYS runs, not just when mDNS came back empty.
	// On Windows hosts with two active interfaces (home Wi-Fi + USB
	// Wi-Fi dongle for the Bose setup AP), zeroconf-go finishes its
	// browse as soon as ANY response arrives. Observed live 2026-05-24:
	// the Portable on 192.168.1.1 (Setup-AP, Wi-Fi 2) answered first,
	// browse closed, the ST10 on 192.168.178.66 (Wi-Fi 1) never made
	// it into the result channel even though both interfaces were
	// joined for multicast. Running the TCP sweep unconditionally
	// catches every speaker the user actually has — the upsert dedupe
	// downstream collapses any double-counts. Cost: ~12 s of parallel
	// HTTP probes per refresh; acceptable given the auto-refresh
	// cadence is throttled to a few times per minute.
	fallbackCtx, fallbackCancel := context.WithTimeout(a.ctx, 12*time.Second)
	var fbWG sync.WaitGroup
	var fbMu sync.Mutex
	var stockHits, strHits int
	fbWG.Add(2)
	go func() {
		defer fbWG.Done()
		hits := a.probeLANForStock(fallbackCtx)
		fbMu.Lock()
		defer fbMu.Unlock()
		stockHits = len(hits)
		for _, probed := range hits {
			upsert(probed)
		}
	}()
	go func() {
		defer fbWG.Done()
		hits := a.probeLANForSTR(fallbackCtx)
		fbMu.Lock()
		defer fbMu.Unlock()
		strHits = len(hits)
		for _, probed := range hits {
			upsert(probed)
		}
	}()
	fbWG.Wait()
	fallbackCancel()
	a.logger.Info("discovery: TCP fallback done", "stockHits", stockHits, "strHits", strHits)
	a.logger.Info("discovery: returning", "totalBoxes", len(seen), "fromMDNS", mdnsHits)

	// Enrich every box with the serial number and model from
	// /info on :8090. Stock boxes already have these from
	// probeStock, but STR-flashed boxes do not because the mDNS
	// TXT record never carried the Bose-printed serial. Without
	// this, users with two identical ST20s cannot tell them apart
	// in the Setup target picker. Run in parallel with a tight
	// per-box budget so a slow/dead :8090 cannot stall discovery.
	a.enrichSeenBoxes(ctx, seen)

	out := make([]BoxInfo, 0, len(seen))
	for _, b := range seen {
		out = append(out, b)
	}
	return out, nil
}

// enrichSeenBoxes fans out enrichBoxWithStockInfo for every box in
// seen that is still missing a SerialNumber, then writes the
// enriched record back into seen under the same key. Bounded
// parallelism (8 in flight) keeps the discovery latency low even
// on a LAN with many speakers; per-call timeout (1.5s, inside
// enrichBoxWithStockInfo) caps the worst case.
func (a *App) enrichSeenBoxes(ctx context.Context, seen map[string]BoxInfo) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	var mu sync.Mutex
	for key, b := range seen {
		if b.SerialNumber != "" && b.Model != "" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(key string, b BoxInfo) {
			defer wg.Done()
			defer func() { <-sem }()
			enriched := a.enrichBoxWithStockInfo(ctx, b)
			if enriched.SerialNumber == b.SerialNumber && enriched.Model == b.Model {
				return // nothing to update, /info was unreachable
			}
			mu.Lock()
			defer mu.Unlock()
			// The box may have been upserted again by the time we
			// got the lock (concurrent mDNS announcement). Only
			// overwrite the specific fields we enriched, leave the
			// rest untouched.
			if cur, ok := seen[key]; ok {
				if cur.SerialNumber == "" {
					cur.SerialNumber = enriched.SerialNumber
				}
				if cur.Model == "" {
					cur.Model = enriched.Model
				}
				seen[key] = cur
			}
		}(key, b)
	}
	wg.Wait()
}

// probeLANForStock walks every local IPv4 /24 and HTTP-probes each
// host on port 8090 for the stock Bose /info XML. This is the
// fallback path used when mDNS returned no speakers at all: we
// assume STR-equipped speakers will be found by mDNS, and the only
// reason to actively probe is to surface a vanilla SoundTouch that
// needs the install. Single port keeps the sweep below "looks like
// a portscan" thresholds on consumer routers and IDS-enabled APs.
func (a *App) probeLANForStock(ctx context.Context) []BoxInfo {
	subnets := localIPv4Subnets()
	if len(subnets) == 0 {
		return nil
	}

	hits := make(chan BoxInfo, 32)
	sem := make(chan struct{}, 32)
	var wg sync.WaitGroup

	probeOne := func(ip string) {
		defer wg.Done()
		defer func() { <-sem }()
		if b, ok := probeStock(ctx, ip); ok {
			hits <- b
		}
	}

	for _, subnet := range subnets {
		base := subnet
		for i := 1; i <= 254; i++ {
			select {
			case <-ctx.Done():
				goto done
			case sem <- struct{}{}:
			}
			wg.Add(1)
			go probeOne(base + fmt.Sprintf("%d", i))
		}
	}
done:
	go func() { wg.Wait(); close(hits) }()

	var out []BoxInfo
	for h := range hits {
		out = append(out, h)
	}
	return out
}

// localIPv4Subnets returns the unique "first three octets + dot" of
// every non-loopback non-link-local IPv4 interface address on this
// host. The probe sweep uses these as scan bases. Filtered to /24-ish
// private ranges so we never sweep public addresses by accident.
func localIPv4Subnets() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip4 := ipnet.IP.To4()
		if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
			continue
		}
		// Only sweep RFC1918 ranges. Skips the carrier-grade NAT and
		// public IPs that should never host a SoundTouch.
		if !(ip4[0] == 10 ||
			(ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31) ||
			(ip4[0] == 192 && ip4[1] == 168)) {
			continue
		}
		base := fmt.Sprintf("%d.%d.%d.", ip4[0], ip4[1], ip4[2])
		if _, dup := seen[base]; dup {
			continue
		}
		seen[base] = struct{}{}
		out = append(out, base)
	}
	return out
}

// probeLANForSTR walks every local IPv4 /24 and HTTP-probes each
// host on port 8888 for the STR agent's /api/agent/version JSON. The
// counterpart to probeLANForStock: when mDNS returns nothing AND no
// stock box answers /info, we still want STR-flashed speakers in the
// box list so the user can press play. Same single-port-per-host
// budget, same parallelism cap.
func (a *App) probeLANForSTR(ctx context.Context) []BoxInfo {
	subnets := localIPv4Subnets()
	if len(subnets) == 0 {
		return nil
	}

	hits := make(chan BoxInfo, 32)
	sem := make(chan struct{}, 32)
	var wg sync.WaitGroup

	probeOne := func(ip string) {
		defer wg.Done()
		defer func() { <-sem }()
		if b, ok := probeSTR(ctx, ip); ok {
			hits <- b
		}
	}

	for _, subnet := range subnets {
		base := subnet
		for i := 1; i <= 254; i++ {
			select {
			case <-ctx.Done():
				goto done
			case sem <- struct{}{}:
			}
			wg.Add(1)
			go probeOne(base + fmt.Sprintf("%d", i))
		}
	}
done:
	go func() { wg.Wait(); close(hits) }()

	var out []BoxInfo
	for h := range hits {
		out = append(out, h)
	}
	return out
}

// probeSTR checks ip:17008/api/agent/version for the STR agent JSON
// envelope. Port 17008 is the chipset-whitelisted SoftwareUpdate slot
// that STR hijacks via an LD_PRELOAD accept() shim — the only port
// reachable from outside on every SoundTouch variant we have tested
// (Portable/taigan verified, ST10/20/30 same chipset architecture).
// On hit returns a BoxInfo with Kind="str" so the upsert dedupe in
// DiscoverBoxes treats it the same as an mDNS-announced STR speaker.
// Tight timeout: 1.2s per host so a /24 sweep stays under ~10s wall
// time across 32-way fan-out.
//
// :8888 is no longer probed externally because (a) the chipset
// firmware drops :8888 SYNs on Series-I boxes (Brecht, deqw, the
// Portable), (b) on Series-II it works but adds a second probe pass
// for no gain over the now-universal :17008. STR still binds :8888
// locally for the marge stub and self-probes.
//
// On hit we also pull /info from :8090 on the same box — the Bose
// firmware keeps answering that endpoint even after STR is installed,
// and without it the box list shows "str-192.168.x.x" with no
// FriendlyName/DeviceID/Model, which the frontend renders as if the
// box were unprovisioned.
func probeSTR(ctx context.Context, ip string) (BoxInfo, bool) {
	url := fmt.Sprintf("http://%s:17008/api/agent/version", ip)
	body, ok := httpGetSmall(ctx, url, 1200*time.Millisecond, 1024)
	if !ok {
		return BoxInfo{}, false
	}
	s := string(body)
	// Cheap sniff for the JSON shape {"build":"...","version":"..."}
	// without pulling in encoding/json on the hot path.
	if !strings.Contains(s, `"version"`) {
		return BoxInfo{}, false
	}
	version := jsonStringField(s, "version")
	build := jsonStringField(s, "build")

	box := BoxInfo{
		Name:    "str-" + ip,
		Host:    ip,
		Port:    17008,
		Version: version,
		Build:   build,
		Kind:    "str",
	}
	// Best-effort enrichment from the underlying Bose firmware's
	// /info endpoint. Failure is OK: caller still gets a usable
	// box, just less labelled.
	if info, ok := probeStock(ctx, ip); ok {
		box.FriendlyName = info.FriendlyName
		box.Model = info.Model
		box.DeviceID = info.DeviceID
		box.SerialNumber = info.SerialNumber
	}
	return box, true
}

// jsonStringField pulls the value of a top-level string field from
// a small JSON envelope by substring scanning. Matches `"key":"val"`
// optionally separated by whitespace; returns "" on no match. Used
// for the STR /api/agent/version probe which has a known fixed
// shape and one of two short fields per call — adding encoding/json
// for that one call would bloat the desktop binary's startup graph
// for no observable benefit.
func jsonStringField(s, key string) string {
	needle := `"` + key + `"`
	i := strings.Index(s, needle)
	if i < 0 {
		return ""
	}
	rest := s[i+len(needle):]
	c := strings.IndexByte(rest, ':')
	if c < 0 {
		return ""
	}
	rest = rest[c+1:]
	for len(rest) > 0 && (rest[0] == ' ' || rest[0] == '\t') {
		rest = rest[1:]
	}
	if len(rest) == 0 || rest[0] != '"' {
		return ""
	}
	rest = rest[1:]
	e := strings.IndexByte(rest, '"')
	if e < 0 {
		return ""
	}
	return rest[:e]
}

// probeStock checks ip:8090/info for the Bose SoundTouch device XML.
// Conservative timeouts so a sweep across 254 hosts stays cheap on a
// LAN where most addresses do not respond.
func probeStock(ctx context.Context, ip string) (BoxInfo, bool) {
	url := fmt.Sprintf("http://%s:8090/info", ip)
	body, ok := httpGetSmall(ctx, url, 1200*time.Millisecond, 4096)
	if !ok {
		return BoxInfo{}, false
	}
	s := string(body)
	if !strings.Contains(s, "<info ") || !strings.Contains(s, "deviceID=") {
		return BoxInfo{}, false
	}
	deviceID := strings.ToUpper(extractAttr(s, "deviceID"))
	name := extractTag(s, "name")
	model := extractTag(s, "type")
	serial := extractPackagedProductSerial(s)
	return BoxInfo{
		Name:         "stock-" + lastN(deviceID, 6),
		Host:         ip,
		Port:         8090,
		DeviceID:     deviceID,
		FriendlyName: name,
		Model:        model,
		SerialNumber: serial,
		Kind:         "stock",
	}, true
}

// extractPackagedProductSerial pulls the human-readable Bose serial
// out of the /info XML. The XML has multiple <component> blocks; the
// one that matches the physical sticker on the speaker is the one
// with <componentCategory>PackagedProduct</componentCategory> (the
// SCM block has the mainboard serial, which is different and not
// printed anywhere the user can see). Returns the first match or
// "" if no PackagedProduct component exists.
//
// We parse with substring scanning rather than encoding/xml because
// the Bose /info XML is small, well-structured, and we already use
// the same approach for other tags here. No new dependencies.
func extractPackagedProductSerial(infoXML string) string {
	const cat = "<componentCategory>PackagedProduct</componentCategory>"
	idx := strings.Index(infoXML, cat)
	if idx < 0 {
		return ""
	}
	// Walk forward to the next </component> closing tag and pull
	// the <serialNumber>...</serialNumber> inside this block.
	end := strings.Index(infoXML[idx:], "</component>")
	if end < 0 {
		return ""
	}
	block := infoXML[idx : idx+end]
	const open, close = "<serialNumber>", "</serialNumber>"
	s := strings.Index(block, open)
	if s < 0 {
		return ""
	}
	e := strings.Index(block[s+len(open):], close)
	if e < 0 {
		return ""
	}
	return strings.TrimSpace(block[s+len(open) : s+len(open)+e])
}

// enrichBoxWithStockInfo fetches /info on :8090 for an already-known
// box and copies Model + SerialNumber into the BoxInfo if they were
// missing. Used to give STR-flashed speakers the same identifying
// info as stock ones in the Setup target picker, where users with
// two identical ST20s rely on the serial sticker to tell them
// apart. Best-effort and short-timeout: a slow or missing /info
// just leaves the fields empty and the picker still renders.
func (a *App) enrichBoxWithStockInfo(ctx context.Context, b BoxInfo) BoxInfo {
	if b.Host == "" {
		return b
	}
	if b.SerialNumber != "" && b.Model != "" {
		return b
	}
	probeCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	url := fmt.Sprintf("http://%s:8090/info", b.Host)
	body, ok := httpGetSmall(probeCtx, url, 1200*time.Millisecond, 4096)
	if !ok {
		return b
	}
	xml := string(body)
	if b.Model == "" {
		if m := extractTag(xml, "type"); m != "" {
			b.Model = m
		}
	}
	if b.SerialNumber == "" {
		if sn := extractPackagedProductSerial(xml); sn != "" {
			b.SerialNumber = sn
		}
	}
	return b
}

func httpGetSmall(ctx context.Context, url string, timeout time.Duration, max int64) ([]byte, bool) {
	c, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, url, nil)
	if err != nil {
		return nil, false
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max))
	if err != nil {
		return nil, false
	}
	return b, true
}

func extractAttr(xml, key string) string {
	needle := key + "=\""
	i := strings.Index(xml, needle)
	if i < 0 {
		return ""
	}
	j := strings.Index(xml[i+len(needle):], "\"")
	if j < 0 {
		return ""
	}
	return xml[i+len(needle) : i+len(needle)+j]
}

func extractTag(xml, tag string) string {
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	i := strings.Index(xml, open)
	if i < 0 {
		return ""
	}
	j := strings.Index(xml[i+len(open):], close)
	if j < 0 {
		return ""
	}
	return xml[i+len(open) : i+len(open)+j]
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// Preset Format passt zu internal/presets.Preset JSON.
type Preset struct {
	Slot      int    `json:"slot"`
	Name      string `json:"name"`
	StreamURL string `json:"stream_url"`
	Type      string `json:"type"`
	Art       string `json:"art,omitempty"`
}

func (a *App) baseURL(host string, port int) string {
	// Default to the chipset-whitelisted hijack port. Classic frontend
	// callers that pre-discovery hard-coded 8888 still work because
	// they pass port=8888 explicitly; this fallback only kicks in for
	// freshly-resolved boxes where port was left zero.
	if port == 0 {
		port = 17008
	}
	return fmt.Sprintf("http://%s:%d", host, port)
}

// GetPresets ruft GET /api/presets des angegebenen Sticks.
func (a *App) GetPresets(host string, port int) ([]Preset, error) {
	url := a.baseURL(host, port) + "/api/presets"
	resp, err := a.httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	var out []Preset
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetPreset macht PUT /api/presets/<slot>. art ist die Sender Logo URL,
// wird beim Play als upnp:albumArtURI an die Box geschickt.
func (a *App) SetPreset(host string, port int, slot int, name, streamURL, art string) error {
	url := fmt.Sprintf("%s/api/presets/%d", a.baseURL(host, port), slot)
	body, _ := json.Marshal(Preset{Slot: slot, Name: name, StreamURL: streamURL, Type: "radio", Art: art})
	req, _ := http.NewRequestWithContext(a.ctx, http.MethodPut, url, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// DeletePreset macht DELETE /api/presets/<slot>.
func (a *App) DeletePreset(host string, port int, slot int) error {
	url := fmt.Sprintf("%s/api/presets/%d", a.baseURL(host, port), slot)
	req, _ := http.NewRequestWithContext(a.ctx, http.MethodDelete, url, nil)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// PlaySlot triggert POST /api/play/<slot>.
func (a *App) PlaySlot(host string, port int, slot int) error {
	url := fmt.Sprintf("%s/api/play/%d", a.baseURL(host, port), slot)
	resp, err := a.httpClient.Post(url, "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s", friendlyError(resp))
	}
	return nil
}

// PlayURL triggert POST /api/play mit beliebigem Stream URL. icon ist
// die Sender Logo URL (wird auf der Box angezeigt), uuid ermoeglicht
// dass radio-browser den Klick zaehlt.
func (a *App) PlayURL(host string, port int, streamURL, title, icon, uuid string) error {
	url := a.baseURL(host, port) + "/api/play"
	body, _ := json.Marshal(map[string]string{
		"url":   streamURL,
		"title": title,
		"icon":  icon,
		"uuid":  uuid,
	})
	resp, err := a.httpClient.Post(url, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s", friendlyError(resp))
	}
	return nil
}

// BoxSettings holt name/volume/bass/network/sources der Box via Stick.
func (a *App) BoxSettings(host string, port int) (map[string]any, error) {
	url := a.baseURL(host, port) + "/api/box/settings"
	resp, err := a.httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetBoxName aendert den Anzeigenamen der Bose Box.
func (a *App) SetBoxName(host string, port int, name string) error {
	return a.boxPut(host, port, "/api/box/name", map[string]string{"name": name})
}

// SetBoxVolume setzt die Lautstaerke (0-100).
func (a *App) SetBoxVolume(host string, port int, value int) error {
	return a.boxPut(host, port, "/api/box/volume", map[string]int{"value": value})
}

// SetBoxBass setzt den Bass Wert (Range pro Box, ST10 z.B. -9..0).
func (a *App) SetBoxBass(host string, port int, value int) error {
	return a.boxPut(host, port, "/api/box/bass", map[string]int{"value": value})
}

// SelectBoxSource schaltet die Box auf eine andere Quelle: "AUX",
// "BLUETOOTH", "STANDBY". Stick Agent uebersetzt das in den passenden
// /select bzw /key Aufruf an die Bose REST API.
func (a *App) SelectBoxSource(host string, port int, source string) error {
	return a.boxPut(host, port, "/api/box/source", map[string]string{"source": source})
}

func (a *App) boxPut(host string, port int, path string, body any) error {
	url := a.baseURL(host, port) + path
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(a.ctx, http.MethodPut, url, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		bb, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(bb))
	}
	return nil
}

// SyncBoxPresets schickt alle Stick Presets erneut an die Box damit
// die Hardware Preset Tasten 1-6 funktionieren. Wird vom "Hardware
// Tasten reparieren" Button im Settings Tab benutzt.
func (a *App) SyncBoxPresets(host string, port int) (map[string]any, error) {
	url := a.baseURL(host, port) + "/api/box/sync-presets"
	resp, err := a.httpClient.Post(url, "application/json", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}
	// Wenn der Stick Agent zu alt ist und den Endpoint nicht kennt,
	// fallback auf den Default Handler liefert HTML statt JSON. Pruefen
	// und freundlich melden.
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "json") {
		return nil, fmt.Errorf("stick agent is too old for this operation. Please update the stick first (update banner at the top).")
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// RebootBox loest einen Neustart der Bose Box aus (via Stick Agent
// shell `reboot`). Damit greifen frische Setup Wizard Configs auf dem
// USB Stick sofort, ohne dauerhaftes Polling im Agent.
func (a *App) RebootBox(host string, port int) error {
	url := a.baseURL(host, port) + "/api/box/reboot"
	resp, err := a.httpClient.Post(url, "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// VoteStation gibt einem Sender bei radio-browser einen Daumen hoch.
// Best Effort; Fehler wird zurueckgegeben aber muss nicht angezeigt werden.
func (a *App) VoteStation(host string, port int, uuid string) error {
	if uuid == "" {
		return nil
	}
	url := fmt.Sprintf("%s/api/radio/vote/%s", a.baseURL(host, port), uuid)
	resp, err := a.httpClient.Post(url, "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("vote status %d", resp.StatusCode)
	}
	return nil
}

// friendlyError extrahiert das `detail` Feld aus der Stick API Fehler
// Antwort, falls vorhanden. Fallback: der Rohbody.
func friendlyError(resp *http.Response) string {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var m map[string]any
	if err := json.Unmarshal(b, &m); err == nil {
		if d, ok := m["detail"].(string); ok && d != "" {
			return d
		}
		if e, ok := m["error"].(string); ok && e != "" {
			return e
		}
	}
	return string(b)
}

// Pause / Stop pro Box.
func (a *App) Pause(host string, port int) error { return a.doAction(host, port, "pause") }
func (a *App) Stop(host string, port int) error  { return a.doAction(host, port, "stop") }

func (a *App) doAction(host string, port int, action string) error {
	url := fmt.Sprintf("%s/api/%s", a.baseURL(host, port), action)
	resp, err := a.httpClient.Post(url, "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// pickReachableIP waehlt aus den IPs die der Stick via mDNS announct die
// vom aktuellen LAN aus erreichbare. Box's USB Gadget Interface
// (203.0.113.x) ist nicht aus dem WLAN routbar; dieselbe Box announct
// auch ihre echte WLAN IP, die nehmen wir.
//
// Priorisierung:
//
//  1. Private LAN Ranges (RFC 1918): 192.168/16, 10/8, 172.16/12
//  2. Link Local: 169.254/16
//  3. Public IPs (unwahrscheinlich)
//
// Skip: 203.0.113/24 (Documentation TEST-NET-3, Box USB Gadget),
// 127/8 Loopback.
func pickReachableIP(ips []string) string {
	if len(ips) == 0 {
		return ""
	}
	var lan, linkLocal, public string
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil || ip.IsLoopback() {
			continue
		}
		// USB Gadget TEST-NET-3 ist nicht routbar
		if strings.HasPrefix(ipStr, "203.0.113.") {
			continue
		}
		if ip.IsPrivate() {
			if lan == "" {
				lan = ipStr
			}
			continue
		}
		if ip.IsLinkLocalUnicast() {
			if linkLocal == "" {
				linkLocal = ipStr
			}
			continue
		}
		if public == "" {
			public = ipStr
		}
	}
	// No "default: return ips[0]" fallback. If the only IP we got
	// was loopback or TEST-NET-3, returning it would cause the
	// desktop app to show an entry that cannot actually be reached
	// (and dedup against the real entry would fail because the IPs
	// differ). Better to drop the unreachable record and let the
	// other discovery path or a refresh pick up the real IP.
	switch {
	case lan != "":
		return lan
	case linkLocal != "":
		return linkLocal
	case public != "":
		return public
	default:
		return ""
	}
}

// ---- Stick Setup ----

// ListDrives liefert alle entfernbaren Volumes die als Stick Ziel taugen.
// Frontend nutzt das im Setup Wizard.
func (a *App) ListDrives() ([]sticksetup.Drive, error) {
	return sticksetup.ListDrives()
}

// FormatStick formatiert den Stick neu als FAT32. ACHTUNG: alle Daten
// gehen verloren. Wird vor WriteStickFiles aufgerufen wenn der User die
// "Stick zuerst formatieren" Checkbox aktiviert hat.
func (a *App) FormatStick(targetPath string) error {
	return sticksetup.FormatFAT32(targetPath, "REBORN")
}

// WriteStickFiles bestueckt das angegebene Volume mit allen noetigen
// Files (Templates plus eingebettetes Stick Agent Binary). Das Binary
// ist beim App Build embedded und braucht keinen Pfad vom User.
// Die App Version PLUS Build Stamp wird in version.txt geschrieben
// (Format "1.0.0+2026-05-15-2202") damit der Update Detector auch
// bei gleicher Versionsnummer Build Unterschiede erkennt.
func (a *App) WriteStickFiles(targetPath string) ([]string, error) {
	v := appVersion
	if appBuild != "" && appBuild != "dev" {
		v = appVersion + "+" + appBuild
	}
	return sticksetup.WriteStickFiles(targetPath, agentbin.Bytes(), v)
}

// WriteWLANConfig schreibt eine WLAN Konfig auf den Stick. Optional vor
// dem Eject; Box's run.sh erkennt das beim ersten Boot.
func (a *App) WriteWLANConfig(targetPath, ssid, password string) error {
	return sticksetup.WriteWLANConfig(targetPath, sticksetup.WLANConfig{
		SSID: ssid, Password: password,
	})
}

// WriteRegionConfig schreibt eine region.conf JSON Datei (ISO 3166-1
// alpha-2 Country Code) auf den Stick. Stick persistiert das beim Boot
// nach NAND und nutzt es als Default fuer Radio Suche und Sprache.
func (a *App) WriteRegionConfig(targetPath, country string) error {
	return sticksetup.WriteRegionConfig(targetPath, sticksetup.RegionConfig{Country: country})
}

// WriteNameConfig schreibt eine name.conf JSON Datei mit dem vom User
// gewuenschten Box Namen auf den Stick. Stick wendet den beim ersten
// Boot via Bose REST API auf die Box an und haengt die UID Box ID an.
func (a *App) WriteNameConfig(targetPath, name string) error {
	return sticksetup.WriteNameConfig(targetPath, sticksetup.NameConfig{Name: name})
}

// ListWiFiProfiles liefert die gespeicherten WLAN Profile vom Host OS.
// Frontend nutzt das als Dropdown im Setup damit der User die SSID nicht
// abtippen muss.
func (a *App) ListWiFiProfiles() ([]wifiprofiles.Profile, error) {
	return wifiprofiles.List()
}

// TryWiFiPassword versucht das gespeicherte Passwort fuer eine SSID
// auszulesen. Auf Windows funktioniert das fuer Profile die der User
// selbst gespeichert hat ohne Admin Rechte. Auf Mac/Linux braucht es ggf.
// User Consent. Returns leer wenn nichts gefunden.
func (a *App) TryWiFiPassword(ssid string) string {
	pw, _ := wifiprofiles.TryPassword(ssid)
	return pw
}

// CurrentWiFi liefert die SSID des aktuell verbundenen WLAN. Wird im UI
// als Default in der Dropdown ausgewaehlt.
func (a *App) CurrentWiFi() string {
	return wifiprofiles.CurrentSSID()
}

// IsBoseStick true wenn auf dem Volume schon ein STR liegt.
func (a *App) IsBoseStick(path string) bool {
	return sticksetup.IsBoseStick(path)
}

// StickVersion liest die version.txt vom Stick.
func (a *App) StickVersion(path string) string {
	return sticksetup.StickVersion(path)
}

// StickConfigs liefert noch nicht applizierte Setup Konfigs vom Stick
// (wlan, region, name). Wird zum Vorbefuellen des Wizards genutzt.
func (a *App) StickConfigs(path string) sticksetup.StickConfigs {
	return sticksetup.ReadStickConfigs(path)
}

// AppVersion liefert die Semver Version der laufenden App.
func (a *App) AppVersion() string { return appVersion }

// AppInfo liefert App Metadaten (Version, Build, Autor, URLs) fuer
// About Dialog, Footer und Auto Update Check.
//
// UpdateManifestURL zeigt auf eine kleine JSON Datei der Form
//
//	{"version":"1.1.0","build":"2026-06-01-0900","downloadUrl":"https://.../app-windows-amd64.exe","notes":"..."}
//
// Die App prueft beim Start ob die remote Version groesser ist als ihre
// eigene und zeigt dann ein Update Banner. Leer = Auto Update aus.
type AppInfo struct {
	Version           string `json:"version"`
	Build             string `json:"build"`
	Author            string `json:"author"`
	GitHubURL         string `json:"githubUrl"`
	WebsiteURL        string `json:"websiteUrl"`
	DonateURL         string `json:"donateUrl"`
	DonateSlogan      string `json:"donateSlogan"`
	UpdateManifestURL string `json:"updateManifestUrl"`
}

// Versionen werden ueber -ldflags X im Build gesetzt; defaults nur zum
// Entwickeln.
var (
	appVersion = "1.0.0"
	appBuild   = "dev"
)

func (a *App) AppInfo() AppInfo {
	return AppInfo{
		Version:           appVersion,
		Build:             appBuild,
		Author:            "Jens Roggenfelder (JRpersonal)",
		GitHubURL:         "https://github.com/JRpersonal/streborn",
		WebsiteURL:        "https://st-reborn.de",
		DonateURL:         "", // populated once the PayPal link on the website is live
		// DonateSlogan is left empty so the frontend renders the
		// locale-aware fallback from the i18n bundle. Hardcoding
		// German here would shadow the bundle for every locale.
		DonateSlogan:      "",
		UpdateManifestURL: "", // populated once the manifest URL is fixed
	}
}

// CheckAppUpdate fetches the UpdateManifestURL and returns the
// manifest when the remote version is greater than the running one.
// kein Manifest URL gesetzt ist oder Version gleich, leere Map.
func (a *App) CheckAppUpdate() (map[string]string, error) {
	info := a.AppInfo()
	if info.UpdateManifestURL == "" {
		return map[string]string{}, nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, info.UpdateManifestURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest status %d", resp.StatusCode)
	}
	var m map[string]string
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&m); err != nil {
		return nil, err
	}
	rv := m["version"]
	if rv == "" || rv == info.Version {
		return map[string]string{}, nil
	}
	return m, nil
}

// EjectDrive wirft den Stick aus damit der User ihn entnehmen kann.
func (a *App) EjectDrive(path string) error {
	return sticksetup.Eject(path)
}

// BoxAgentVersion fragt die Stick Agent Version der Box ab.
// Returns {version, build}.
func (a *App) BoxAgentVersion(host string, port int) (map[string]string, error) {
	url := a.baseURL(host, port) + "/api/agent/version"
	resp, err := a.httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var out map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateBoxAgent schickt das eingebettete ARM Binary an die Box. Box
// schreibt es atomar nach NAND und restartet sich selbst (rc.local
// startet sie wieder). Returns nach erfolgreichem Upload, vor Box Restart.
func (a *App) UpdateBoxAgent(host string, port int) error {
	bin := agentbin.Bytes()
	if len(bin) == 0 {
		return fmt.Errorf("no embedded stick binary available")
	}
	url := a.baseURL(host, port) + "/api/agent/update"
	req, err := http.NewRequestWithContext(a.ctx, http.MethodPost, url, strings.NewReader(string(bin)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = int64(len(bin))
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// Status liefert das now_playing XML als String. Frontend kann selber
// regex-parsen.
func (a *App) Status(host string, port int) (string, error) {
	url := a.baseURL(host, port) + "/api/status"
	resp, err := a.httpClient.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	return string(b), nil
}

// === Library (DLNA / UPnP MediaServer browsing) ===
//
// MVP scope: discover MediaServers on the LAN, browse one server's
// ContentDirectory tree, play a track via the existing PlayURL path,
// optionally save the track as one of the six STR presets via the
// existing SetPreset path. No queue, no search, no transcoding.
// Audio items only; the frontend filters the rest out.

// LibraryServer is the flat DTO sent to the frontend dropdown.
// Mirrors dlna.Server but trims it to JSON-friendly fields.
type LibraryServer struct {
	UDN          string `json:"udn"`
	FriendlyName string `json:"friendlyName"`
	Manufacturer string `json:"manufacturer"`
	ModelName    string `json:"modelName"`
	IconURL      string `json:"iconURL"`
	Address      string `json:"address"`
}

// LibraryContainer is a folder / album node in the browse view.
type LibraryContainer struct {
	ID         string `json:"id"`
	ParentID   string `json:"parentID"`
	Title      string `json:"title"`
	ChildCount int    `json:"childCount"`
}

// LibraryItem is a single playable track.
type LibraryItem struct {
	ID          string `json:"id"`
	ParentID    string `json:"parentID"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
	MimeType    string `json:"mimeType"`
	StreamURL   string `json:"streamURL"`
	AlbumArtURL string `json:"albumArtURL"`
	DurationSec int    `json:"durationSec"`
}

// LibraryPage is one page of a browse call.
type LibraryPage struct {
	Containers   []LibraryContainer `json:"containers"`
	Items        []LibraryItem      `json:"items"`
	TotalMatches int                `json:"totalMatches"`
	Returned     int                `json:"returned"`
}

// ListMediaServers does an SSDP sweep for DLNA MediaServers on the
// LAN and returns the list. Result is cached so BrowseLibrary can
// look up the server by UDN without rediscovering.
func (a *App) ListMediaServers(timeoutSec int) ([]LibraryServer, error) {
	if timeoutSec <= 0 {
		timeoutSec = 3
	}
	servers, err := dlna.DiscoverServers(a.ctx, time.Duration(timeoutSec)*time.Second)
	if err != nil {
		return nil, err
	}
	a.libraryMu.Lock()
	a.libraryServers = map[string]dlna.Server{}
	for _, s := range servers {
		a.libraryServers[s.UDN] = s
	}
	a.libraryMu.Unlock()

	out := make([]LibraryServer, 0, len(servers))
	for _, s := range servers {
		out = append(out, LibraryServer{
			UDN:          s.UDN,
			FriendlyName: s.FriendlyName,
			Manufacturer: s.Manufacturer,
			ModelName:    s.ModelName,
			IconURL:      s.IconURL,
			Address:      s.Address,
		})
	}
	return out, nil
}

// BrowseLibrary returns one page of children under objectID on the
// server identified by udn. objectID "0" or empty is the server root.
// Items that are not audio are filtered out so the Library tab only
// shows things the SoundTouch can actually play.
func (a *App) BrowseLibrary(udn, objectID string, start, count int) (LibraryPage, error) {
	a.libraryMu.Lock()
	srv, ok := a.libraryServers[udn]
	a.libraryMu.Unlock()
	if !ok {
		return LibraryPage{}, fmt.Errorf("unknown media server %q, call ListMediaServers first", udn)
	}
	ctx, cancel := context.WithTimeout(a.ctx, 12*time.Second)
	defer cancel()
	res, err := dlna.Browse(ctx, srv, objectID, start, count)
	if err != nil {
		return LibraryPage{}, err
	}
	page := LibraryPage{
		TotalMatches: res.TotalMatches,
		Returned:     res.Returned,
	}
	for _, c := range res.Containers {
		page.Containers = append(page.Containers, LibraryContainer{
			ID: c.ID, ParentID: c.ParentID, Title: c.Title,
			ChildCount: c.ChildCount,
		})
	}
	for _, it := range res.Items {
		if !it.IsAudioItem() {
			continue
		}
		page.Items = append(page.Items, LibraryItem{
			ID: it.ID, ParentID: it.ParentID, Title: it.Title,
			Artist: it.Artist, Album: it.Album, MimeType: it.MimeType,
			StreamURL: it.StreamURL, AlbumArtURL: it.AlbumArtURL,
			DurationSec: it.DurationSec,
		})
	}
	return page, nil
}
