// STR Desktop App: finds all sticks on the LAN via mDNS, lists them
// and controls them via REST API. Wails app, backend is Go, frontend HTML/JS.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/JRpersonal/streborn/discovery"
	"github.com/JRpersonal/streborn/dlna"
	"github.com/JRpersonal/streborn/sticksetup"
	"github.com/JRpersonal/streborn/wifiprofiles"
	qrcode "github.com/skip2/go-qrcode"
	"streborn-app/agentbin"
)

// App is the central state struct.
type App struct {
	ctx        context.Context
	logger     *slog.Logger
	logFile    *os.File // kept so ExportDiagnosticLogs can Sync before reading
	httpClient *http.Client

	// portCache maps a box host to the agent port last seen answering it.
	// BCO boxes (Portable/taigan, ST20-spotty) expose the agent only on
	// the redirected :17008, classic boxes answer :8888 directly, and mDNS
	// announces :8888 either way, so a box record can carry the wrong
	// port. boxDo tries the cached/known port, falls back to the other on
	// any transport failure, and caches whichever connects. This is
	// self-healing: if the box froze and the app got pinned to a port that
	// no longer answers (observed: a freeze made :17008 time out, discovery
	// fell back to the announced :8888 and never retried :17008), the next
	// call simply fails over and re-pins to the working port.
	portMu    sync.Mutex
	portCache map[string]int

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
	// observed live on a spotty ST20, #90) otherwise cause the box
	// to vanish and radio/presets to fail with "Failed to fetch" until
	// the next cycle re-finds it. See mergeDiscoveryCache.
	discMu    sync.Mutex
	discCache map[string]discEntry

	// otaPinned maps a speaker IP to the time STR last initiated an agent OTA
	// on it. During the post-OTA reboot the agent is down while the box's stock
	// Bose port still answers, so discovery would briefly reclassify the box as
	// stock and offer a USB reinstall (#108). Because STR itself triggered the
	// update, it KNOWS that IP runs STR: while the pin is fresh, discovery forces
	// the box to stay classified as STR regardless of what the half-booted box
	// reports. Guarded by discMu (same lock as discCache, always held together).
	otaPinned map[string]time.Time

	// logoCache memoises resolved station-logo URLs (ResolveStationLogo)
	// so the same station is validated against DuckDuckGo at most once per
	// app run. Value "" means "no real logo, draw a monogram".
	logoMu    sync.Mutex
	logoCache map[string]string
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

// discoverySTRStickyTTL is the longer eviction grace for a box already known to
// run STR. A post-OTA reboot can take longer than discoveryStickyTTL while the
// agent restarts; without this the STR cache entry is evicted mid-reboot and a
// transient stock sighting relabels the box as "needs install" until a manual
// Refresh (#108). A removed STR box lingers at most this long, an acceptable
// trade for not flickering to a wrong reinstall offer.
const discoverySTRStickyTTL = 6 * time.Minute

// otaRebootGrace is how long after STR triggers an agent OTA the target IP is
// force-classified as STR. It must comfortably cover the box rebooting and the
// agent coming back up (so the stock Bose port answering first cannot relabel
// the box), while being short enough that a genuinely re-flashed-to-stock box
// would correct itself soon after. See otaPinned and mergeDiscoveryCache (#108).
const otaRebootGrace = 4 * time.Minute

// NewApp creates a new App instance.
func NewApp() *App {
	logger, logFile := newFileLogger(slog.LevelInfo)
	return &App{
		logger:         logger,
		logFile:        logFile,
		httpClient:     &http.Client{Timeout: 6 * time.Second},
		libraryServers: map[string]dlna.Server{},
	}
}

// appCtx returns the Wails runtime context, or context.Background() before
// startup has set it. A bound App method can run before startup (Wails dispatches
// from arbitrary goroutines), and context.WithTimeout panics on a nil parent, so
// every timeout/request that parents on a.ctx must go through here.
func (a *App) appCtx() context.Context {
	if a.ctx == nil {
		return context.Background()
	}
	return a.ctx
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	// Clear any leftover "<exe>.old" from a previous self-update (#71).
	a.cleanupOldBinary()
	// Route the dlna package's logs through our file logger so the
	// per-interface SSDP M-SEARCH summary lines land in str.log next
	// to the STR discovery cycles. Without this, a media server scan
	// that returns zero results is indistinguishable from "no servers
	// on the LAN" in the diagnostic bundle.
	dlna.Logger = a.logger.With("comp", "dlna")
	// Same for sticksetup, so USB-stick discovery timing (a slow search
	// while Windows finishes mounting a freshly inserted stick) is visible
	// in the diagnostic bundle instead of an unexplained UI hang.
	sticksetup.Logger = a.logger.With("comp", "sticksetup")
	// Verbose startup line so users always see SOMETHING in the
	// log when they hit "Save diagnostic logs", even on a session
	// where they did not poke any features that emit further logs.
	a.logger.Info("Desktop App started",
		"version", appVersion,
		"build", appBuild,
		"logFile", LogFilePath(),
		"agentbinAvailable", agentbin.Available())
}

// LogClientError records an error the frontend caught (a global
// window onerror or an unhandledrejection) into str.log. Frontend
// JavaScript crashes do not otherwise reach the file logger, so
// without this a startup "flashes up and quits" leaves no trace to
// diagnose. Best-effort, never throws back into JS.
func (a *App) LogClientError(msg string) {
	if a.logger != nil {
		a.logger.Error("frontend error", "detail", msg)
	}
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

// DiscoverBoxes scans the LAN for sticks via mDNS. When mDNS
// finds nothing (e.g. Windows Firewall blocks 5353, or the stock
// firmware announces under a service name we do not know yet),
// a one-time lightweight HTTP probe sweep on port 8090 runs as a
// fallback. The fallback does NOT run on every discovery and only
// on a single port, so that a successful mDNS run does not trigger
// a port scan on the local network.
func (a *App) DiscoverBoxes(timeoutSec int) ([]BoxInfo, error) {
	if timeoutSec <= 0 {
		timeoutSec = 6
	}
	ctx, cancel := context.WithTimeout(a.appCtx(), time.Duration(timeoutSec)*time.Second)
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
		// Same kind: combine the two records field by field instead of
		// picking one whole winner. The two sources disagree in opposite
		// directions right after an OTA: the mDNS TXT carries the real
		// FriendlyName but a stale version (the re-announce lags the
		// restart), while the live :8888 probe carries the fresh version
		// and a verified port. Picking one whole record lost either the
		// name (box shows "str-<ip>") or the new version (update not
		// flagged) — the two halves of #108. mergeSameKind keeps the
		// best of each, including the verified-port rule the BCO boxes
		// need (agent announces :8888 via mDNS but only the REDIRECTed
		// :17008 is reachable).
		seen[key] = mergeSameKind(prev, b)
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
			FriendlyName: toValidUTF8(inst.FriendlyName),
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
	fallbackCtx, fallbackCancel := context.WithTimeout(a.appCtx(), 12*time.Second)
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
	// flaps instead of flickering (#90: spotty ST20 dropped out of
	// the list on marginal Wi-Fi / mid-reboot and radio+presets failed
	// whenever it briefly vanished).
	a.mergeDiscoveryCache(seen)

	out := make([]BoxInfo, 0, len(seen))
	for _, b := range seen {
		out = append(out, b)
	}
	// Stable order so speakers keep their place in the app across refreshes
	// instead of jumping around (seen is a map, whose iteration order is
	// random). Sort by display name, then host as a tiebreaker for two boxes
	// with the same (or empty) name. #108: the list reordering on every
	// discovery cycle was disorienting with several speakers.
	sort.Slice(out, func(i, j int) bool {
		ni, nj := boxSortName(out[i]), boxSortName(out[j])
		if ni != nj {
			return ni < nj
		}
		return out[i].Host < out[j].Host
	})
	return out, nil
}

// boxSortName is the case-insensitive key a box is ordered by in the speaker
// list: its display name, falling back to the mDNS name then the host so a
// box with no friendly name still sorts deterministically.
func boxSortName(b BoxInfo) string {
	n := b.FriendlyName
	if n == "" {
		n = b.Name
	}
	if n == "" {
		n = b.Host
	}
	return strings.ToLower(n)
}

// notePostOTA records that STR just triggered an agent OTA on host, so the
// post-OTA reboot window does not let the box's still-answering stock Bose port
// reclassify it as stock / "needs install" (#108).
func (a *App) notePostOTA(host string) {
	if host == "" {
		return
	}
	a.discMu.Lock()
	if a.otaPinned == nil {
		a.otaPinned = map[string]time.Time{}
	}
	a.otaPinned[host] = time.Now()
	a.discMu.Unlock()
	a.logger.Info("post-OTA: pinning box as STR through its reboot", "host", host, "grace", otaRebootGrace.String())
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
	// Boxes found this cycle refresh the timestamp, but their record is
	// MERGED with the cached one rather than blindly overwriting it: a
	// thinner cycle (only the stock mDNS entry because probeSTR missed the
	// agent, or no FriendlyName/Version because :8090 was slow) must not
	// downgrade what the user already sees. Otherwise a flashed speaker
	// flickers to "Bereit für STR" or to the generic "Bose SoundTouch
	// <id>" name between good cycles.
	for key, b := range seen {
		if prev, ok := a.discCache[key]; ok {
			b = mergeBoxInfo(prev.box, b)
			seen[key] = b
		}
		a.discCache[key] = discEntry{box: b, seen: now}
	}
	// Re-add recently-seen boxes the current cycle missed; evict stale.
	for key, e := range a.discCache {
		if _, ok := seen[key]; ok {
			continue
		}
		ttl := discoveryStickyTTL
		if e.box.Kind == "str" {
			// A known STR box gets a longer grace so a post-OTA reboot does not
			// evict it and let a transient stock sighting relabel it as "needs
			// install" (#108).
			ttl = discoverySTRStickyTTL
		}
		if now.Sub(e.seen) <= ttl {
			seen[key] = e.box
		} else {
			delete(a.discCache, key)
		}
	}
	// Post-OTA pin: any IP STR is mid-update on stays classified as STR through
	// its reboot, regardless of what the half-booted box reports (its stock Bose
	// port answers before the agent does, #108). STR triggered the update, so it
	// knows that IP runs STR. Expired pins are dropped.
	for host, t := range a.otaPinned {
		if now.Sub(t) > otaRebootGrace {
			delete(a.otaPinned, host)
			continue
		}
		b, ok := seen[host]
		if !ok {
			// Not visible this cycle (mid-reboot): keep the last-known record, or
			// synthesise a minimal STR one so the box neither vanishes nor gets
			// offered for reinstall.
			if e, cached := a.discCache[host]; cached {
				b = e.box
			} else {
				b = BoxInfo{Host: host, Port: 8888}
			}
		}
		b.Kind = "str"
		// The box is coming up on the app's embedded agent, so report that
		// version to stop a spurious "update available" flag from looping while
		// the agent is still restarting and cannot answer its real version.
		b.Version = appVersion
		b.Build = appBuild
		seen[host] = b
		a.discCache[host] = discEntry{box: b, seen: now}
	}
}

// RefreshKnownBoxes re-probes only the speakers already in the discovery cache,
// directly by their last-known IP, with NO mDNS browse and NO full /24 sweep.
// The desktop refresh calls this FIRST so the boxes you already have update
// their live values (reachable, version, name) within a second, then kicks off
// the full DiscoverBoxes in the background to pick up new or moved speakers.
// This is the common case ("I just want the current values of my known box")
// and avoids making the user wait out the ~3 s mDNS + ~12 s LAN sweep for it.
func (a *App) RefreshKnownBoxes() ([]BoxInfo, error) {
	a.discMu.Lock()
	known := make([]BoxInfo, 0, len(a.discCache))
	for _, e := range a.discCache {
		known = append(known, e.box)
	}
	a.discMu.Unlock()
	if len(known) == 0 {
		return []BoxInfo{}, nil
	}
	ctx, cancel := context.WithTimeout(a.appCtx(), 6*time.Second)
	defer cancel()
	seen := map[string]BoxInfo{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, kb := range known {
		kb := kb
		if kb.Host == "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Fall back to the cached record if the direct probe misses; the
			// sticky cache merge below keeps it from being downgraded/evicted.
			b := kb
			if probed, ok := probeSTR(ctx, kb.Host); ok {
				b = probed
			}
			b = a.enrichBoxWithStockInfo(ctx, b)
			mu.Lock()
			seen[b.Host] = b
			mu.Unlock()
		}()
	}
	wg.Wait()
	a.mergeDiscoveryCache(seen)
	out := make([]BoxInfo, 0, len(seen))
	for _, b := range seen {
		out = append(out, b)
	}
	a.logger.Info("refresh known boxes done", "count", len(out))
	return out, nil
}

// mergeBoxInfo keeps the richer of two records for the same physical box
// so a thinner discovery cycle never downgrades the display. cur is this
// cycle, prev the cached record. This is a safety net; the real fix is to
// query reliably enough (see probe timeouts) that cur is rarely thin.
func mergeBoxInfo(prev, cur BoxInfo) BoxInfo {
	out := cur
	// An STR agent, once seen, outranks a stock-only sighting: a missed
	// probeSTR must not relabel a flashed speaker as "needs install".
	if prev.Kind == "str" && out.Kind != "str" {
		out.Kind = "str"
		if out.Version == "" {
			out.Version = prev.Version
		}
		if out.Build == "" {
			out.Build = prev.Build
		}
		if prev.PortVerified && !out.PortVerified && prev.Port != 0 {
			out.Port = prev.Port
			out.PortVerified = true
		}
	}
	if isGenericBoxName(out.FriendlyName) && !isGenericBoxName(prev.FriendlyName) {
		out.FriendlyName = prev.FriendlyName
	}
	if out.Version == "" {
		out.Version = prev.Version
	}
	if (out.Model == "" || out.Model == "SoundTouch") && prev.Model != "" && prev.Model != "SoundTouch" {
		out.Model = prev.Model
	}
	if out.DeviceID == "" {
		out.DeviceID = prev.DeviceID
	}
	if out.SerialNumber == "" {
		out.SerialNumber = prev.SerialNumber
	}
	if out.Build == "" {
		out.Build = prev.Build
	}
	if prev.PortVerified && !out.PortVerified && prev.Port != 0 {
		out.Port = prev.Port
		out.PortVerified = true
	}
	return out
}

// isGenericBoxName reports whether name is empty or Bose's factory
// default ("Bose SoundTouch <id>"), i.e. a name a real user-assigned one
// should win over.
func isGenericBoxName(name string) bool {
	n := strings.TrimSpace(name)
	return n == "" || strings.HasPrefix(n, "Bose SoundTouch ")
}

// mergeSameKind combines two discovery records for the same physical box
// (same Host, same Kind) field by field. The mDNS and live-probe sources are
// each authoritative for different fields, so picking one whole record drops
// good data from the other (#108):
//
//   - Version/Build: a PortVerified record is a live HTTP probe of the running
//     agent, so its version is current; an mDNS-announced version can lag a
//     re-announce after an OTA restart. The verified value wins.
//   - FriendlyName / Model: a real (non-generic, non-empty) label beats a
//     generic or empty one, then the longer string wins.
//   - Port: a verified port beats an mDNS-announced one (BCO boxes announce
//     :8888 but only the REDIRECTed :17008 actually answers).
//
// Rules are applied per field, so it does not matter which argument is the
// mDNS record and which is the probe.
func mergeSameKind(a, b BoxInfo) BoxInfo {
	out := a
	out.FriendlyName = pickBoxName(a.FriendlyName, b.FriendlyName)
	out.Model = pickModelName(a.Model, b.Model)

	// Version/Build: the live-probed record is the source of truth.
	switch {
	case b.PortVerified && !a.PortVerified:
		if b.Version != "" {
			out.Version = b.Version
		}
		if b.Build != "" {
			out.Build = b.Build
		}
	case a.PortVerified && !b.PortVerified:
		// keep a's version/build
	default:
		if out.Version == "" {
			out.Version = b.Version
		}
		if out.Build == "" {
			out.Build = b.Build
		}
	}

	// Port: prefer a verified one.
	if b.PortVerified && !a.PortVerified && b.Port != 0 {
		out.Port = b.Port
		out.PortVerified = true
	}

	// DeviceID: prefer the value from the live :8090 /info probe (the
	// PortVerified record). That is the Bose SoundTouch deviceID (the SCM MAC),
	// which the firmware's zone protocol (/setZone, /addGroup) keys on. The mDNS
	// TXT instead carries the agent's wlan0 MAC, which on a two-chip chassis
	// (ST20 spotty/BCO, Portable) is the SMSC MAC, NOT the SoundTouch ID, so a
	// zone formed with it never forms (the master never recognizes itself, a
	// slave is never matched). Fall back to whichever side actually has a value.
	// Test against the ORIGINAL verified flags: the port-merge above may have
	// already flipped out.PortVerified to b's, which would otherwise make the
	// stale mDNS deviceID look verified.
	switch {
	case a.PortVerified && a.DeviceID != "":
		out.DeviceID = a.DeviceID
	case b.PortVerified && b.DeviceID != "":
		out.DeviceID = b.DeviceID
	case out.DeviceID == "":
		out.DeviceID = b.DeviceID
	}
	if out.SerialNumber == "" {
		out.SerialNumber = b.SerialNumber
	}
	if out.Name == "" {
		out.Name = b.Name
	}
	return out
}

// pickBoxName returns the better of two friendly names: a real one beats a
// generic or empty one, and between two real names the longer (richer) wins.
func pickBoxName(a, b string) string {
	ag, bg := isGenericBoxName(a), isGenericBoxName(b)
	if ag && !bg {
		return b
	}
	if bg && !ag {
		return a
	}
	if len(b) > len(a) {
		return b
	}
	return a
}

// pickModelName prefers a specific model string over the generic "SoundTouch"
// fallback (or empty) the agent announces before /info resolves the real type.
func pickModelName(a, b string) string {
	ag := a == "" || a == "SoundTouch"
	bg := b == "" || b == "SoundTouch"
	if ag && !bg {
		return b
	}
	return a
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

// AddBoxByIP probes one speaker IP the user typed in, bypassing mDNS and the
// /24 sweep entirely. It is the manual fallback for networks where discovery
// cannot reach the boxes at all: Wi-Fi AP/client isolation, the PC on a
// different subnet, a VPN/virtual adapter, or a security suite that blocks the
// sweep (a tester, 2026-06-28: both mDNS and the /24 TCP fallback returned 0 while
// the boxes were plainly on the LAN and visible in Windows Explorer). On a hit
// the box is cached like a discovered one, so it shows in the list and the
// periodic RefreshKnownBoxes keeps it live.
func (a *App) AddBoxByIP(host string) (BoxInfo, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return BoxInfo{}, fmt.Errorf("enter the speaker's IP address")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	a.logger.Info("AddBoxByIP: manual probe", "host", host)
	var box BoxInfo
	// An STR-flashed box still answers :8090, so probe :8888 (STR) first and
	// prefer that record; fall back to the stock Bose box so a vanilla speaker
	// is offered the USB-stick install.
	if str, isSTR := probeSTRWithRetry(ctx, host, 2); isSTR {
		box = str
	} else if stock, ok := probeStock(ctx, host); ok {
		box = stock
	} else {
		a.logger.Warn("AddBoxByIP: nothing answered", "host", host)
		return BoxInfo{}, fmt.Errorf("no SoundTouch answered at %s. Check the address, and that this PC and the speaker are on the same network", host)
	}
	if box.Host == "" {
		box.Host = host
	}
	now := time.Now()
	a.discMu.Lock()
	if a.discCache == nil {
		a.discCache = map[string]discEntry{}
	}
	a.discCache[box.Host] = discEntry{box: box, seen: now}
	a.discMu.Unlock()
	a.logger.Info("AddBoxByIP: added speaker", "host", box.Host, "kind", box.Kind, "name", box.Name, "version", box.Version)
	return box, nil
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

	var out []BoxInfo
	var collectWG sync.WaitGroup
	collectWG.Add(1)
	go func() {
		defer collectWG.Done()
		for h := range hits {
			out = append(out, h)
		}
	}()

	probeOne := func(ip string) {
		defer wg.Done()
		defer func() { <-sem }()
		b, ok := probeStock(ctx, ip)
		if !ok {
			return
		}
		// A host answering :8090 may well be an STR-flashed speaker, not a
		// stock box: STR leaves the Bose REST port alive. Classifying it
		// "stock" here is what tells the user to do a full USB-stick install,
		// so confirm STR is genuinely absent on this exact host before
		// emitting stock. When STR answers, emit the STR record (kind=str,
		// version) so the app offers an OTA update instead (#108). The whole
		// LAN STR sweep in probeLANForSTR can be cut short by the discovery
		// budget on a busy network; this per-host confirmation is the
		// reliable path because it runs only for the handful of hosts that
		// actually answer :8090.
		if str, isSTR := probeSTRWithRetry(ctx, ip, 2); isSTR {
			hits <- str
			return
		}
		hits <- b
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
	// Drain concurrently with spawning. The producers send to a buffered
	// channel while still holding a sem slot; if more than the buffer's worth of
	// hosts answer before any draining starts, they block on the send and wedge
	// the spawn loop (no free sem slot) until ctx fires. Collecting in a separate
	// goroutine that started before the loop removes that stall.
	wg.Wait()
	close(hits)
	collectWG.Wait()
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
		isPrivate := ip4[0] == 10 ||
			(ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31) ||
			(ip4[0] == 192 && ip4[1] == 168)
		if !isPrivate {
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

	var out []BoxInfo
	var collectWG sync.WaitGroup
	collectWG.Add(1)
	go func() {
		defer collectWG.Done()
		for h := range hits {
			out = append(out, h)
		}
	}()

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
	// Drain concurrently with spawning. The producers send to a buffered
	// channel while still holding a sem slot; if more than the buffer's worth of
	// hosts answer before any draining starts, they block on the send and wedge
	// the spawn loop (no free sem slot) until ctx fires. Collecting in a separate
	// goroutine that started before the loop removes that stall.
	wg.Wait()
	close(hits)
	collectWG.Wait()
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
			// 3 s, not 1.2 s: under sustained box load (BoseApp churning
			// CPU, loadavg 3-4) the agent's reply can take >1.2 s, and a
			// missed probe relabels a flashed speaker as "needs install".
			// The version endpoint is tiny, so a generous timeout only
			// costs latency on a genuinely dead host.
			body, ok := httpGetSmall(ctx, url, 3*time.Second, 1024)
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
		// The agent now carries the box display name/model in its version
		// envelope (#108). Seeding them here means a flashed speaker is
		// labelled straight from this one verified probe, even when the
		// :8090 /info enrichment below fails because the box is busy right
		// after an OTA restart. Without this the box showed as "str-<ip>".
		FriendlyName: jsonStringField(s, "friendlyName"),
		Model:        jsonStringField(s, "model"),
	}
	// Best-effort enrichment from the underlying Bose firmware's
	// /info endpoint. Failure is OK: caller still gets a usable
	// box, just less labelled. Only overwrite the agent-reported
	// fields when /info actually returns a value, so a slow/dead
	// :8090 cannot blank out the name we already have.
	if info, ok := probeStock(ctx, ip); ok {
		if info.FriendlyName != "" {
			box.FriendlyName = info.FriendlyName
		}
		if info.Model != "" {
			box.Model = info.Model
		}
		box.DeviceID = info.DeviceID
		box.SerialNumber = info.SerialNumber
	}
	return box, true
}

// probeSTRWithRetry probes a single host for the STR agent up to attempts
// times and returns the first success. Used to CONFIRM STR on a host that
// already answered the stock :8090 /info, where a single missed STR probe
// would wrongly classify an already-flashed speaker as stock and prompt a
// full USB-stick reinstall instead of an OTA update (#108: an ST10 .183,
// running v0.7.1, was sent to a complete stick install whenever the parallel
// STR sweep happened to miss it). STR speakers keep the Bose :8090 port alive,
// so a :8090 hit alone must never win over a present STR agent; a couple of
// targeted attempts make that check reliable even when the box is briefly busy.
func probeSTRWithRetry(ctx context.Context, ip string, attempts int) (BoxInfo, bool) {
	for i := 0; i < attempts; i++ {
		if b, ok := probeSTR(ctx, ip); ok {
			return b, true
		}
		if ctx.Err() != nil {
			break
		}
	}
	return BoxInfo{}, false
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
	// The Bose /info XML labels itself UTF-8 but reports an umlaut box name as a
	// lone Latin-1 byte ("ü" = 0xFC). Left raw it JSON-marshals to U+FFFD and
	// shows as garbled "K�che" in the speaker list / multiroom UI (#70, Albrecht).
	name := toValidUTF8(extractTag(s, "name"))
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

// toValidUTF8 returns s unchanged when it is already valid UTF-8, otherwise it
// reinterprets the bytes as Latin-1 (ISO-8859-1) and re-encodes them as UTF-8.
// The Bose /info XML labels itself UTF-8 but reports an umlaut box name as a
// lone Latin-1 byte ("ü" = 0xFC); left raw that JSON-marshals to U+FFFD and
// shows as garbled "K�che" (#70, Albrecht). Latin-1 maps 1:1 to the first 256
// code points, so ASCII is untouched and only the high bytes are widened.
func toValidUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		b.WriteRune(rune(s[i]))
	}
	return b.String()
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
	Bitrate   int    `json:"bitrate,omitempty"`
	URI       string `json:"uri,omitempty"`      // Spotify presets: playlist/album URI
	Account   string `json:"account,omitempty"`  // Spotify presets: owning account
	Source    string `json:"source,omitempty"`   // DLNA presets: media server name (cosmetic badge)
	Homepage  string `json:"homepage,omitempty"` // radio presets: station website (recent "website" link)
}

func (a *App) baseURL(host string, port int) string {
	// Default to the chipset-whitelisted hijack port. Classic frontend
	// callers that pre-discovery hard-coded 8888 still work because
	// they pass port=8888 explicitly; this fallback only kicks in for
	// freshly-resolved boxes where port was left zero.
	if port == 0 {
		port = 17008
	}
	if cp, ok := a.cachedPort(host); ok {
		port = cp
	}
	return fmt.Sprintf("http://%s:%d", host, port)
}

func (a *App) cachedPort(host string) (int, bool) {
	a.portMu.Lock()
	defer a.portMu.Unlock()
	p, ok := a.portCache[host]
	return p, ok
}

func (a *App) rememberPort(host string, port int) {
	a.portMu.Lock()
	defer a.portMu.Unlock()
	if a.portCache == nil {
		a.portCache = map[string]int{}
	}
	a.portCache[host] = port
}

func (a *App) forgetPort(host string) {
	a.portMu.Lock()
	defer a.portMu.Unlock()
	delete(a.portCache, host)
}

// altAgentPort returns the other agent port. The two are the STR agent's
// direct :8888 and the BCO chipset-whitelisted redirect :17008.
func altAgentPort(p int) int {
	if p == 8888 {
		return 17008
	}
	return 8888
}

// candidatePorts is the ordered, deduped list of agent ports to try for a
// host: the cached working port first (if any), then the caller's port,
// then the alternate. So the common case is one direct hit; a wrong/stale
// port costs one extra fast attempt and then self-corrects via the cache.
func (a *App) candidatePorts(host string, port int) []int {
	if port == 0 {
		port = 17008
	}
	order := make([]int, 0, 3)
	if cp, ok := a.cachedPort(host); ok {
		order = append(order, cp)
	}
	order = append(order, port, altAgentPort(port))
	seen := map[int]bool{}
	out := order[:0]
	for _, p := range order {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// boxDo performs an HTTP request against the agent with transparent port
// fallback. It tries each candidate port in turn; the first that connects
// is cached for the host and its response returned. A transport-level
// failure (connection refused, timeout, reset) drops the cached port and
// moves to the next candidate, so a box that changed which port it answers
// on (reboot, freeze, OTA) self-heals on the very next call. A non-
// transport error (a real HTTP response the caller must see) is returned
// immediately without flailing across ports. Caller closes resp.Body.
func (a *App) boxDo(host string, port int, method, path, contentType, body string) (*http.Response, error) {
	var lastErr error
	for _, p := range a.candidatePorts(host, port) {
		url := fmt.Sprintf("http://%s:%d%s", host, p, path)
		var rdr io.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		}
		req, err := http.NewRequestWithContext(a.appCtx(), method, url, rdr)
		if err != nil {
			return nil, err
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := a.httpClient.Do(req)
		if err == nil {
			a.rememberPort(host, p)
			return resp, nil
		}
		lastErr = err
		if !isTransportNotReady(err) {
			return nil, err
		}
		a.forgetPort(host)
	}
	return nil, lastErr
}

// ---- Multiroom zone (#70, BETA) ----

// ZoneMember is a speaker in a multiroom zone: its stable deviceID and LAN IP.
type ZoneMember struct {
	DeviceID string `json:"deviceID"`
	IP       string `json:"ip"`
}

// ZoneSpec is the form-a-zone request the desktop sends to the master's agent.
type ZoneSpec struct {
	Master ZoneMember   `json:"master"`
	Slaves []ZoneMember `json:"slaves"`
	Name   string       `json:"name"`
	Stereo bool         `json:"stereo"`
	// Mode is "native" (firmware sync) or "mirror" (each speaker pulls the same
	// stream). Empty defaults to native on the agent.
	Mode string `json:"mode"`
}

// GetZoneState reads the live multiroom zone the speaker reports
// (GET /api/box/zone) -> {master, senderIP, members[]}. Self-heals across
// :8888/:17008 like the other box calls.
func (a *App) GetZoneState(host string, port int) (map[string]any, error) {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/box/zone", "", "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// memberReadiness is the result of the pre-form readiness gate: which members
// answered their STR agent (reachable + /api/agent/version) within the budget,
// and which were still mid-restart and not safe to enroll. NotReady carries the
// LAN IPs so the UI can name the speaker that is still starting (#70: a member
// that had only been up ~57s after an OTA was enrolled into a zone it then never
// joined, leaving a silently-incomplete group).
type memberReadiness struct {
	Ready    []string
	NotReady []string
}

// strInSlice reports whether v is in s.
func strInSlice(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// ensureZoneMembersReady probes the master and every slave IP for a live STR
// agent before a zone is formed. It reuses probeSTRWithRetry, the same probe
// discovery uses, so a box that is briefly busy right after an OTA reboot gets a
// few attempts to answer (a member that has only been up ~57s, its agent still
// starting). A member that answers is "ready"; one that never does is reported
// rather than enrolled, so the caller can form the group with only the ready
// members and tell the user which speaker is still starting. Empty IPs are
// skipped (cannot be probed) and left for the caller to pass through.
func (a *App) ensureZoneMembersReady(ips []string) memberReadiness {
	var res memberReadiness
	for _, ip := range ips {
		if ip == "" {
			continue
		}
		// ~8 s budget per member; a ready box answers on the first attempt in
		// well under a second, so the common case adds almost no latency.
		ctx, cancel := context.WithTimeout(a.appCtx(), 8*time.Second)
		_, ok := probeSTRWithRetry(ctx, ip, 3)
		cancel()
		if ok {
			res.Ready = append(res.Ready, ip)
		} else {
			a.logger.Warn("zone: member not STR-ready, will not enroll it (mid-restart?)", "ip", ip)
			res.NotReady = append(res.NotReady, ip)
		}
	}
	return res
}

// FormZone forms (or replaces) a multiroom zone with masterHost as the master and
// the given slaves (#70 beta). POSTed to the master's agent, which drives the
// native Bose /setZone and persists it so the zone auto-reforms after a reboot.
func (a *App) FormZone(masterHost string, masterPort int, spec ZoneSpec) (result map[string]any, err error) {
	// Log every attempt + outcome here on the app side: the agent logs the
	// firmware /setZone & /addGroup responses on the box, but a remote user's
	// diagnostic bundle ships only app.log, so without this an alpha stereo-pair
	// failure (e.g. the firmware refusing /addGroup) left no trace at all. The
	// error returned to the frontend already carries the agent's "addGroup: ..."
	// / "setZone: ..." text, so this records the real firmware reason.
	a.logger.Info("FormZone: forming (stereo=alpha, zone=beta)", "masterHost", masterHost,
		"master", spec.Master.DeviceID, "masterIP", spec.Master.IP, "slaves", len(spec.Slaves),
		"stereo", spec.Stereo, "mode", spec.Mode)
	defer func() {
		if err != nil {
			a.logger.Warn("FormZone: failed", "stereo", spec.Stereo, "master", spec.Master.DeviceID, "err", err)
		} else {
			a.logger.Info("FormZone: done", "stereo", spec.Stereo, "master", spec.Master.DeviceID, "result", result)
		}
	}()
	if spec.Master.DeviceID == "" || len(spec.Slaves) == 0 {
		return nil, fmt.Errorf("a master and at least one slave are required")
	}
	// Readiness gate (#70): never form a zone against a member whose STR agent is
	// still starting. The master must be ready to drive /setZone at all; a slave
	// that is mid-restart would be silently dropped by the firmware, leaving an
	// incomplete group the user thinks succeeded.
	ips := make([]string, 0, len(spec.Slaves)+1)
	ips = append(ips, spec.Master.IP)
	for _, sl := range spec.Slaves {
		ips = append(ips, sl.IP)
	}
	readiness := a.ensureZoneMembersReady(ips)
	if spec.Master.IP != "" && !strInSlice(readiness.Ready, spec.Master.IP) {
		return nil, fmt.Errorf("box_not_ready: master")
	}
	// Drop slaves that are not ready from this attempt but report them, so the UI
	// can name the speaker that is still starting. The agent-side zone reconcile
	// and the next discovery cycle pick them up once they answer. A slave with no
	// known IP cannot be probed and is passed through unchanged.
	notReady := make([]string, 0)
	readySlaves := make([]ZoneMember, 0, len(spec.Slaves))
	for _, sl := range spec.Slaves {
		if sl.IP == "" || strInSlice(readiness.Ready, sl.IP) {
			readySlaves = append(readySlaves, sl)
		} else {
			notReady = append(notReady, sl.IP)
		}
	}
	spec.Slaves = readySlaves
	if len(spec.Slaves) == 0 {
		return map[string]any{"ok": false, "notReady": notReady}, nil
	}
	b, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	resp, err := a.boxDo(masterHost, masterPort, http.MethodPost, "/api/box/zone", "application/json", string(b))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out == nil {
		out = map[string]any{}
	}
	if len(notReady) > 0 {
		out["notReady"] = notReady
	}
	return out, nil
}

// DissolveZone tears down the multiroom zone led by masterHost (#70 beta).
func (a *App) DissolveZone(masterHost string, masterPort int) error {
	resp, err := a.boxDo(masterHost, masterPort, http.MethodDelete, "/api/box/zone", "", "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return readHTTPError(resp)
	}
	return nil
}

// SpotifySyncTarget is one speaker to copy the Spotify login TO.
type SpotifySyncTarget struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	Name string `json:"name"`
}

// SyncSpotifyLogin copies the go-librespot Spotify credential from whichever
// speaker is already logged into Spotify to all the others, so the user logs in
// ONCE and recall then works on every speaker (#45 root cause: a saved Spotify
// preset with account="" because the box was never logged in). It auto-detects
// the source (the first speaker that returns a stored credential) so the user
// only taps one button. The credential moves only between the user's own
// discovered speakers, over the LAN, never off-device.
func (a *App) SyncSpotifyLogin(boxes []SpotifySyncTarget) (map[string]any, error) {
	var cred []byte
	var sourceHost, sourceName string
	for _, b := range boxes {
		if b.Host == "" {
			continue
		}
		resp, err := a.boxDo(b.Host, b.Port, http.MethodGet, "/spotify/credential", "", "")
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
		ok := resp.StatusCode == http.StatusOK
		resp.Body.Close()
		if ok && len(data) > 0 {
			cred = data
			sourceHost = b.Host
			sourceName = b.Name
			if sourceName == "" {
				sourceName = b.Host
			}
			break
		}
	}
	if len(cred) == 0 {
		return nil, fmt.Errorf("no speaker is logged into Spotify yet. Log one speaker into Spotify first (pick it in the Spotify app and play a track), then sync")
	}
	synced := make([]string, 0, len(boxes))
	failed := map[string]string{}
	for _, b := range boxes {
		if b.Host == "" || b.Host == sourceHost {
			continue
		}
		label := b.Name
		if label == "" {
			label = b.Host
		}
		r2, err := a.boxDo(b.Host, b.Port, http.MethodPost, "/spotify/credential", "application/octet-stream", string(cred))
		if err != nil {
			failed[label] = err.Error()
			continue
		}
		ok := r2.StatusCode == http.StatusOK
		r2.Body.Close()
		if ok {
			synced = append(synced, label)
		} else {
			failed[label] = r2.Status
		}
	}
	a.logger.Info("spotify: synced login to speakers", "source", sourceName, "synced", len(synced), "failed", len(failed))
	return map[string]any{"source": sourceName, "synced": synced, "failed": failed}, nil
}

// GetPresets calls GET /api/presets of the given stick.
// latestBoseFirmware is the final firmware Bose shipped for every SoundTouch
// model (27.0.6, 2022-08-04). There is nothing newer; an older box can be
// brought up to it with the Bose app.
const latestBoseFirmware = "27.0.6"

// FirmwareInfo is a speaker's Bose firmware + model, read from its :8090/info.
// Used as an install pre-flight (and on STR boxes too): STR shows the firmware,
// flags a box that is not on the latest Bose firmware, and includes it in
// install failures so an old firmware can be ruled in or out (#114).
type FirmwareInfo struct {
	Reachable  bool   `json:"reachable"`
	Model      string `json:"model"`      // <type>, e.g. "SoundTouch 20"
	Firmware   string `json:"firmware"`   // SCM softwareVersion, first token
	Short      string `json:"short"`      // human version, e.g. "27.0.6"
	ModuleType string `json:"moduleType"` // scm / sm2 / ...
	Variant    string `json:"variant"`    // taigan / rhino / ...
	Latest     string `json:"latest"`     // the latest Bose firmware (27.0.6)
	Outdated   bool   `json:"outdated"`   // older than Latest
}

// GetBoxFirmware reads :8090/info from a speaker (the Bose REST API, bound on
// 0.0.0.0 and LAN-reachable on stock AND STR boxes) and returns its model +
// firmware. The endpoint is on every SoundTouch, so this works before STR is
// installed as well as afterwards.
func (a *App) GetBoxFirmware(host string) (FirmwareInfo, error) {
	fi := FirmwareInfo{Latest: latestBoseFirmware}
	if host == "" {
		return fi, fmt.Errorf("host is required")
	}
	ctx, cancel := context.WithTimeout(a.appCtx(), 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s:8090/info", host), nil)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fi, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if err != nil {
		return fi, err
	}
	var info struct {
		Type       string `xml:"type"`
		ModuleType string `xml:"moduleType"`
		Variant    string `xml:"variant"`
		Components []struct {
			Category string `xml:"componentCategory"`
			Version  string `xml:"softwareVersion"`
		} `xml:"components>component"`
	}
	if err := xml.Unmarshal(body, &info); err != nil {
		return fi, fmt.Errorf("parse /info: %w", err)
	}
	fi.Reachable = true
	fi.Model = strings.TrimSpace(info.Type)
	fi.ModuleType = strings.TrimSpace(info.ModuleType)
	fi.Variant = strings.TrimSpace(info.Variant)
	// The SCM component carries the main firmware; fall back to the first
	// component that reports a version.
	ver := ""
	for _, c := range info.Components {
		if strings.EqualFold(strings.TrimSpace(c.Category), "SCM") && strings.TrimSpace(c.Version) != "" {
			ver = c.Version
			break
		}
	}
	if ver == "" {
		for _, c := range info.Components {
			if strings.TrimSpace(c.Version) != "" {
				ver = c.Version
				break
			}
		}
	}
	if f := strings.Fields(ver); len(f) > 0 {
		fi.Firmware = f[0] // drop the "epdbuild..." build tail after the space
	}
	fi.Short = shortFirmware(fi.Firmware)
	fi.Outdated = fi.Short != "" && firmwareOlder(fi.Short, latestBoseFirmware)
	return fi, nil
}

// shortFirmware reduces a "27.0.6.46330.5043500" version to its human "27.0.6".
func shortFirmware(v string) string {
	parts := strings.Split(v, ".")
	if len(parts) >= 3 {
		return strings.Join(parts[:3], ".")
	}
	return v
}

// firmwareOlder reports whether version a is older than b, comparing the first
// three numeric segments (major.minor.patch).
func firmwareOlder(a, b string) bool {
	pa := strings.Split(a, ".")
	pb := strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// presetAPIPath is the agent's preset REST route; the slot is appended for
// per-slot writes and deletes.
const presetAPIPath = "/api/presets"

func (a *App) GetPresets(host string, port int) ([]Preset, error) {
	resp, err := a.boxDo(host, port, http.MethodGet, presetAPIPath, "", "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp)
	}
	var out []Preset
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetPreset does PUT /api/presets/<slot>. art is the station logo URL,
// sent to the box as upnp:albumArtURI on play. Routed through
// boxPut so a preset save gets the same :8888<->:17008 port fallback as the
// other box commands.
func (a *App) SetPreset(host string, port int, slot int, name, streamURL, art string, bitrate int, homepage string) error {
	return a.boxPut(host, port, fmt.Sprintf("%s/%d", presetAPIPath, slot),
		Preset{Slot: slot, Name: name, StreamURL: streamURL, Type: "radio", Art: art, Bitrate: bitrate, Homepage: homepage})
}

// SaveLibraryPreset stores a preset saved from a DLNA media server (the Library
// tab). It plays like a radio preset (a stream URL the box pulls) but carries
// the media server name as Source, so the desktop app can show a small "from"
// badge on the preset. Source is cosmetic and round-trips through the agent.
func (a *App) SaveLibraryPreset(host string, port int, slot int, name, streamURL, art string, bitrate int, source string) error {
	return a.boxPut(host, port, fmt.Sprintf("%s/%d", presetAPIPath, slot),
		Preset{Slot: slot, Name: name, StreamURL: streamURL, Type: "radio", Art: art, Bitrate: bitrate, Source: source})
}

// SaveFolderPreset stores a queue preset (a whole DLNA folder, type=queue) on a
// slot. payloadJSON is the already-built preset object from the Library tab
// ({name, type:"queue", shuffle, items:[{url,title,art,mime,duration_sec}...]});
// it is PUT verbatim to /api/presets/<slot> so the frontend owns the shape and
// the agent reloads it into the play-queue on recall. Routed through boxDo for
// the same :8888<->:17008 port fallback as the other preset saves.
func (a *App) SaveFolderPreset(host string, port int, slot int, payloadJSON string) error {
	resp, err := a.boxDo(host, port, http.MethodPut,
		fmt.Sprintf("%s/%d", presetAPIPath, slot), "application/json", payloadJSON)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readHTTPError(resp)
	}
	return nil
}

// SaveSpotifyPreset stores a real Spotify preset (type=spotify with the
// playlist/album URI) on a slot. A long-press while a Spotify playlist plays
// uses this so the saved preset is recallable, shuffled and account-aware,
// instead of a radio link to the raw stream (which showed the album cover, not
// the Spotify logo, and did not recall the playlist). The agent fills the
// account and a stable playlist cover when they are empty.
func (a *App) SaveSpotifyPreset(host string, port int, slot int, name, uri, account string) error {
	return a.boxPut(host, port, fmt.Sprintf("%s/%d", presetAPIPath, slot),
		Preset{Slot: slot, Name: name, Type: "spotify", URI: uri, Account: account})
}

// CopyPresetsAcrossBoxes copies every preset (slots 1-6) from a source speaker
// to a target speaker, preserving radio vs Spotify type and all fields, then
// re-syncs the target's hardware keys so buttons 1-6 reflect the copy. Used by
// the box-to-box preset copy in Speaker Settings so the user does not have to
// re-enter stations on every speaker. Returns the number of presets copied.
func (a *App) CopyPresetsAcrossBoxes(srcHost string, srcPort int, dstHost string, dstPort int) (int, error) {
	if srcHost == "" || dstHost == "" {
		return 0, fmt.Errorf("source and target host are required")
	}
	if srcHost == dstHost {
		return 0, fmt.Errorf("source and target are the same speaker")
	}
	presets, err := a.GetPresets(srcHost, srcPort)
	if err != nil {
		return 0, fmt.Errorf("read source presets: %w", err)
	}
	copied := 0
	for _, p := range presets {
		if p.Slot < 1 || p.Slot > 6 || p.Name == "" {
			continue
		}
		// PUT the source preset verbatim (via boxPut, so the target's port
		// fallback applies too) so radio and Spotify presets keep all their
		// fields (type, uri, account, art, bitrate) with no field mapping.
		if err := a.boxPut(dstHost, dstPort, fmt.Sprintf("%s/%d", presetAPIPath, p.Slot), p); err != nil {
			return copied, fmt.Errorf("write preset %d: %w", p.Slot, err)
		}
		copied++
	}
	// Re-push the target's hardware keys so 1-6 on the speaker match the copy.
	if _, err := a.SyncBoxPresets(dstHost, dstPort); err != nil {
		a.logger.Warn("copy presets: target hardware sync failed", "dst", dstHost, "err", err)
	}
	return copied, nil
}

// DeletePreset does DELETE /api/presets/<slot>.
func (a *App) DeletePreset(host string, port int, slot int) error {
	resp, err := a.boxDo(host, port, http.MethodDelete, fmt.Sprintf("/api/presets/%d", slot), "", "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readHTTPError(resp)
	}
	return nil
}

// PlaySlot triggert POST /api/play/<slot>.
func (a *App) PlaySlot(host string, port int, slot int) error {
	resp, err := a.playPost(host, port, fmt.Sprintf("/api/play/%d", slot), "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s", friendlyError(resp))
	}
	return nil
}

// isTransportNotReady reports whether err is a connection-level failure
// (timeout, refused, reset, no route) rather than an HTTP response from a
// live agent. On BCO boxes the :17008->:8888 redirect and the agent take
// a few seconds to come up after a reboot or OTA; a play issued in that
// window fails at the transport layer and should read as "still starting"
// instead of a raw timeout (a POST :17008/api/play context
// deadline exceeded right after the box rebooted).
func isTransportNotReady(err error) bool {
	if err == nil {
		return false
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"deadline exceeded", "connection refused", "actively refused", "connection reset", "no route to host", "timeout"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// playPost issues a play POST, but first confirms the agent is actually
// reachable with a cheap, fast probe. This is both quicker and more
// reliable than blindly POSTing and waiting out the play timeout:
//
//   - When the box is ready (the common case) the probe answers in well
//     under a second, then the play runs with its full timeout, so a
//     legitimately slow play (e.g. the agent waking the box from standby)
//     is never cut short. Stability is unchanged.
//   - When the box is still coming up after a reboot/OTA, the probe loop
//     detects "not ready" in a few seconds instead of hanging on the
//     full play timeout, and returns the sentinel "box_not_ready" for the
//     UI to render a localized "speaker is still starting" hint.
func (a *App) playPost(host string, port int, path, body string) (*http.Response, error) {
	if !a.waitAgentReady(host, port) {
		return nil, fmt.Errorf("box_not_ready")
	}
	resp, err := a.boxDo(host, port, http.MethodPost, path, "application/json", body)
	if err != nil {
		if isTransportNotReady(err) {
			return nil, fmt.Errorf("box_not_ready")
		}
		return nil, err
	}
	return resp, nil
}

// waitAgentReady probes the agent's version endpoint (the same cheap
// endpoint discovery uses) with a short per-try timeout, briefly
// retrying so a box whose :17008->:8888 redirect and agent are still
// coming up gets a moment to answer. Returns true the instant it
// responds (so a ready box adds only one sub-second round trip), false
// if it stays unreachable within the budget.
func (a *App) waitAgentReady(host string, port int) bool {
	deadline := time.Now().Add(4 * time.Second)
	for {
		// Try each candidate port; the one that answers is cached so the
		// subsequent play (and every later call) goes straight to it. This
		// is where a box that switched ports (reboot/freeze) gets re-pinned.
		for _, p := range a.candidatePorts(host, port) {
			url := fmt.Sprintf("http://%s:%d/api/agent/version", host, p)
			ctx, cancel := context.WithTimeout(a.appCtx(), 1200*time.Millisecond)
			body, ok := httpGetSmall(ctx, url, 1200*time.Millisecond, 512)
			cancel()
			if ok && strings.Contains(string(body), `"version"`) {
				a.rememberPort(host, p)
				return true
			}
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-time.After(400 * time.Millisecond):
		case <-a.ctx.Done():
			return false
		}
	}
}

// PlayURL triggers POST /api/play with an arbitrary stream URL. icon is
// the station logo URL (shown on the box), uuid lets
// radio-browser count the click.
func (a *App) PlayURL(host string, port int, streamURL, title, icon, uuid, mime, homepage string) error {
	body, _ := json.Marshal(map[string]string{
		"url":      streamURL,
		"title":    title,
		"icon":     icon,
		"uuid":     uuid,
		"mime":     mime,
		"homepage": homepage,
	})
	resp, err := a.playPost(host, port, "/api/play", string(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s", friendlyError(resp))
	}
	return nil
}

// StartQueue starts an agent-side library play queue. payloadJSON is the full
// request body the agent expects:
// {"items":[{"url","title","art","mime","duration_sec"}],"start","shuffle","repeat"}.
// The queue auto-advances on the box; a single PlayURL later clears it.
func (a *App) StartQueue(host string, port int, payloadJSON string) error {
	resp, err := a.playPost(host, port, "/api/queue", payloadJSON)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s", friendlyError(resp))
	}
	return nil
}

// QueueNext / QueuePrev skip within the active queue.
func (a *App) QueueNext(host string, port int) error {
	return a.queuePost(host, port, "/api/queue/next", "")
}

func (a *App) QueuePrev(host string, port int) error {
	return a.queuePost(host, port, "/api/queue/prev", "")
}

// QueueShuffle turns shuffle on or off for the active queue.
func (a *App) QueueShuffle(host string, port int, on bool) error {
	b, _ := json.Marshal(map[string]bool{"on": on})
	return a.queuePost(host, port, "/api/queue/shuffle", string(b))
}

// QueueRepeat sets the repeat mode ("off", "all", "one") for the active queue.
func (a *App) QueueRepeat(host string, port int, mode string) error {
	b, _ := json.Marshal(map[string]string{"mode": mode})
	return a.queuePost(host, port, "/api/queue/repeat", string(b))
}

func (a *App) queuePost(host string, port int, path, body string) error {
	resp, err := a.boxDo(host, port, http.MethodPost, path, "application/json", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readHTTPError(resp)
	}
	return nil
}

// GetQueue returns the current queue snapshot (active, pos, shuffle, repeat,
// items) or an empty object when no queue is active.
func (a *App) GetQueue(host string, port int) (map[string]any, error) {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/queue", "", "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// BoxSettings fetches name/volume/bass/network/sources of the box via the stick.
func (a *App) BoxSettings(host string, port int) (map[string]any, error) {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/box/settings", "", "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetBoxName changes the display name of the Bose box.
func (a *App) SetBoxName(host string, port int, name string) error {
	return a.boxPut(host, port, "/api/box/name", map[string]string{"name": name})
}

// SetBoxVolume sets the volume (0-100).
func (a *App) SetBoxVolume(host string, port int, value int) error {
	return a.boxPut(host, port, "/api/box/volume", map[string]int{"value": value})
}

// SetBoxBass sets the bass value (range per box, ST10 e.g. -9..0).
func (a *App) SetBoxBass(host string, port int, value int) error {
	return a.boxPut(host, port, "/api/box/bass", map[string]int{"value": value})
}

// SelectBoxSource switches the box to a different source: "AUX",
// "BLUETOOTH", "STANDBY". The Stick Agent translates that into the matching
// /select or /key call to the Bose REST API.
func (a *App) SelectBoxSource(host string, port int, source string) error {
	return a.boxPut(host, port, "/api/box/source", map[string]string{"source": source})
}

// readHTTPError turns a failed box response into an error carrying the status
// code and a bounded slice of the body. One canonical place for the read limit
// and message format, used at every status>=400 / non-200 site.
func readHTTPError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
}

func (a *App) boxPut(host string, port int, path string, body any) error {
	// Routed through boxDo so the small settings PUTs (volume, bass,
	// name, source, wlan) get the same transparent :8888<->:17008 port
	// fallback as every other agent call: if the box record carries the
	// wrong/stale port, the first attempt fails fast (connection refused)
	// and the alternate is tried and cached, instead of the PUT erroring
	// out on a dead port.
	b, _ := json.Marshal(body)
	resp, err := a.boxDo(host, port, http.MethodPut, path, "application/json", string(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readHTTPError(resp)
	}
	return nil
}

// --- Webhook config (remote thumbs key -> a user-defined HTTP request) ----
//
// The remote's thumbs-up and thumbs-down keys surface on the box only as a
// generic activity ping with no up/down identity, so they share ONE trigger
// (suited to a smart-home on/off toggle). These call STR's /api/webhooks
// endpoints on the agent.

// GetWebhooks reads the agent's webhook config (shape: {"thumb":{...}}).
func (a *App) GetWebhooks(host string, port int) (map[string]any, error) {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/webhooks", "", "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetWebhooks stores the thumbs-trigger HTTP request on the agent.
func (a *App) SetWebhooks(host string, port int, enabled bool, method, url, body, contentType string) error {
	cfg := map[string]any{
		"thumb": map[string]any{
			"enabled":      enabled,
			"method":       method,
			"url":          url,
			"body":         body,
			"content_type": contentType,
		},
	}
	return a.boxPut(host, port, "/api/webhooks", cfg)
}

// SaveWebhookConfig replaces the agent's FULL webhook config (thumb + the
// per-remote-key buttons preset1..preset6, aux, power). The PUT replaces the
// whole config on the agent, so the frontend sends the complete object it built
// from GetWebhooks; saving only one field would wipe the others.
func (a *App) SaveWebhookConfig(host string, port int, cfg map[string]any) error {
	return a.boxPut(host, port, "/api/webhooks", cfg)
}

// TestWebhook fires the given request immediately so the user can verify their
// URL from the app without pressing a key on the box. Returns {ok, status}.
func (a *App) TestWebhook(host string, port int, method, url, body, contentType string) (map[string]any, error) {
	action := map[string]any{
		"enabled":      true,
		"method":       method,
		"url":          url,
		"body":         body,
		"content_type": contentType,
	}
	b, _ := json.Marshal(action)
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/webhooks/test", "application/json", string(b))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, readHTTPError(resp)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// TestWebhookAction fires an arbitrary configured action (http/udp/wol) once for
// the test button, without pressing a key on the box. actionJSON is the full
// webhook Action the frontend built (type + its fields), so the UDP/WoL test
// works the same as the HTTP one (#187). Returns {ok, status}.
func (a *App) TestWebhookAction(host string, port int, actionJSON string) (map[string]any, error) {
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/webhooks/test", "application/json", actionJSON)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, readHTTPError(resp)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
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
	ctx, cancel := context.WithTimeout(a.appCtx(), 4*time.Second)
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
	ctx, cancel := context.WithTimeout(a.appCtx(), 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, a.boseURL(host)+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "text/xml")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readHTTPError(resp)
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
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/box/airplay-opt", "", "")
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
	body, _ := json.Marshal(map[string]bool{"enabled": enabled})
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/box/airplay-opt", "application/json", string(body))
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

// GetResumeOnPowerOn reads the per-box "resume the last station on power-on"
// toggle from the STR agent. Returns {"supported":bool,"enabled":bool}; default
// is enabled. Routed through boxDo so it self-heals across :8888 / :17008 like
// the other box calls (a BCO speaker reachable only on :17008 still answers).
// See internal/webui handleResumeOnPowerOn.
func (a *App) GetResumeOnPowerOn(host string, port int) (map[string]bool, error) {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/box/resume-on-power-on", "", "")
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

// SetResumeOnPowerOn flips the power-on resume toggle on the box. The agent
// persists it to NAND and applies it live (no reboot needed): the next real
// power-on either resumes the last station or stays silent.
func (a *App) SetResumeOnPowerOn(host string, port int, enabled bool) error {
	body, _ := json.Marshal(map[string]bool{"enabled": enabled})
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/box/resume-on-power-on", "application/json", string(body))
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

// GetDisplayTrack reads the per-box "show the live radio track on the speaker
// display" opt-in (default off). Returns {supported, enabled, mode} where mode is
// "both" | "title" | "artist".
func (a *App) GetDisplayTrack(host string, port int) (map[string]any, error) {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/box/display-track", "", "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetDisplayTrack toggles the per-box "show the live radio track on the speaker
// display" opt-in and sets what it shows (mode: "both" | "title" | "artist").
// Enabling it makes the box re-buffer (a brief audio dropout) on each text change.
func (a *App) SetDisplayTrack(host string, port int, enabled bool, mode string) error {
	body, _ := json.Marshal(map[string]any{"enabled": enabled, "mode": mode})
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/box/display-track", "application/json", string(body))
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

// BoxPresetInfo is one of the box's OWN presets (incl. foreign sources like
// Deezer that STR did not set), from GET /api/box/presets.
type BoxPresetInfo struct {
	Slot          int    `json:"slot"`
	Source        string `json:"source"`
	Type          string `json:"type"`
	Location      string `json:"location"`
	SourceAccount string `json:"sourceAccount"`
	Name          string `json:"name"`
}

// BoxPresets reads the box's own preset list (incl. foreign sources). Empty until
// the box has reported a presetsUpdated frame at least once since the agent start.
func (a *App) BoxPresets(host string, port int) ([]BoxPresetInfo, error) {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/box/presets", "", "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var out []BoxPresetInfo
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// BoxSnapshot returns the agent's pre-takeover snapshot of the box's presets +
// sources. Used to warn the user about account-linked cloud sources (Deezer,
// ...) that STR cannot carry over yet, and to show what was there. The shape is
// {captured:bool, lostServices:[], lostPresets:[], presets:[], sources:[]};
// returns {captured:false} when nothing was captured.
func (a *App) BoxSnapshot(host string, port int) (map[string]any, error) {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/box/snapshot", "", "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// RestoreBoxSnapshot (EXPERIMENTAL) asks the agent to write account-linked cloud
// presets (e.g. Deezer) back onto their original slots and re-advertise their
// sources, so the box plays them again via its cached account token. presetsXML
// is an optional box /presets dump the user saved; empty uses the agent's
// snapshot. Returns the agent's result (restored slots, services, failed,
// rebootRecommended).
func (a *App) RestoreBoxSnapshot(host string, port int, presetsXML string) (map[string]any, error) {
	body, _ := json.Marshal(map[string]string{"presetsXML": presetsXML})
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/box/snapshot/restore", "application/json", string(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// RecallBoxPreset plays one of the box's own presets by pressing its hardware
// preset key, so a foreign preset (Deezer) plays via the box's cached account.
func (a *App) RecallBoxPreset(host string, port int, slot int) error {
	body, _ := json.Marshal(map[string]int{"slot": slot})
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/box/presets/recall", "application/json", string(body))
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

// --- Persistent app flags ---
//
// A few one-way app-level flags (e.g. "the user has already been invited to the
// community world map") must survive app version updates and even a reinstall, so
// a one-time prompt never reappears and becomes annoying. The frontend's
// localStorage is NOT reliable for this: a WebView2/WKWebView profile can reset
// on an update or be cleared. These flags live in a tiny JSON file in the OS
// user-config dir (Roaming AppData / ~/Library/Application Support / ~/.config),
// a stable path independent of the app version and the executable location.
var appFlagsMu sync.Mutex

func appStatePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ST Reborn", "app-state.json"), nil
}

func readAppFlags() map[string]bool {
	m := map[string]bool{}
	path, err := appStatePath()
	if err != nil {
		return m
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

// GetAppFlag reports whether a persistent one-way app flag has been set. Used by
// the frontend to gate once-ever prompts so they survive app updates, unlike
// localStorage. Unknown/unset flags return false.
func (a *App) GetAppFlag(name string) bool {
	appFlagsMu.Lock()
	defer appFlagsMu.Unlock()
	return readAppFlags()[name]
}

// SetAppFlag persists a one-way app flag (sets it true, never unset). Best-effort
// and atomic (temp file + rename); a write failure is returned but the caller
// treats it as non-fatal and still falls back to the frontend localStorage guard.
func (a *App) SetAppFlag(name string) error {
	appFlagsMu.Lock()
	defer appFlagsMu.Unlock()
	m := readAppFlags()
	if m[name] {
		return nil
	}
	m[name] = true
	path, err := appStatePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// RescuedSpeakerCount returns how many speakers are shown on the community world
// map, i.e. the sum of the per-pin reaction counts at st-reborn.de/api/pins.php,
// which is exactly what the website's "rescued" counter displays. The world-map
// invite shows it to motivate the user to add their pin. Best-effort: returns 0
// on any error so the invite simply omits the count. Fetched server-side here to
// avoid a cross-origin fetch from the webview.
func (a *App) RescuedSpeakerCount() int {
	ctx, cancel := context.WithTimeout(a.appCtx(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://st-reborn.de/api/pins.php", nil)
	if err != nil {
		return 0
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	var out struct {
		Pins []struct {
			Count int `json:"count"`
		} `json:"pins"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return 0
	}
	total := 0
	for _, p := range out.Pins {
		if p.Count > 0 {
			total += p.Count
		} else {
			total++ // a pin with no explicit count still represents one rescued box
		}
	}
	return total
}

// StreamBitrate returns the agent's currently-detected stream bitrate in
// kbit/s (icy-br, or a throughput sample), or 0 if none/unavailable.
// Routed through boxDo so it self-heals across :8888 / :17008 like every
// other box call. The frontend previously did a raw fetch pinned to
// box.port, which silently failed on BCO speakers (Portable, ST20-spotty)
// reachable only on :17008, so the live bitrate never showed there.
func (a *App) StreamBitrate(host string, port int) int {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/stream/bitrate", "", "")
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	var out struct {
		Bitrate int `json:"bitrate"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0
	}
	return out.Bitrate
}

// SpotifyBitrate returns the bitrate the agent measured from the live
// go-librespot Ogg stream in kbit/s, or 0 if Spotify is idle/unavailable.
// Spotify presets carry no radio-browser bitrate, so the tile reads the
// real measured stream rate here instead of a hardcoded nominal. Routed
// through boxDo so it self-heals across :8888 / :17008 like StreamBitrate.
func (a *App) SpotifyBitrate(host string, port int) int {
	resp, err := a.boxDo(host, port, http.MethodGet, "/spotify/info", "", "")
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	var out struct {
		Bitrate int `json:"bitrate"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0
	}
	return out.Bitrate
}

// StreamTitle returns the live ICY StreamTitle the agent parsed from the radio
// stream currently proxied, or "" when the station sends no metadata. The app
// shows it next to the station name as the now-playing track. Routed through
// boxDo so it self-heals across :8888 / :17008 like StreamBitrate.
func (a *App) StreamTitle(host string, port int) string {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/stream/title", "", "")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var out struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ""
	}
	return out.Title
}

// SpotifyNowPlaying returns the live Spotify state for the UI: measured
// bitrate plus the current track title, artist and cover URL (from
// go-librespot's events). Empty fields when nothing is playing. Routed
// through boxDo so it self-heals across :8888 / :17008.
type SpotifyNow struct {
	Bitrate int    `json:"bitrate"`
	Track   string `json:"track"`
	Artist  string `json:"artist"`
	Cover   string `json:"cover"`
	Context string `json:"context"` // current playlist/album URI (for a long-press save)
	Account string `json:"account"` // current go-librespot login
	// PremiumRequired is true when the box's Spotify account is free/open, which
	// cannot do the autonomous on-demand playback a preset recall needs (#45). The
	// Spotify view shows a "recall needs Premium" note when set.
	PremiumRequired bool `json:"premiumRequired"`
}

func (a *App) SpotifyNowPlaying(host string, port int) SpotifyNow {
	var out SpotifyNow
	resp, err := a.boxDo(host, port, http.MethodGet, "/spotify/info", "", "")
	if err != nil {
		return out
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}

// SyncBoxPresets re-sends all stick presets to the box so that
// the hardware preset buttons 1-6 work. Used by the "Repair hardware
// buttons" button in the Settings tab.
func (a *App) SyncBoxPresets(host string, port int) (map[string]any, error) {
	// boxDo so the :8888<->:17008 self-heal applies (BCO/Portable boxes only
	// answer on the REDIRECTed :17008; a baseURL+raw POST pinned to :8888 failed).
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/box/sync-presets", "application/json", "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, readHTTPError(resp)
	}
	// If the Stick Agent is too old and does not know the endpoint,
	// the fallback to the default handler returns HTML instead of JSON. Check
	// and report it nicely.
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "json") {
		return nil, fmt.Errorf("stick agent is too old for this operation; please update the stick first (update banner at the top)")
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// PhoneQR returns a QR code (a PNG data URI) encoding url, for the "Open on
// your phone" card in Speaker Settings: the user scans it with a phone camera
// to open that speaker's web remote and add it to the home screen. Generated
// locally, so the LAN address never leaves the machine. The caller builds url
// from the box's reachable host:port (probeSTR already records the right port).
func (a *App) PhoneQR(url string) (string, error) {
	if strings.TrimSpace(url) == "" {
		return "", fmt.Errorf("empty url")
	}
	png, err := qrcode.Encode(url, qrcode.Medium, 240)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}

// RebootBox triggers a restart of the Bose box (via the Stick Agent
// shell `reboot`). This makes fresh setup-wizard configs on the
// USB stick take effect immediately, without continuous polling in the agent.
func (a *App) RebootBox(host string, port int) error {
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/box/reboot", "application/json", "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readHTTPError(resp)
	}
	return nil
}

// VoteStation gives a station a thumbs-up on radio-browser.
// Best effort; the error is returned but does not have to be shown.
func (a *App) VoteStation(host string, port int, uuid string) error {
	if uuid == "" {
		return nil
	}
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/radio/vote/"+uuid, "application/json", "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("vote status %d", resp.StatusCode)
	}
	return nil
}

// friendlyError extracts the `detail` field from the Stick API error
// response, if present. Fallback: the raw body.
func friendlyError(resp *http.Response) string {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var m map[string]any
	if err := json.Unmarshal(b, &m); err == nil {
		msg := ""
		if d, ok := m["detail"].(string); ok && d != "" {
			msg = d
		} else if e, ok := m["error"].(string); ok && e != "" {
			msg = e
		}
		// Surface the stable machine `code` (e.g. spotify-not-logged-in,
		// spotify-premium-required) ahead of the human message as "code: message"
		// so the frontend can branch on the code rather than on fragile English
		// wording, and the wording stays free to change (#45). Callers that only
		// show the string still read fine.
		if c, ok := m["code"].(string); ok && c != "" {
			if msg != "" {
				return c + ": " + msg
			}
			return c
		}
		if msg != "" {
			return msg
		}
	}
	return string(b)
}

// Pause / Stop pro Box.
func (a *App) Pause(host string, port int) error  { return a.doAction(host, port, "pause") }
func (a *App) Resume(host string, port int) error { return a.doAction(host, port, "resume") }
func (a *App) Stop(host string, port int) error   { return a.doAction(host, port, "stop") }

func (a *App) doAction(host string, port int, action string) error {
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/"+action, "application/json", "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readHTTPError(resp)
	}
	return nil
}

// pickReachableIP selects, from the IPs the stick announces via mDNS, the one
// reachable from the current LAN. The box's USB gadget interface
// (203.0.113.x) is not routable from the Wi-Fi; the same box also announces
// its real Wi-Fi IP, which is the one we take.
//
// Prioritization:
//
//  1. Private LAN ranges (RFC 1918): 192.168/16, 10/8, 172.16/12
//  2. Link local: 169.254/16
//  3. Public IPs (unlikely)
//
// Skip: 203.0.113/24 (Documentation TEST-NET-3, box USB gadget),
// 127/8 loopback.
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
		// USB gadget TEST-NET-3 is not routable
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

// ListDrives returns all removable volumes that are suitable as a stick target.
// The frontend uses this in the setup wizard.
func (a *App) ListDrives() ([]sticksetup.Drive, error) {
	return sticksetup.ListDrives()
}

// FormatStick reformats the stick as FAT32. WARNING: all data
// is lost. Called before WriteStickFiles when the user has enabled the
// "Format stick first" checkbox.
func (a *App) FormatStick(targetPath string) error {
	// Log the prepare step so a stick-boot install (the ST10 path, which
	// never touches the SSH installer that does log) leaves a trail in the
	// diagnostic bundle. Without this a failed self-install shows only
	// discovery noise and the cause is invisible (see #195).
	a.logger.Info("FormatStick", "comp", "sticksetup", "target", targetPath)
	err := sticksetup.FormatFAT32(targetPath, "REBORN")
	if err != nil {
		a.logger.Warn("FormatStick failed", "comp", "sticksetup", "target", targetPath, "err", err)
	}
	return err
}

// WriteStickFiles populates the given volume with all the necessary
// files (templates plus the embedded Stick Agent binary). The binary
// is embedded at app build time and needs no path from the user.
// The app version PLUS build stamp is written to version.txt
// (format "1.0.0+2026-05-15-2202") so that the update detector also
// recognizes build differences when the version number is the same.
func (a *App) WriteStickFiles(targetPath string) ([]string, error) {
	v := appVersion
	if appBuild != "" && appBuild != "dev" {
		v = appVersion + "+" + appBuild
	}
	files, err := sticksetup.WriteStickFiles(targetPath, agentbin.Bytes(), agentbin.GoLibrespotBytes(), v)
	// Record what was staged onto the stick. agentBytes confirms the embedded
	// agent is present (0 on a dev build, which is itself the cause of a
	// non-starting agent), and the file count/version pins the attempt in the
	// bundle so a later stick-boot failure is traceable (see #195).
	a.logger.Info("WriteStickFiles",
		"comp", "sticksetup", "target", targetPath, "version", v,
		"agentBytes", len(agentbin.Bytes()), "fileCount", len(files), "err", err)
	return files, err
}

// WriteWLANConfig writes a Wi-Fi config onto the stick. Optional before
// the eject; the box's run.sh detects it on first boot.
func (a *App) WriteWLANConfig(targetPath, ssid, password string) error {
	return sticksetup.WriteWLANConfig(targetPath, sticksetup.WLANConfig{
		SSID: ssid, Password: password,
	})
}

// WriteRegionConfig writes a region.conf JSON file (ISO 3166-1
// alpha-2 country code) onto the stick. The stick persists it on boot
// to NAND and uses it as the default for radio search and language.
func (a *App) WriteRegionConfig(targetPath, country string) error {
	return sticksetup.WriteRegionConfig(targetPath, sticksetup.RegionConfig{Country: country})
}

// WriteNameConfig writes a name.conf JSON file with the box name
// requested by the user onto the stick. The stick applies it on first
// boot to the box via the Bose REST API, verbatim, so the user's chosen
// name stays clean (#133, #292).
func (a *App) WriteNameConfig(targetPath, name string) error {
	return sticksetup.WriteNameConfig(targetPath, sticksetup.NameConfig{Name: name})
}

// WriteLangConfig writes lang.conf onto the stick. locale + country
// are the wizard signals, sysLanguage the Bose value chosen by the user
// in the language dropdown. The box's run.sh reads the integer on first
// boot as the OOB-gate language AND display language, instead of forcing
// German worldwide. See project_bose_language_enum.
func (a *App) WriteLangConfig(targetPath, locale, country string, sysLanguage int) error {
	return sticksetup.WriteLangConfig(targetPath, locale, country, sysLanguage)
}

// SuggestBoxLanguage returns the Bose sysLanguage that the setup wizard
// should preselect in the language dropdown: derived primarily from the
// chosen country, with the active app language as a deliberate override,
// otherwise English. The frontend calls it on load and on every country change.
func (a *App) SuggestBoxLanguage(locale, country string) int {
	return sticksetup.SuggestBoxLanguage(locale, country)
}

// SetAppLocale remembers the user's UI-active language (BCP-47,
// e.g. "de"/"en"). The frontend calls it at startup and on every
// language change. Server-side provisioning paths (setup-AP push)
// derive the box display language from it.
func (a *App) SetAppLocale(locale string) {
	a.localeMu.Lock()
	a.userLocale = strings.TrimSpace(locale)
	a.localeMu.Unlock()
}

// appLocale returns the most recently reported UI locale (empty if none
// has been set yet).
func (a *App) appLocale() string {
	a.localeMu.RLock()
	defer a.localeMu.RUnlock()
	return a.userLocale
}

// ListWiFiProfiles returns the saved Wi-Fi profiles from the host OS.
// The frontend uses this as a dropdown in setup so the user does not have
// to type the SSID.
func (a *App) ListWiFiProfiles() ([]wifiprofiles.Profile, error) {
	return wifiprofiles.List()
}

// TryWiFiPassword tries to read the saved password for an SSID.
// On Windows this works for profiles the user saved themselves
// without admin rights. On Mac/Linux it may need user consent.
// Returns empty when nothing is found.
func (a *App) TryWiFiPassword(ssid string) string {
	pw, _ := wifiprofiles.TryPassword(ssid)
	return pw
}

// CurrentWiFi returns the SSID of the currently connected Wi-Fi. Used in the UI
// as the default selection in the dropdown.
func (a *App) CurrentWiFi() string {
	return wifiprofiles.CurrentSSID()
}

// IsBoseStick true when the volume already holds an STR install.
func (a *App) IsBoseStick(path string) bool {
	return sticksetup.IsBoseStick(path)
}

// StickVersion reads version.txt from the stick.
func (a *App) StickVersion(path string) string {
	return sticksetup.StickVersion(path)
}

// CheckStick technically checks whether the volume is suitable as an install stick
// (present, FAT32, large enough, writable). The setup wizard calls this
// before letting the user proceed, so that an NTFS/exFAT, too-small or
// write-protected stick is caught early with a clear message instead of
// running into a later cryptic error.
func (a *App) CheckStick(path string) sticksetup.StickCheck {
	return sticksetup.CheckStick(path)
}

// StickConfigs returns not-yet-applied setup configs from the stick
// (wlan, region, name). Used to pre-fill the wizard.
func (a *App) StickConfigs(path string) sticksetup.StickConfigs {
	return sticksetup.ReadStickConfigs(path)
}

// AppVersion returns the semver version of the running app.
func (a *App) AppVersion() string { return appVersion }

// AppInfo returns app metadata (version, build, author, URLs) for
// the About dialog, footer and auto-update check.
//
// UpdateManifestURL points to a small JSON file of the form
//
//	{"version":"1.1.0","build":"2026-06-01-0900","downloadUrl":"https://.../app-windows-amd64.exe","notes":"..."}
//
// On startup the app checks whether the remote version is greater than its
// own and then shows an update banner. Empty = auto-update off.
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

// Versions are set via -ldflags X in the build; defaults are for
// development only.
var (
	appVersion = "1.0.0"
	appBuild   = "dev"
)

func (a *App) AppInfo() AppInfo {
	return AppInfo{
		Version:    appVersion,
		Build:      appBuild,
		Author:     "Jens Roggenfelder (JRpersonal)",
		GitHubURL:  "https://github.com/JRpersonal/streborn",
		WebsiteURL: "https://st-reborn.de",
		DonateURL:  "", // populated once the PayPal link on the website is live
		// DonateSlogan is left empty so the frontend renders the
		// locale-aware fallback from the i18n bundle. Hardcoding
		// German here would shadow the bundle for every locale.
		DonateSlogan: "",
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
	// Kill switch to A/B test whether the startup update check is behind a
	// report (e.g. a macOS start crash). With STR_NO_UPDATE_CHECK set
	// the check is a no-op, so a user can run with it fully off and see if
	// the crash persists.
	if strings.TrimSpace(os.Getenv("STR_NO_UPDATE_CHECK")) != "" {
		return map[string]string{}, nil
	}
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
	ctx, cancel := context.WithTimeout(a.appCtx(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	// Stable, identifiable agent string so the server can filter bots and
	// keep meaningful update-check stats.
	req.Header.Set("User-Agent", "STReborn-Desktop/"+info.Version+" ("+runtime.GOOS+"; "+runtime.GOARCH+")")
	// Use the pure-Go update client (embedded RootCAs + PreferGo), NOT the
	// shared httpClient. The shared one leaves TLS verification to the
	// platform, which on macOS runs through cgo (Security.framework) and
	// crashed an old Mac on this very call (#102). See updateHTTPClient.
	resp, err := updateHTTPClient().Do(req)
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

// ResolveStationLogo returns the best real logo URL for a station, or ""
// when none exists (the frontend then draws a local monogram instead).
//
// It exists because DuckDuckGo's icon service answers HTTP 404 with a
// grey "no icon" placeholder image (a chevron) for hosts it does not
// know. The Wails webview renders that 404 body instead of firing the
// <img> error handler, so a pure-frontend cascade cannot tell a real
// icon from the placeholder and the grey chevron wins. Here, in Go, we
// can read the HTTP status: 200 means a real icon, 404 means placeholder
// (skip it). Results are cached per (favicon, hosts) for the app run.
func (a *App) ResolveStationLogo(faviconURL string, brandHost string, hosts []string) string {
	key := faviconURL + "\x1f" + brandHost + "\x1f" + strings.Join(hosts, ",")
	a.logoMu.Lock()
	if a.logoCache == nil {
		a.logoCache = map[string]string{}
	}
	if v, ok := a.logoCache[key]; ok {
		a.logoMu.Unlock()
		return v
	}
	a.logoMu.Unlock()

	best := ""
	// 1. The station's own HTTPS favicon, if it really serves an image.
	//    HTTP favicons are skipped: the secure webview blocks them as
	//    mixed content anyway.
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(faviconURL)), "https://") {
		if status, ctype := a.headStatusType(faviconURL); status == http.StatusOK && strings.HasPrefix(ctype, "image/") {
			best = faviconURL
		}
	}
	// 2. DuckDuckGo per host. 200 = real icon; 404 = grey placeholder, so
	//    only a 200 counts.
	if best == "" {
		for _, h := range hosts {
			h = strings.TrimSpace(h)
			if h == "" {
				continue
			}
			u := "https://icons.duckduckgo.com/ip3/" + h + ".ico"
			if status, _ := a.headStatusType(u); status == http.StatusOK {
				best = u
				break
			}
		}
	}
	// 3. The brand site's own /favicon.ico at the standard path. DuckDuckGo
	//    often does not know smaller brand domains (e.g. epic-classical.com)
	//    even though they serve a favicon. Only the homepage host is tried,
	//    never a stream CDN, so a shared provider logo cannot leak in. Last
	//    because brand sites can be slow; this runs only for the minority
	//    that the favicon field and DuckDuckGo both missed.
	if best == "" && strings.TrimSpace(brandHost) != "" {
		u := "https://" + strings.TrimSpace(brandHost) + "/favicon.ico"
		if status, ctype := a.headStatusType(u); status == http.StatusOK && strings.HasPrefix(ctype, "image/") {
			best = u
		}
	}

	a.logoMu.Lock()
	a.logoCache[key] = best
	a.logoMu.Unlock()
	return best
}

// headStatusType fetches just enough of url to learn the HTTP status and
// Content-Type, with a short timeout so a slow host never stalls logo
// resolution. The body is not read. A GET (not HEAD) is used because
// some icon hosts mishandle HEAD; the response is closed immediately.
func (a *App) headStatusType(url string) (int, string) {
	ctx, cancel := context.WithTimeout(a.appCtx(), 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, ""
	}
	req.Header.Set("User-Agent", "STReborn-Desktop")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return 0, ""
	}
	resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Content-Type")
}

// EjectDrive ejects the stick so the user can remove it.
func (a *App) EjectDrive(path string) error {
	return sticksetup.Eject(path)
}

// Status returns the now_playing XML as a string. The frontend can
// regex-parse it itself.
func (a *App) Status(host string, port int) (string, error) {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/status", "", "")
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
	ctx, cancel := context.WithTimeout(a.appCtx(), 12*time.Second)
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
