// STR Desktop App: findet alle Sticks im LAN via mDNS, listet sie
// und steuert sie via REST API. Wails App, Backend ist Go, Frontend HTML/JS.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
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

	// userLocale is the active UI language (BCP-47, e.g. "de"/"en")
	// reported by the frontend via SetAppLocale. Server-side
	// provisioning paths that set the box display language (the
	// Setup-AP push) map it to a Bose sysLanguage so we never force a
	// hardcoded language on the user. Guarded because Wails dispatches
	// method calls from arbitrary goroutines.
	localeMu   sync.RWMutex
	userLocale string

	// discCache keeps recently-discovered boxes so a single missed mDNS
	// or TCP cycle does not make a box flicker out of the list and back.
	// mDNS multicast drops, a box mid-reboot, or marginal Wi-Fi (all
	// observed live on deqw's spotty ST20, #90) otherwise cause the box
	// to vanish and radio/presets to fail with "Failed to fetch" until
	// the next cycle re-finds it. See mergeDiscoveryCache.
	discMu    sync.Mutex
	discCache map[string]discEntry
}

// discEntry is one cached discovery result plus when it was last
// genuinely seen (not counting cache re-adds).
type discEntry struct {
	box  BoxInfo
	seen time.Time
}

// discoveryStickyTTL is how long a box stays in the list after its last
// genuine sighting. Long enough to cover a box rebooting (~60-120s on a
// slow BCO box) so it does not disappear mid-reboot, short enough that a
// truly powered-off box drops out reasonably soon.
const discoveryStickyTTL = 100 * time.Second

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
	// Route the dlna package's logs through our file logger so the
	// per-interface SSDP M-SEARCH summary lines land in str.log next
	// to the STR discovery cycles. Without this, a media server scan
	// that returns zero results is indistinguishable from "no servers
	// on the LAN" in the diagnostic bundle.
	dlna.Logger = a.logger.With("comp", "dlna")
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
	// PortVerified is true when Port was confirmed reachable by an
	// actual HTTP probe (probeSTR), false when it is only the
	// mDNS-announced port. On BCO boxes (Portable, ST20-spotty) the
	// agent announces :8888 via mDNS but the chipset firewall drops
	// direct :8888; only the REDIRECTed :17008 is reachable. The merge
	// in DiscoverBoxes prefers a verified port over an announced one so
	// agent calls (radio, presets) do not hit the firewalled :8888.
	PortVerified bool `json:"portVerified"`
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
		if !exists {
			seen[key] = b
			return
		}
		// STR announcement always wins over a stock entry for the same
		// physical device.
		if prev.Kind == "str" && b.Kind == "stock" {
			return
		}
		if b.Kind == "str" && prev.Kind == "stock" {
			seen[key] = b
			return
		}
		// Same kind: the richer record wins (longer FriendlyName,
		// non-empty Version) — BUT a VERIFIED reachable port always
		// beats an unverified (mDNS-announced) one, in either merge
		// order. On BCO boxes the agent announces :8888 via mDNS while
		// only the REDIRECTed :17008 is reachable; without this, the
		// rich mDNS record would pin the box to the firewalled :8888
		// and every agent call (radio, presets) would fail.
		keepPrev := len(prev.FriendlyName) >= len(b.FriendlyName) && prev.Version != ""
		if keepPrev {
			if b.PortVerified && !prev.PortVerified && b.Port != 0 && b.Port != prev.Port {
				prev.Port = b.Port
				prev.PortVerified = true
				seen[key] = prev
			}
			return
		}
		// b is the richer record: take it, but carry over a verified
		// port from prev if b's port is only mDNS-announced.
		if prev.PortVerified && !b.PortVerified && prev.Port != 0 {
			b.Port = prev.Port
			b.PortVerified = true
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

	// Discovery stickiness: re-add boxes seen within discoveryStickyTTL
	// that this cycle missed, so the list stays stable across mDNS/TCP
	// flaps instead of flickering (deqw #90: spotty ST20 dropped out of
	// the list on marginal Wi-Fi / mid-reboot and radio+presets failed
	// whenever it briefly vanished).
	a.mergeDiscoveryCache(seen)

	out := make([]BoxInfo, 0, len(seen))
	for _, b := range seen {
		out = append(out, b)
	}
	return out, nil
}

// mergeDiscoveryCache refreshes the cache for boxes genuinely seen this
// cycle, then re-adds any cached box this cycle missed but which was
// seen within discoveryStickyTTL (keeping its last-known record, NOT
// refreshing its timestamp, so it still expires relative to its last
// genuine sighting). Boxes past the TTL are evicted.
func (a *App) mergeDiscoveryCache(seen map[string]BoxInfo) {
	now := time.Now()
	a.discMu.Lock()
	defer a.discMu.Unlock()
	if a.discCache == nil {
		a.discCache = map[string]discEntry{}
	}
	// Boxes found this cycle are authoritative: refresh data + timestamp.
	for key, b := range seen {
		a.discCache[key] = discEntry{box: b, seen: now}
	}
	// Re-add recently-seen boxes the current cycle missed; evict stale.
	for key, e := range a.discCache {
		if _, ok := seen[key]; ok {
			continue
		}
		if now.Sub(e.seen) <= discoveryStickyTTL {
			seen[key] = e.box
		} else {
			delete(a.discCache, key)
		}
	}
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

// probeSTR checks both :8888 and :17008 on the host for the STR
// agent JSON envelope. Bose's BCO wifi chipset has a different
// whitelist on each model family:
//
//   - Series-II classic boxes (ST10/20/30 verified live 2026-05-28
//     on ST10 .66 build 1944): :8888 / :9080 / :8081 are reachable
//     externally without any hijack. STR's agent answers :8888
//     directly.
//   - Series-I taigan boxes (Portable verified): :8888 SYNs are
//     dropped at the chipset level. STR's agent uses an LD_PRELOAD
//     shim inside Bose's SoftwareUpdate process to make :17008
//     forward to localhost:8888. On these boxes :17008 is the only
//     externally-reachable port.
//
// Both ports are probed in parallel; whichever responds with the
// STR JSON wins. The BoxInfo.Port records the actual reachable port
// so subsequent API calls hit the right entry point.
//
// On hit we also pull /info from :8090 on the same box — the Bose
// firmware keeps answering that endpoint even after STR is installed,
// and without it the box list shows "str-192.168.x.x" with no
// FriendlyName/DeviceID/Model, which the frontend renders as if the
// box were unprovisioned.
func probeSTR(ctx context.Context, ip string) (BoxInfo, bool) {
	type result struct {
		port int
		body []byte
	}
	hits := make(chan result, 2)
	for _, port := range []int{8888, 17008} {
		p := port
		go func() {
			url := fmt.Sprintf("http://%s:%d/api/agent/version", ip, p)
			body, ok := httpGetSmall(ctx, url, 1200*time.Millisecond, 1024)
			if !ok || !strings.Contains(string(body), `"version"`) {
				hits <- result{}
				return
			}
			hits <- result{port: p, body: body}
		}()
	}
	var winner result
	for i := 0; i < 2; i++ {
		r := <-hits
		if r.port != 0 && winner.port == 0 {
			winner = r
		}
	}
	if winner.port == 0 {
		return BoxInfo{}, false
	}
	s := string(winner.body)
	version := jsonStringField(s, "version")
	build := jsonStringField(s, "build")

	box := BoxInfo{
		Name:         "str-" + ip,
		Host:         ip,
		Port:         winner.port,
		Version:      version,
		Build:        build,
		Kind:         "str",
		PortVerified: true, // winner.port answered an actual HTTP probe
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
	// Short per-call timeout. boxPut is used for the small settings
	// PUTs (volume, bass, name, source, wlan). Sub-3s box-side
	// responses are normal; anything slower indicates the box's
	// HTTP server is hung (Series-I :17008 SoftwareUpdate when the
	// shim is not active, stock Bose firmware on a not-yet-flashed
	// speaker, etc.). The default 6 s httpClient cap then lets a
	// rapid volume drag pile up requests and the UI throws a wall
	// of timeout errors. Cap at 3 s so a single dead PUT does not
	// hold the throttle queue for half the drag.
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Second)
	defer cancel()
	url := a.baseURL(host, port) + path
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, url, strings.NewReader(string(b)))
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

// --- Box-native (:8090) controls that STR does not proxy ---------------
//
// Clock display and language live on the box's OWN Bose HTTP API (:8090),
// not STR's REST API. They must be driven server-side from here: the
// box's :8090 sends no CORS headers, so the previous frontend
// fetch(boseUrl('/clockDisplay'|'/language')) with a text/xml POST
// triggered a CORS preflight the box never answered and failed with
// "TypeError: Failed to fetch". :8090 is a Bose-owned port and stays
// externally reachable even on Series-I/BCO boxes where STR's :8888 is
// firewalled (verified live 2026-06-01), so a direct server-side call
// works on every model. (WLAN + presets etc. already go through STR's
// CORS-enabled :8888/:17008 API, so only these two needed moving.)
func (a *App) boseURL(host string) string { return fmt.Sprintf("http://%s:8090", host) }

func (a *App) boseGet(host, path string) (string, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.boseURL(host)+path, nil)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return string(b), nil
}

func (a *App) bosePostXML(host, path, body string) error {
	ctx, cancel := context.WithTimeout(a.ctx, 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, a.boseURL(host)+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "text/xml")
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

// xmlTagOrAttr pulls the text content of <tag ...>VALUE</tag>, or if the
// element is self-closing, the value of one of the given attributes
// (enable/enabled/value). Cheap substring scan, no encoding/xml. Returns
// "" when nothing matches (caller shows "unknown").
func xmlTagOrAttr(xml, tag string, attrs ...string) string {
	open := "<" + tag
	i := strings.Index(xml, open)
	if i < 0 {
		return ""
	}
	gt := strings.IndexByte(xml[i:], '>')
	if gt < 0 {
		return ""
	}
	head := xml[i : i+gt+1]
	// Element with text content: <tag ...>VALUE</tag>
	if !strings.HasSuffix(strings.TrimSpace(head), "/>") {
		rest := xml[i+gt+1:]
		if end := strings.Index(rest, "</"+tag+">"); end >= 0 {
			if v := strings.TrimSpace(rest[:end]); v != "" {
				return v
			}
		}
	}
	// Self-closing / attribute form: <tag enable="VALUE"/>
	for _, at := range attrs {
		key := at + "=\""
		if j := strings.Index(head, key); j >= 0 {
			r := head[j+len(key):]
			if k := strings.IndexByte(r, '"'); k >= 0 {
				return r[:k]
			}
		}
	}
	return ""
}

// GetClockDisplay reads the box clock-display state (BETA, undocumented,
// not on every model). Live-verified schema 2026-06-01 (taigan):
// GET /clockDisplay -> <clockDisplay><clockConfig userEnable="false"
// timeFormat="..." .../></clockDisplay>. The on/off state is the
// userEnable attribute of the inner <clockConfig>. Returns "true"/
// "false" or "" if absent / endpoint unsupported.
func (a *App) GetClockDisplay(host string) (string, error) {
	body, err := a.boseGet(host, "/clockDisplay")
	if err != nil {
		return "", err
	}
	return xmlTagOrAttr(body, "clockConfig", "userEnable"), nil
}

// SetClockDisplay toggles the box clock display and sets the local-time
// offset + 12/24h format. The box rejects a bare <clockConfig .../>
// (HTTP 400 CLIENT_XML_ERROR); it requires the full
// <clockDisplay><clockConfig .../></clockDisplay> wrapper (live-verified
// 2026-06-01). The box keeps its UTC time from NTP but shows it raw
// (timezoneInfo stays NOT_SET); userOffsetMinute is the minutes EAST of
// UTC to add, so passing the desktop's current offset makes the speaker
// display local time. timeFormat picks 12h vs 24h. offsetMinutes is
// ignored by the box when userEnable is false but we always send a
// consistent config.
func (a *App) SetClockDisplay(host string, enable bool, timezone string, offsetMinutes int, format24 bool) error {
	tf := "TIME_FORMAT_12HOUR_ID"
	if format24 {
		tf = "TIME_FORMAT_24HOUR_ID"
	}
	// timezoneInfo is the real IANA zone (e.g. "Europe/Berlin"), the same
	// thing the Bose iOS app sets (live-verified 2026-06-01); with it the
	// speaker handles DST itself from its own tz database. We also send
	// the current userOffsetMinute as a correct-now fallback. timezone ""
	// leaves it unset.
	tz := timezone
	off := offsetMinutes
	if tz == "" {
		tz = "NOT_SET" // no zone: fall back to the raw offset shift
	} else {
		// With a real IANA zone the box derives the offset (incl DST)
		// itself. Sending userOffsetMinute on TOP would DOUBLE-shift the
		// clock: live 2026-06-01, timezoneInfo=Europe/Berlin (+2) plus
		// userOffsetMinute=120 (+2) showed 06:00 instead of 04:00. So
		// whenever a zone is set, the offset must be 0.
		off = 0
	}
	body := fmt.Sprintf(
		`<clockDisplay><clockConfig userEnable="%t" timezoneInfo="%s" userOffsetMinute="%d" timeFormat="%s" /></clockDisplay>`,
		enable, tz, off, tf)
	return a.bosePostXML(host, "/clockDisplay", body)
}

// GetClockFormat24 reports whether the box clock is currently in 24h
// mode, so the UI can preselect the right radio. "" GET -> false (12h
// default). Separate tiny method to avoid changing GetClockDisplay's
// return shape (its string drives the on/off label).
func (a *App) GetClockFormat24(host string) (bool, error) {
	body, err := a.boseGet(host, "/clockDisplay")
	if err != nil {
		return false, err
	}
	return strings.Contains(xmlTagOrAttr(body, "clockConfig", "timeFormat"), "24HOUR"), nil
}

// GetBoxLanguage reads the box sysLanguage integer (as a string), or "".
func (a *App) GetBoxLanguage(host string) (string, error) {
	body, err := a.boseGet(host, "/language")
	if err != nil {
		return "", err
	}
	return xmlTagOrAttr(body, "sysLanguage"), nil
}

// SetBoxLanguage sets the box sysLanguage integer (see project_bose_language_enum).
func (a *App) SetBoxLanguage(host string, value int) error {
	return a.bosePostXML(host, "/language", fmt.Sprintf(`<sysLanguage>%d</sysLanguage>`, value))
}

// GetAirplayOpt reads the BCO "AirPlay optimization" toggle from the STR
// agent on host:port. Returns {"supported":bool,"enabled":bool}. Only
// BCO speakers (Portable, ST20-spotty) support it; others report
// supported=false. See internal/webui handleBoxAirplayOpt.
func (a *App) GetAirplayOpt(host string, port int) (map[string]bool, error) {
	url := a.baseURL(host, port) + "/api/box/airplay-opt"
	resp, err := a.httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var out map[string]bool
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetAirplayOpt flips the AirPlay-optimization toggle. The agent
// rewrites BCOResetTimerEnabled and reboots the speaker to apply it
// (BoseApp reads the value at boot, like the iOS app), so the box drops
// off the LAN for ~60-120s after this returns.
func (a *App) SetAirplayOpt(host string, port int, enabled bool) error {
	url := a.baseURL(host, port) + "/api/box/airplay-opt"
	body, _ := json.Marshal(map[string]bool{"enabled": enabled})
	req, err := http.NewRequestWithContext(a.ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
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

// WriteLangConfig schreibt lang.conf auf den Stick. locale + country
// sind die Wizard-Signale, sysLanguage der vom User im Sprach-Dropdown
// gewaehlte Bose-Wert. Die Box's run.sh liest den Integer beim ersten
// Boot als OOB-Gate-Sprache UND Display-Sprache, statt weltweit Deutsch
// zu erzwingen. Siehe project_bose_language_enum.
func (a *App) WriteLangConfig(targetPath, locale, country string, sysLanguage int) error {
	return sticksetup.WriteLangConfig(targetPath, locale, country, sysLanguage)
}

// SuggestBoxLanguage liefert die Bose-sysLanguage, die der Setup-Wizard
// im Sprach-Dropdown vorbelegen soll: primaer aus dem gewaehlten Land
// abgeleitet, mit der aktiven App-Sprache als bewusster Override, sonst
// Englisch. Das Frontend ruft es beim Laden und bei jeder Laenderaenderung.
func (a *App) SuggestBoxLanguage(locale, country string) int {
	return sticksetup.SuggestBoxLanguage(locale, country)
}

// SetAppLocale merkt sich die im UI aktive Sprache des Users (BCP-47,
// z.B. "de"/"en"). Das Frontend ruft das beim Start und bei jedem
// Sprachwechsel auf. Server-seitige Provisioning-Pfade (Setup-AP Push)
// leiten daraus die Box-Display-Sprache ab.
func (a *App) SetAppLocale(locale string) {
	a.localeMu.Lock()
	a.userLocale = strings.TrimSpace(locale)
	a.localeMu.Unlock()
}

// appLocale liefert das zuletzt gemeldete UI-Locale (leer wenn noch
// keins gesetzt wurde).
func (a *App) appLocale() string {
	a.localeMu.RLock()
	defer a.localeMu.RUnlock()
	return a.userLocale
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
		// Update endpoint on the website (separate repo). CheckAppUpdate
		// appends the running client's context (?v=&b=&os=&arch=&lang=) so
		// the server can pick the right OS download and localized notes,
		// then returns a small JSON manifest. See CheckAppUpdate for the
		// request/response contract.
		UpdateManifestURL: "https://st-reborn.de/api/update-check.php",
	}
}

// versionLess reports whether dotted numeric version a is strictly less
// than b. Both may carry a leading "v" and a git-describe suffix
// ("-3-gabc123-dirty"); only the leading numeric segments are compared,
// so a dev build off tag v0.6.5 compares equal to the v0.6.5 release.
func versionLess(a, b string) bool {
	pa, pb := parseVersionParts(a), parseVersionParts(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return x < y
		}
	}
	return false
}

func parseVersionParts(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	var parts []int
	for _, seg := range strings.Split(v, ".") {
		n, ok := 0, false
		for _, r := range seg { // stop at the first non-numeric rune (git suffix)
			if r < '0' || r > '9' {
				break
			}
			n, ok = n*10+int(r-'0'), true
		}
		if !ok {
			break
		}
		parts = append(parts, n)
	}
	return parts
}

// CheckAppUpdate fetches the UpdateManifestURL and returns the manifest
// when the remote version is strictly newer than the running one.
//
// Request: GET UpdateManifestURL with the running client's context as
// query parameters (all non-identifying, no device/network data):
//
//	v     running app version    e.g. v0.6.5
//	b     build stamp            e.g. 2026-06-01-1150
//	os    runtime GOOS           windows | darwin | linux
//	arch  runtime GOARCH         amd64 | arm64
//	lang  active UI locale       e.g. de | en | uk (omitted if unset)
//
// Response: a small JSON object with string fields. version is required;
// downloadUrl and notes are optional. The server may either always return
// the latest release (the client filters with versionLess below) or only
// respond with a body when v is older. Example:
//
//	{"version":"v0.6.6","build":"...","downloadUrl":"https://st-reborn.de/download/windows","notes":"..."}
func (a *App) CheckAppUpdate() (result map[string]string, err error) {
	// The update check is best-effort and must never take the app down.
	// Any unforeseen panic (a malformed response that trips a code path,
	// a nil deref, etc.) is recovered here and reported as a plain error,
	// so an unreachable or garbage endpoint can only ever mean "no banner".
	defer func() {
		if r := recover(); r != nil {
			if a.logger != nil {
				a.logger.Warn("CheckAppUpdate recovered from panic", "panic", r)
			}
			result, err = nil, fmt.Errorf("update check failed")
		}
	}()
	info := a.AppInfo()
	manifestURL := info.UpdateManifestURL
	// Dev/staging override: point the update check at a different
	// manifest (a local mock or the staging endpoint) without
	// rebuilding the baked-in production URL. Empty/unset uses the
	// shipped URL, so this is inert in normal operation.
	if override := strings.TrimSpace(os.Getenv("STR_UPDATE_MANIFEST_URL")); override != "" {
		manifestURL = override
	}
	if manifestURL == "" {
		return map[string]string{}, nil
	}
	reqURL := manifestURL
	if u, perr := url.Parse(reqURL); perr == nil {
		q := u.Query()
		q.Set("v", info.Version)
		q.Set("b", info.Build)
		q.Set("os", runtime.GOOS)
		q.Set("arch", runtime.GOARCH)
		if loc := a.appLocale(); loc != "" {
			q.Set("lang", loc)
		}
		u.RawQuery = q.Encode()
		reqURL = u.String()
	}
	ctx, cancel := context.WithTimeout(a.ctx, 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	// Stable, identifiable agent string so the server can filter bots and
	// keep meaningful update-check stats.
	req.Header.Set("User-Agent", "STReborn-Desktop/"+info.Version+" ("+runtime.GOOS+"; "+runtime.GOARCH+")")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest status %d", resp.StatusCode)
	}
	var m map[string]string
	// Read cap is generous on purpose: the server caps notes at 1500
	// *characters*, which in heavy multi-byte text (emoji/CJK) can be
	// several KB. 4 KB risked truncating the JSON mid-notes and failing
	// the decode (no banner); 16 KB leaves comfortable headroom.
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16384)).Decode(&m); err != nil {
		return nil, err
	}
	rv := m["version"]
	// Only surface the banner when the remote version is strictly newer
	// than the running one; equal or older (e.g. a dev build ahead of the
	// published tag) stays silent.
	if rv == "" || !versionLess(info.Version, rv) {
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

// UpdateBoxAgent ships the embedded ARM binary to the speaker. Preferred
// path is HTTP POST to /api/agent/update on host:port — but only when a
// preflight confirms STR's agent really answers there. On Series-I boxes
// (scm/spotty, taigan) where the LD_PRELOAD shim has not hijacked
// SoftwareUpdate's :17008 listener, that port belongs to Bose's own
// SoftwareUpdate HTTP service. Its work buffer is 1.5 KB, and the 10 MB
// agent binary returns "Request Too Large" (verified live 2026-05-30 on
// a scm/spotty ST20 diagnostic bundle — see [[bose-http-buffer]]).
//
// On preflight failure we fall back to SSH: stream the binary via stdin
// into /mnt/nv/streborn/bin/streborn-armv7l.new, size-verify, atomic-
// rename, SIGTERM the running agent. run.sh's boot watchdog respawns it
// from the new file within seconds.
func (a *App) UpdateBoxAgent(host string, port int) error {
	bin := agentbin.Bytes()
	if len(bin) == 0 {
		return fmt.Errorf("no embedded stick binary available")
	}
	if perr := a.updateAgentPreflight(host, port); perr != nil {
		a.logger.Warn("update agent: HTTP preflight rejected, switching to SSH-OTA",
			"host", host, "port", port, "reason", perr)
		if sshErr := a.updateAgentViaSSH(host, bin); sshErr != nil {
			return fmt.Errorf("HTTP preflight rejected the listener at :%d and SSH fallback also failed: %w (preflight: %v)", port, sshErr, perr)
		}
		a.logger.Info("update agent: SSH-OTA succeeded", "host", host, "bytes", len(bin))
		return nil
	}
	return a.updateAgentViaHTTP(host, port, bin)
}

// updateAgentPreflight checks that /api/agent/version on host:port really
// answers as STR (JSON envelope containing a "version" key). A success
// here is the green light for the 10 MB HTTP POST. Any other response
// shape (HTML error, plain text, missing field, non-200) means the
// listener is something else — almost certainly Bose's SoftwareUpdate
// service on a Series-I box without an active shim, where the 1.5 KB
// POST buffer guarantees failure.
func (a *App) updateAgentPreflight(host string, port int) error {
	url := a.baseURL(host, port) + "/api/agent/version"
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	snip := string(body)
	if len(snip) > 200 {
		snip = snip[:200] + "..."
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d at %s — body=%q", resp.StatusCode, url, snip)
	}
	var probe map[string]any
	if jerr := json.Unmarshal(body, &probe); jerr != nil || probe["version"] == nil {
		return fmt.Errorf("listener at :%d is not STR (ct=%q body=%q) — likely Bose SoftwareUpdate, agent OTA via HTTP would hit the 1.5 KB POST buffer",
			port, resp.Header.Get("Content-Type"), snip)
	}
	return nil
}

func (a *App) updateAgentViaHTTP(host string, port int, bin []byte) error {
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

// updateAgentViaSSH streams bin into /mnt/nv/streborn/bin/streborn-armv7l
// over SSH, then SIGTERMs the running agent so run.sh's watchdog respawns
// it from the new file. Each step's failure is reported with concrete
// context so the desktop's error toast tells the user what to look at
// instead of "ssh: exit 1".
func (a *App) updateAgentViaSSH(host string, bin []byte) error {
	if hello, err := boxSSHOutput(host, "echo STR_SSH_OK", 8*time.Second); err != nil || !strings.Contains(hello, "STR_SSH_OK") {
		return fmt.Errorf("ssh handshake failed: %v (%s)", err, strings.TrimSpace(hello))
	}
	// mkdir + cat in one ssh-with-stdin session so a missing parent dir on
	// a freshly-installed box does not need a separate round-trip. The
	// 120 s timeout covers a 10 MB upload over slow Wi-Fi with the
	// speaker's modest CPU spending time on SSH crypto.
	uploadCmd := "mkdir -p /mnt/nv/streborn/bin && cat > /mnt/nv/streborn/bin/streborn-armv7l.new"
	if out, err := boxSSHUploadStdin(host, uploadCmd, bytes.NewReader(bin), 120*time.Second); err != nil {
		return fmt.Errorf("ssh upload (%d bytes) failed: %v (%s)", len(bin), err, strings.TrimSpace(out))
	}
	// Size-verify before the atomic rename so a half-uploaded file never
	// becomes the live agent. Sentinel "OK_<size>" so the caller can
	// distinguish a successful rename from any stderr noise.
	verifyCmd := fmt.Sprintf(
		"size=$(wc -c < /mnt/nv/streborn/bin/streborn-armv7l.new) && "+
			"[ \"$size\" = \"%d\" ] && "+
			"chmod 0755 /mnt/nv/streborn/bin/streborn-armv7l.new && "+
			"mv /mnt/nv/streborn/bin/streborn-armv7l.new /mnt/nv/streborn/bin/streborn-armv7l && "+
			"echo OK_%d", len(bin), len(bin))
	sentinel := fmt.Sprintf("OK_%d", len(bin))
	if out, err := boxSSHOutput(host, verifyCmd, 15*time.Second); err != nil || !strings.Contains(out, sentinel) {
		return fmt.Errorf("ssh size-verify or rename failed: %v (%s)", err, strings.TrimSpace(out))
	}
	// Always reboot after the binary swap. Jens 2026-06-01: an OTA that
	// only SIGTERMs the agent and relies on run.sh's watchdog respawn
	// leaves a dirty post-update state. The new binary came up but the
	// app showed no presets, because the boot-time preset push and the
	// leave-OOB full re-sync (cmd/agent reconcileOnce forceFull) only run
	// on a real boot, not on a live process restart; and OTA replaces
	// only the binary, so the NAND run.sh + rc.local otherwise stay at
	// the pre-OTA vintage (project_ota_only_replaces_binary). A clean
	// reboot fixes both: the new binary self-deploys its matching
	// run.sh/rc.local on boot AND the preset reconcile runs from clean.
	// Detached so the SSH session returns before the box drops off the
	// LAN. sync first so the just-renamed binary is flushed to NAND.
	rebootAfterOTA := "(sleep 1; sync; /sbin/reboot) </dev/null >/dev/null 2>&1 &"
	_ = boxSSHFireAndForget(host, rebootAfterOTA, 5*time.Second)
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
