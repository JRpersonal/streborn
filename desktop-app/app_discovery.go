package main

// This file was split out of app.go (wave-1 move-only refactor):
// speaker discovery (mDNS + LAN probing), the discovery cache, and box identity/merge helpers.

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/JRpersonal/streborn/discovery"
)

// discEntry is one cached discovery result plus when it was last
// genuinely seen (not counting cache re-adds).
type discEntry struct {
	box  BoxInfo
	seen time.Time
	// firstMiss is when the box first failed ALL probes in an unbroken run
	// of misses (zero while the box keeps answering). Eviction is based on
	// this, not on `seen`: discovery cycles only run on user actions, so a
	// wall-clock TTL on `seen` evicted a speaker that rebooted after the
	// user had simply been listening for a few minutes - the very first
	// failed cycle saw a long-expired timestamp and dropped it with no way
	// back short of a manual refresh. Miss-based, every box gets the full
	// grace window of ACTUAL misses to finish its reboot.
	firstMiss time.Time
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

// discoveryOfflineRetention is how long a box that misses EVERY probe stays
// listed as a greyed-out offline tile after its grace window (the sticky TTLs
// above) ran out. Requirement (2026-07-26): a speaker once sighted must not
// vanish from the list just because it is rebooting or a scan misfired; it
// greys out, shows how long it has been unseen, and comes back to life the
// moment it answers. Only a box silent for a full day is dropped.
const discoveryOfflineRetention = 24 * time.Hour

// strKnownTTL is how long a box's confirmed-STR identity is remembered by
// deviceID after its last STR sighting. It must comfortably outlast a runtime
// Wi-Fi change / band switch and a box left off overnight so the speaker never
// flickers to a false "STR not installed" reinstall prompt when it returns on a
// new DHCP lease, while still being short enough that a box genuinely reverted
// to stock (uninstalled outside the app, or NAND wiped) eventually reclassifies.
// An in-app uninstall clears the entry immediately (see UninstallSTR), so this
// only bounds the exotic out-of-band case. See strKnown and mergeDiscoveryCache.
const strKnownTTL = 24 * time.Hour

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
	// Offline marks a speaker that was seen earlier but has missed every probe
	// for longer than its reboot grace window (e.g. it is rebooting for a long
	// time, powered off, or off the LAN). The tile stays listed greyed out
	// (sticky list, 2026-07-26) until the box answers again or the 24 h
	// retention expires. OfflineSinceSec is how long ago the miss streak
	// started, for the "last seen ..." tooltip; only meaningful while Offline.
	Offline         bool `json:"offline,omitempty"`
	OfflineSinceSec int  `json:"offlineSinceSec,omitempty"`
	// BoxHealth is the agent's wedged-control verdict ("ok" or "wedged"): a
	// wedged box accepts transport pushes but never plays, and only a
	// power-cycle clears it. The UI turns "wedged" into a pull-the-plug hint.
	BoxHealth string `json:"boxHealth,omitempty"`
	// ConflictingMod names a rival cloud-free SoundTouch tool (e.g. "AfterTouch")
	// whose leftover files STR found on the box. Two such tools fight over the
	// cloud redirect, OLED, Wi-Fi and presets; the UI warns the user to remove it
	// (#270). Empty on an STR-only box.
	ConflictingMod string `json:"conflictingMod,omitempty"`
	// Storm1036 is true while the box rejects essentially every preset recall
	// (Bose error 1036, "not logged in"). Nothing the user presses will play
	// until the state clears, and the remedy people reach for on their own,
	// pulling the plug, resets the box clock and poisons the next boot, while a
	// SOFT reboot clears it (#419 Finding 4). The UI says so and offers the
	// reboot. Storm1036SinceSec is how long it has been going.
	Storm1036         bool `json:"storm1036,omitempty"`
	Storm1036SinceSec int  `json:"storm1036SinceSec,omitempty"`
	// RecallRefusal is the storm's quiet sibling: the agent latched
	// consecutive recalls whose source self-dropped to STANDBY without a
	// single 1036. Joined into the same banner (a soft restart clears both).
	RecallRefusal         bool `json:"recallRefusal,omitempty"`
	RecallRefusalSinceSec int  `json:"recallRefusalSinceSec,omitempty"`
	// WLANCredsMissing is true when STR has no saved Wi-Fi on the box: it only
	// stays online while the stick or an ethernet cable is inserted and strands
	// the user on the next cold boot. The UI warns the user to run STR's own
	// Wi-Fi setup (#270).
	WLANCredsMissing bool `json:"wlanCredsMissing,omitempty"`
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
	// The known-speaker probes finished long ago (single 3 s probes launched
	// at discovery start); fold their hits in on the single-threaded side.
	knownFound := 0
	for b := range knownHits {
		knownFound++
		upsert(b)
	}
	a.logger.Info("discovery: TCP fallback done", "stockHits", stockHits, "strHits", strHits, "knownDirect", knownFound)
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
	a.mergeDiscoveryCacheWith(seen, nil)
}

// mergeDiscoveryCacheWith is mergeDiscoveryCache with an extra presenceOnly
// set (keyed like seen): hosts that answered ONLY on the stock :8090 port this
// cycle. They count as present (eviction timer refreshes) but NOT as an STR
// sighting, so a box whose agent is permanently gone (uninstalled out-of-band,
// NAND wiped) stops perpetually re-confirming its stale STR identity memory.
func (a *App) mergeDiscoveryCacheWith(seen map[string]BoxInfo, presenceOnly map[string]bool) {
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
		}
		// A genuine sighting always clears the offline marker, whatever the
		// merge carried over from the cached record.
		b.Offline = false
		b.OfflineSinceSec = 0
		seen[key] = b
		a.discCache[key] = discEntry{box: b, seen: now}
	}
	// Devices GENUINELY seen this cycle (before any cache re-adds), keyed by their
	// stable deviceID. Used just below to drop a stale cache entry for a device that
	// reappeared at a NEW IP this cycle: a router restart (or a LAN<->Wi-Fi / band
	// switch) hands every speaker a fresh DHCP lease at once, so without this each
	// box would linger in the list a second time at its dead old IP until the sticky
	// TTL expires, and half those tiles would fail to play.
	freshDevices := make(map[string]string, len(seen)) // deviceID -> live host
	for _, b := range seen {
		if b.DeviceID != "" {
			freshDevices[b.DeviceID] = b.Host
		}
	}
	// Re-add boxes the current cycle missed; evict after a full grace window
	// of consecutive misses.
	for key, e := range a.discCache {
		if _, ok := seen[key]; ok {
			continue
		}
		// Same physical box found at a NEW IP this cycle -> this cached record is its
		// dead old IP. Evict it so the speaker shows once, at its live address.
		if e.box.DeviceID != "" {
			if liveHost, moved := freshDevices[e.box.DeviceID]; moved && liveHost != key {
				delete(a.discCache, key)
				continue
			}
		}
		ttl := discoveryStickyTTL
		if e.box.Kind == "str" {
			// A known STR box gets a longer grace so a post-OTA reboot does not
			// evict it and let a transient stock sighting relabel it as "needs
			// install" (#108).
			ttl = discoverySTRStickyTTL
		}
		if e.firstMiss.IsZero() {
			// First miss of a streak: start the grace window NOW. Basing it on
			// `seen` instead evicted a rebooting speaker on the very first
			// failed cycle whenever no discovery had run for a while (the
			// status-fail auto-rediscover then found nothing mid-reboot, the
			// box got deselected, and nothing ever brought it back).
			e.firstMiss = now
			a.discCache[key] = e
			seen[key] = e.box
		} else if now.Sub(e.firstMiss) <= ttl {
			seen[key] = e.box
		} else if now.Sub(e.firstMiss) <= discoveryOfflineRetention {
			// Missed every probe past the reboot grace: keep the tile, greyed
			// out, instead of dropping it (sticky list, 2026-07-26). The box
			// record is annotated on the fly; the cached record stays clean so
			// a comeback restores it verbatim.
			b := e.box
			b.Offline = true
			b.OfflineSinceSec = int(now.Sub(e.firstMiss).Seconds())
			seen[key] = b
		} else {
			// Silent for a full day: genuinely gone (powered off / removed).
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

	// STR identity memory (deviceID-keyed, survives an IP change). The pins above
	// are keyed by IP and cannot help a box that returned on a new DHCP lease
	// after a runtime Wi-Fi change / band switch — it reappears under a fresh key
	// with no STR history, so a transient stock-only sighting relabels it "needs
	// install" (Albrecht, 2026-07-05: switching a box from the 2.4 GHz to the
	// 5 GHz SSID made STR report it as not installed). deviceID is stable across
	// the lease change, so remember every confirmed-STR box by deviceID and keep a
	// later stock sighting of the SAME device classified STR.
	if a.strKnown == nil {
		a.strKnown = map[string]discEntry{}
	}
	// Record: refresh the identity memory for every box confirmed STR this
	// cycle. Presence-only sightings (stock :8090 answered, agent did not) are
	// excluded: they prove the box is on the LAN, not that STR still runs on
	// it, and counting them kept a genuinely reverted box labelled STR forever.
	for key, b := range seen {
		if presenceOnly[key] {
			continue
		}
		if b.Kind == "str" && b.DeviceID != "" {
			a.strKnown[b.DeviceID] = discEntry{box: b, seen: now}
		}
	}
	// Reconcile: a stock sighting of a device we recently knew as STR is the
	// misclassification above. mergeBoxInfo promotes it back to STR and restores
	// the remembered port/version/name (so no spurious update prompt) while
	// keeping the live host it answered on this cycle. Expire memories past the
	// TTL so a box genuinely reverted to stock eventually reclassifies.
	for key := range seen {
		b := seen[key]
		if b.Kind != "stock" || b.DeviceID == "" {
			continue
		}
		memo, ok := a.strKnown[b.DeviceID]
		if !ok {
			continue
		}
		if now.Sub(memo.seen) > strKnownTTL {
			delete(a.strKnown, b.DeviceID)
			continue
		}
		relabelled := mergeBoxInfo(memo.box, b)
		relabelled.Kind = "str"
		// mergeBoxInfo only restores the port from a PortVerified memory, so a memory
		// that originated from an mDNS announce would leave the stock :8090 on the
		// relabelled STR box and the app would try to reach the agent there. We know
		// memo IS the STR record, so prefer its agent port outright.
		if memo.box.Port != 0 && memo.box.Port != 8090 {
			relabelled.Port = memo.box.Port
			if memo.box.PortVerified {
				relabelled.PortVerified = true
			}
		}
		seen[key] = relabelled
		a.discCache[key] = discEntry{box: relabelled, seen: now}
		// Update the memory's box record (live host/port) but keep its ORIGINAL
		// timestamp: the relabel itself is not an STR confirmation, and stamping
		// it fresh here meant the strKnownTTL escape hatch ("a box genuinely
		// reverted to stock eventually reclassifies") could never fire while
		// the app was in use.
		a.strKnown[b.DeviceID] = discEntry{box: relabelled, seen: memo.seen}
		if a.logger != nil {
			a.logger.Info("discovery: box kept STR by device identity across a stock-only sighting (e.g. Wi-Fi band switch)",
				"host", b.Host, "deviceID", b.DeviceID)
		}
	}
	// Drop STR identity memories not refreshed within the TTL.
	for id, e := range a.strKnown {
		if now.Sub(e.seen) > strKnownTTL {
			delete(a.strKnown, id)
		}
	}
}

// dedupeByDeviceID collapses records that share a stable, non-empty DeviceID but
// sit at different IPs. That duplicate is what a DHCP lease change leaves behind:
// the box's stale mDNS A-record at its old IP is still cached and re-announced
// alongside the fresh one at the new IP, so BOTH reach `seen` in the same cycle
// under different host keys and the speaker shows up twice (live ST300,
// 2026-07-07: .35 stale + .43 live). The IP-keyed upsert in DiscoverBoxes cannot
// catch this (two IPs => two keys), and mergeDiscoveryCache only evicts a stale IP
// that is cache-only, not one that was itself (re-)announced this cycle. For each
// duplicated DeviceID it keeps the currently-reachable IP (a quick TCP probe) and
// drops the rest from BOTH `seen` and the sticky cache, so a stale IP cannot
// flicker back on a later cycle. Records without a DeviceID are left untouched:
// stock Bose mDNS sometimes omits the MAC, and collapsing those would merge
// distinct speakers (the reason the upsert is IP-keyed in the first place).
func (a *App) dedupeByDeviceID(seen map[string]BoxInfo) {
	byDevice := map[string][]string{}
	for key, b := range seen {
		if b.DeviceID == "" {
			continue
		}
		byDevice[b.DeviceID] = append(byDevice[b.DeviceID], key)
	}
	var dropped []string
	for dev, keys := range byDevice {
		if len(keys) < 2 {
			continue
		}
		winner := pickLiveBoxKey(seen, keys)
		for _, key := range keys {
			if key == winner {
				continue
			}
			if a.logger != nil {
				a.logger.Info("discovery: collapsed duplicate box at a stale IP (same deviceID as the reachable one)",
					"deviceID", dev, "staleHost", key, "liveHost", winner)
			}
			delete(seen, key)
			dropped = append(dropped, key)
		}
	}
	if len(dropped) == 0 {
		return
	}
	a.discMu.Lock()
	for _, key := range dropped {
		delete(a.discCache, key)
	}
	a.discMu.Unlock()
}

// pickLiveBoxKey chooses which of several host keys for one physical box to keep.
// A currently-reachable IP wins outright; an STR record breaks ties over a stock
// one, and the host string is the final deterministic tiebreaker (seen is a map,
// so keys arrive in random order). If none is reachable — every IP mid-reboot, or
// a transient probe miss — it still returns the best-scored key so the box never
// drops off the list entirely (fallback-first).
func pickLiveBoxKey(seen map[string]BoxInfo, keys []string) string {
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	best := sorted[0]
	bestScore := -1
	for _, key := range sorted {
		b := seen[key]
		score := 0
		if boxKeyReachable(b) {
			score += 2
		}
		if b.Kind == "str" {
			score++
		}
		if score > bestScore {
			bestScore = score
			best = key
		}
	}
	return best
}

// boxKeyReachable reports whether a discovered box answers a TCP connect right
// now, tried on its announced control port first and then the small set of ports
// STR and stock Bose firmware listen on (8888 direct STR, 8090 stock REST, 17008
// the BCO REDIRECT entry). Used only to break a DHCP-move duplicate, which is
// rare, so a short per-port timeout keeps the probe cheap; the first hit wins.
func boxKeyReachable(b BoxInfo) bool {
	if b.Host == "" {
		return false
	}
	tried := map[int]bool{}
	ports := make([]int, 0, 4)
	if b.Port > 0 {
		ports = append(ports, b.Port)
	}
	ports = append(ports, 8888, 8090, 17008)
	for _, p := range ports {
		if p <= 0 || tried[p] {
			continue
		}
		tried[p] = true
		if tcpReachable(b.Host, p, 600*time.Millisecond) {
			return true
		}
	}
	return false
}

// forgetSTRDeviceByHost drops a box's confirmed-STR discovery state so a genuine
// return to stock firmware (an in-app uninstall) reclassifies it as stock instead
// of lingering as STR — both the deviceID identity memory (strKnownTTL) and the
// per-IP sticky cache entry (whose STR record would otherwise re-promote a stock
// sighting via mergeBoxInfo for discoverySTRStickyTTL). Resolves the deviceID from
// the cache for this host and also drops any state still pointing at this host in
// case the box had moved IPs. See strKnown and mergeDiscoveryCache.
func (a *App) forgetSTRDeviceByHost(host string) {
	if host == "" {
		return
	}
	a.discMu.Lock()
	defer a.discMu.Unlock()
	if e, ok := a.discCache[host]; ok && e.box.DeviceID != "" {
		delete(a.strKnown, e.box.DeviceID)
	}
	delete(a.discCache, host)
	for id, e := range a.strKnown {
		if e.box.Host == host {
			delete(a.strKnown, id)
		}
	}
	for key, e := range a.discCache {
		if e.box.Host == host {
			delete(a.discCache, key)
		}
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
	probe := a.probeSTRFn
	if probe == nil {
		probe = probeSTR
	}
	open := a.portOpenFn
	if open == nil {
		open = portOpen
	}
	seen := map[string]BoxInfo{}      // reached this cycle: refresh presence + eviction timer
	offline := map[string]BoxInfo{}   // not reached: keep visible in the grace window, do NOT reset the timer
	presenceOnly := map[string]bool{} // reached on the stock :8090 only: present, but NOT an STR confirmation
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
			probed, probeOK := probe(ctx, kb.Host)
			bosePortOpen := false
			if !probeOK {
				bosePortOpen = open(kb.Host, 8090, 1200)
			}
			b, live, strConfirmed := classifyKnownBox(kb, probed, probeOK, bosePortOpen)
			b = a.enrichBoxWithStockInfo(ctx, b)
			mu.Lock()
			if live {
				seen[b.Host] = b
				if !strConfirmed {
					presenceOnly[b.Host] = true
				}
			} else {
				offline[b.Host] = b
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	a.mergeDiscoveryCacheWith(seen, presenceOnly)
	out := make([]BoxInfo, 0, len(seen)+len(offline))
	for _, b := range seen {
		out = append(out, b)
	}
	for h, b := range offline {
		if _, ok := seen[h]; !ok {
			out = append(out, b)
		}
	}
	a.logger.Info("refresh known boxes done", "count", len(out), "live", len(seen), "offline", len(offline))
	return out, nil
}

// classifyKnownBox is the pure per-box decision behind RefreshKnownBoxes,
// turning one probe outcome into the merge contract fixed in 98883aa:
//
//   - The STR agent answered: live and STR-confirmed, use the fresh record.
//   - Only the stock :8090 answered (agent mid-reboot, or plain stock): the
//     box is on the LAN, so presence refreshes the eviction timer, but it is
//     NOT an STR confirmation (see mergeDiscoveryCacheWith's presenceOnly).
//     Keep the cached record so the tile does not thin out.
//   - Nothing answered: not live. The cached record is still returned so the
//     tile stays visible through the miss-grace window, but callers must NOT
//     feed it back into mergeDiscoveryCache: re-merging an unreachable box
//     every cycle resets its last-seen and is exactly the bug that kept a
//     powered-off Wave listed forever.
func classifyKnownBox(cached, probed BoxInfo, probeOK, bosePortOpen bool) (record BoxInfo, live, strConfirmed bool) {
	if probeOK {
		return probed, true, true
	}
	// Warning flags come from a live agent probe only; while the agent is not
	// answering, a cached warning may be a stale mid-boot snapshot (a "no
	// Wi-Fi saved" banner stuck on a box that was down, #270 2026-07-12).
	// Serve the cached record without them; the next confirmed probe re-sets
	// whatever is really true.
	cached.ConflictingMod = ""
	cached.WLANCredsMissing = false
	cached.Storm1036 = false
	cached.Storm1036SinceSec = 0
	cached.RecallRefusal = false
	cached.RecallRefusalSinceSec = 0
	if bosePortOpen {
		return cached, true, false
	}
	return cached, false, false
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
	// BoxHealth: a fresh verdict wins; an empty one (stock sighting or an
	// older agent) keeps the last known state so the pull-the-plug hint does
	// not flicker between discovery cycles.
	if out.BoxHealth == "" {
		out.BoxHealth = prev.BoxHealth
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

	// Warning flags (ConflictingMod / WLANCredsMissing / BoxHealth): only a
	// live agent probe carries them, so the PortVerified side is
	// authoritative — INCLUDING its no-warning reading, which must clear a
	// stale cached warning (before v0.9.7 these fields were never merged, so
	// a single bad snapshot could stick in the banner, #270). Test against
	// the ORIGINAL flags, same as DeviceID above. With no verified side, keep
	// whichever record has a value.
	switch {
	case a.PortVerified:
		// out already carries a's values verbatim
	case b.PortVerified:
		out.ConflictingMod = b.ConflictingMod
		out.WLANCredsMissing = b.WLANCredsMissing
		out.BoxHealth = b.BoxHealth
		out.Storm1036 = b.Storm1036
		out.Storm1036SinceSec = b.Storm1036SinceSec
		out.RecallRefusal = b.RecallRefusal
		out.RecallRefusalSinceSec = b.RecallRefusalSinceSec
	default:
		if out.ConflictingMod == "" {
			out.ConflictingMod = b.ConflictingMod
		}
		if !out.Storm1036 {
			out.Storm1036 = b.Storm1036
			out.Storm1036SinceSec = b.Storm1036SinceSec
		}
		if !out.RecallRefusal {
			out.RecallRefusal = b.RecallRefusal
			out.RecallRefusalSinceSec = b.RecallRefusalSinceSec
		}
		if !out.WLANCredsMissing {
			out.WLANCredsMissing = b.WLANCredsMissing
		}
		if out.BoxHealth == "" {
			out.BoxHealth = b.BoxHealth
		}
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
			body, err := httpGetSmall(ctx, url, 3*time.Second, 1024)
			if err != nil || !strings.Contains(string(body), `"version"`) {
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
		FriendlyName:   jsonStringField(s, "friendlyName"),
		Model:          jsonStringField(s, "model"),
		BoxHealth:      jsonStringField(s, "boxHealth"),
		ConflictingMod: jsonStringField(s, "conflictingMod"),
		Storm1036:      jsonStringField(s, "preset1036Storm") == "active",
		Storm1036SinceSec: func() int {
			n, _ := strconv.Atoi(jsonStringField(s, "preset1036SinceSec"))
			return n
		}(),
		RecallRefusal: jsonStringField(s, "presetRefusal") == "active",
		RecallRefusalSinceSec: func() int {
			n, _ := strconv.Atoi(jsonStringField(s, "presetRefusalSinceSec"))
			return n
		}(),
		WLANCredsMissing: jsonStringField(s, "wlanCreds") == "missing",
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
	b, ok, _ := probeStockDetail(ctx, ip)
	return b, ok
}

// probeStockDetail is probeStock with the underlying failure preserved, so the
// manual add-by-IP path can log WHY the probe failed (timeout vs. refused vs.
// privacy denial vs. a non-SoundTouch responder). The sweep callers keep the
// cheap two-value form (#420).
func probeStockDetail(ctx context.Context, ip string) (BoxInfo, bool, error) {
	url := fmt.Sprintf("http://%s:8090/info", ip)
	body, err := httpGetSmall(ctx, url, 1200*time.Millisecond, 4096)
	if err != nil {
		return BoxInfo{}, false, err
	}
	s := string(body)
	if !strings.Contains(s, "<info ") || !strings.Contains(s, "deviceID=") {
		return BoxInfo{}, false, fmt.Errorf("%s answered but the reply is not a SoundTouch /info document", url)
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
	}, true, nil
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
	body, err := httpGetSmall(probeCtx, url, 1200*time.Millisecond, 4096)
	if err != nil {
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

// httpGetSmall fetches a small document with a hard timeout. It returns the
// underlying error (client.Do, status, read) instead of a bare bool so probe
// callers can log WHAT failed; a swallowed error made a macOS local-network
// privacy denial indistinguishable from a plain timeout in diagnostics (#420).
func httpGetSmall(ctx context.Context, url string, timeout time.Duration, max int64) ([]byte, error) {
	c, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max))
	if err != nil {
		return nil, err
	}
	return b, nil
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
