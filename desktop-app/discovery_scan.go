package main

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/discovery"
	"github.com/JRpersonal/streborn/dlna"
)

// Speaker discovery, the scan side: DiscoverBoxes (mDNS + /24 sweep + SSDP + known-speaker probe), the :8090 enrichment, the manual add-by-IP path and the LAN sweeps. Split out of app_discovery.go.

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
	upsert := func(b BoxInfo) { upsertSighting(seen, b) }

	// Cold-start safety net: probe the speakers persisted from earlier runs
	// directly and in parallel with the whole discovery. A speaker at its
	// last-known IP then appears on the very first scan even when mDNS is
	// blind on this host and the subnet sweep runs out of budget.
	knownHits := make(chan BoxInfo, maxKnownSpeakers)
	go probeKnownSpeakers(ctx, a.logger, knownHits)

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
			BoxDeviceID:  inst.BoxDeviceID,
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
	// Third source, costing the LAN nothing: the speakers announce THEMSELVES.
	// Every SoundTouch on FW 27.0.6 sends periodic SSDP ssdp:alive NOTIFYs for
	// its :8091 MediaRenderer (measured 2026-08-24 across ST30, ST10 and
	// Portable), and the dlna package's passive listener has been collecting
	// them for the app's whole lifetime anyway (#341). That reaches two boxes
	// the paths above structurally miss: a speaker on a LAN where mDNS is dead
	// (instancesFromMDNS=0 every cycle, the recurring router-setup case) that
	// is ALSO outside the sweep's /24, and one the 12 s sweep budget skipped.
	// Announcements are candidates, never results: each host is probed through
	// the same classification as a sweep hit, so a stale announcement costs one
	// failed probe and can never show a speaker that is not there. Self-assigned
	// addresses are skipped for the reason discovery_coldstart already skips
	// them: unroutable from an ordinary LAN, and the same box re-announces its
	// real lease anyway.
	var ssdpHits int
	fbWG.Add(1)
	go func() {
		defer fbWG.Done()
		hosts := dlna.AnnouncedBoseHosts()
		// Bounded so a hostile or absurd multicast neighbourhood cannot turn
		// discovery into a probe storm; a home fleet is single digits.
		if len(hosts) > 24 {
			hosts = hosts[:24]
		}
		var wg sync.WaitGroup
		for _, h := range hosts {
			if ip := net.ParseIP(h); ip == nil || ip.IsLoopback() ||
				(ip.IsLinkLocalUnicast() && !hostHasLinkLocalIPv4()) || isLocalSubnetBroadcast(ip) {
				continue
			}
			host := h
			wg.Add(1)
			go func() {
				defer wg.Done()
				var found BoxInfo
				ok := false
				if b, hit := probeSTR(fallbackCtx, host); hit {
					found, ok = b, true
				} else if b, hit := probeStock(fallbackCtx, host); hit {
					found, ok = b, true
				}
				if !ok {
					return
				}
				fbMu.Lock()
				ssdpHits++
				upsert(found)
				fbMu.Unlock()
			}()
		}
		wg.Wait()
	}()
	fbWG.Wait()
	fallbackCancel()
	// The known-speaker probes finished long ago (single 3 s probes launched
	// at discovery start); fold their hits in on the single-threaded side.
	knownFound := 0
	for b := range knownHits {
		knownFound++
		upsert(b)
	}
	// Say WHICH networks were searched, not just that nothing was found. A
	// bundle with totalBoxes=0 used to carry no way of telling "the speaker is
	// off" from "this PC was looking at the wrong network", and that is the
	// commonest cause of an install that cannot find anything: a guest Wi-Fi, a
	// VPN or virtual adapter that hides the LAN, or client isolation. Three
	// rounds of mail with one reporter turned on exactly this question
	// (2026-10-01), and the log could not answer it.
	//
	// The COUNT only, never the addresses. The bundle scrubber rewrites LAN
	// addresses anyway, so logging them would hand the reader 192.0.2.x and say
	// nothing. The count survives scrubbing and carries the answer: 0 means this
	// PC had no private IPv4 network to search at all, and more than 1 means it
	// sits on several, which is where a VPN or virtual adapter shows up.
	//
	// Worth knowing when reading it: the sweep only ever covers the /24s derived
	// from this PC's OWN addresses, so a speaker on a different subnet is never
	// probed however long the user waits.
	nets := localIPv4Subnets()
	a.logger.Info("discovery: TCP fallback done", "stockHits", stockHits, "strHits", strHits, "ssdpHits", ssdpHits, "knownDirect", knownFound,
		"subnetsSearched", len(nets))
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
	//
	// Sightings without a live agent probe behind them are handed over as
	// presence-only, like RefreshKnownBoxes does: a stock-only record, or an
	// STR label that only an announcement backs (PortVerified false). They keep
	// the box listed but neither confirm STR on it nor reset its agent-silence
	// streak, so a box whose agent is gone does not get its stale version
	// badge re-armed by every full discovery.
	presenceOnly := make(map[string]bool, len(seen))
	for key, b := range seen {
		if b.Kind == "stock" || !b.PortVerified {
			presenceOnly[key] = true
		}
	}
	a.mergeDiscoveryCacheWith(seen, presenceOnly)

	// Collapse any same-device duplicate that survived the IP-keyed upsert and the
	// cache merge: a DHCP lease change leaves the box's stale mDNS record (old IP)
	// cached and re-announced alongside its fresh one (new IP), so both reach `seen`
	// this cycle under different host keys and the speaker shows up twice (live
	// ST300, 2026-07-07: .35 stale + .43 live). Keeps the currently-reachable IP.
	a.dedupeByDeviceID(seen)

	// Remember this cycle's speakers for the next cold start (best-effort,
	// skipped when unchanged).
	go a.persistKnownSpeakers()

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

// upsertSighting folds one sighting into this cycle's result set, keyed by
// host. It is the merge step of DiscoverBoxes, lifted out so the exact
// sequence a cycle produces (mDNS announcement first, live probe second) can
// be replayed in a test.
func upsertSighting(seen map[string]BoxInfo, b BoxInfo) {
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

// enrichSeenBoxes fans out enrichBoxWithStockInfo for every box in
// seen that is still missing a SerialNumber, then writes the
// enriched record back into seen under the same key. Bounded
// parallelism (8 in flight) keeps the discovery latency low even
// on a LAN with many speakers; per-call timeout (1.5s, inside
// enrichBoxWithStockInfo) caps the worst case.
func (a *App) enrichSeenBoxes(ctx context.Context, seen map[string]BoxInfo) {
	// Snapshot the work list BEFORE fanning out. The goroutines below write
	// back into `seen`, and Go treats a map write while a range over the SAME
	// map is still in progress as a fatal runtime error ("concurrent map
	// iteration and map write") - fatal meaning it cannot be recovered and
	// kills the whole app on the spot, with no dialog and no log line. The
	// mutex here only serialized the writers against each other, never
	// against this loop, and the semaphore even parks the loop mid-range
	// once eight enrichments are in flight, so the window is wide open on a
	// LAN with several speakers. Iterating a snapshot removes the race
	// entirely; the writers keep the mutex for their own overlap.
	type enrichJob struct {
		key string
		box BoxInfo
	}
	jobs := make([]enrichJob, 0, len(seen))
	for key, b := range seen {
		if b.SerialNumber != "" && b.Model != "" {
			continue
		}
		jobs = append(jobs, enrichJob{key: key, box: b})
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	var mu sync.Mutex
	for _, jb := range jobs {
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
		}(jb.key, jb.box)
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
	// Probe STR (:8888/:17008) and the stock Bose API (:8090) in PARALLEL and
	// prefer the STR record. The old sequential order starved the stock probe
	// on routed/firewalled networks: a firewall that silently DROPS packets to
	// the closed STR ports makes every STR connect hang until its own timeout,
	// the two 3 s retry rounds ate the entire 6 s budget, and probeStock then
	// ran against an already-expired context. Result: "no speaker reachable"
	// after exactly 6 s although :8090 answered curl in 20 ms (#420, macOS
	// across two routed subnets).
	type stockRes struct {
		box BoxInfo
		ok  bool
		err error
	}
	stockCh := make(chan stockRes, 1)
	go func() {
		b, ok, err := probeStockDetail(ctx, host)
		stockCh <- stockRes{b, ok, err}
	}()
	if str, isSTR := probeSTRWithRetry(ctx, host, 2); isSTR {
		box = str
	} else if stock := <-stockCh; stock.ok {
		box = stock.box
	} else {
		// Surface the underlying network error: without it a local-network
		// privacy denial, a routing failure, a timeout and a non-SoundTouch
		// responder were indistinguishable in a diagnostic bundle (#420).
		a.logger.Warn("AddBoxByIP: nothing answered", "host", host, "stockProbeErr", stock.err)
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

	// Per-subnet parallel pools; see probeLANForSTR for why.
	sweepSubnets(ctx, subnets, probeOne)
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

	var out []BoxInfo
	var collectWG sync.WaitGroup
	collectWG.Add(1)
	go func() {
		defer collectWG.Done()
		for h := range hits {
			out = append(out, h)
		}
	}()

	// Per-subnet parallel pools (sweepSubnets): a virtual adapter's dead /24
	// used to starve the real LAN inside the shared discovery budget on a
	// cold neighbor cache (every probe holds a worker for the full timeout).
	sweepSubnets(ctx, subnets, func(ip string) {
		if b, ok := probeSTR(ctx, ip); ok {
			hits <- b
		}
	})
	close(hits)
	collectWG.Wait()
	return out
}
