package main

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// The sticky discovery cache: OTA and stock pins, the per-cycle cache merge with its STR identity memory, the DHCP-move dedupe, and RefreshKnownBoxes. Split out of app_discovery.go.

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
	// Which speaker the pin is for, so it can step aside when that speaker
	// turns up at a new address or another one answers at this address.
	if e, ok := a.discCache[host]; ok && e.box.DeviceID != "" {
		if a.otaPinnedID == nil {
			a.otaPinnedID = map[string]string{}
		}
		a.otaPinnedID[host] = e.box.DeviceID
	} else {
		delete(a.otaPinnedID, host)
	}
	// An install or update the app itself runs on the host outranks a stock
	// pin from an earlier uninstall: STR is going back on, so the box must
	// not be corrected to stock while its agent restarts.
	delete(a.stockPinned, host)
	a.discMu.Unlock()
	a.logger.Info("post-OTA: pinning box as STR through its reboot", "host", host, "grace", otaRebootGrace.String())
}

// clearPostOTA drops the post-OTA pin for host again. Called when the update
// attempt FAILED before anything reached the box: the pin's premise (agent
// restarting on the new binary) is false then, and leaving it live kept the
// box annotated as mid-update for the whole grace window (#775).
func (a *App) clearPostOTA(host string) {
	if host == "" {
		return
	}
	a.discMu.Lock()
	a.dropOTAPinLocked(host)
	a.discMu.Unlock()
}

// dropOTAPinLocked removes the post-OTA pin for host. Caller holds discMu.
func (a *App) dropOTAPinLocked(host string) {
	delete(a.otaPinned, host)
	delete(a.otaPinnedID, host)
}

// differentDevices reports whether two records provably describe two
// different speakers: both carry a DeviceID, and neither of one's ids names
// the other. Records without an id are never called different, because stock
// Bose mDNS sometimes omits it.
func differentDevices(a, b BoxInfo) bool {
	if a.DeviceID == "" || b.DeviceID == "" {
		return false
	}
	return !boxIsDevice(b, a.DeviceID) && (a.BoxDeviceID == "" || !boxIsDevice(b, a.BoxDeviceID))
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
	// Before the cache re-adds anything, seen holds only this cycle's genuine
	// sightings: the right moment to notice a box that came back on the new
	// build after its update's verify window had already given up, and to
	// write the journal line that says so (otaverify.go). presenceOnly hosts
	// are excluded there: for those, seen carries the CACHED record (old
	// build) because only the stock :8090 answered, not a sighting.
	a.confirmLateOTA(seen, presenceOnly)
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
		prev, cached := a.discCache[key]
		if cached && differentDevices(prev.box, b) {
			// Another speaker answers at this address now: a router that hands
			// out new leases on every reboot gave it this one's address. Its
			// record must not inherit the old speaker's name, model or agent
			// state, and the old speaker turns up at its own new address
			// (field case 2026-10-26: one speaker's record was merged into
			// another's at the address it took).
			if a.logger != nil {
				a.logger.Info("discovery: another speaker answers at this address now; not merging it into the old speaker's record",
					"host", key, "oldDeviceID", prev.box.DeviceID, "deviceID", b.DeviceID)
			}
			cached = false
		}
		if cached {
			b = mergeBoxInfo(prev.box, b)
		}
		// A genuine sighting always clears the offline marker, whatever the
		// merge carried over from the cached record.
		b.Offline = false
		b.OfflineSinceSec = 0
		e := discEntry{box: b, seen: now}
		// Agent-silence streak: the box counts as STR (cached, or promoted by
		// the merge) but only its stock :8090 answered this cycle. A confirmed
		// agent sighting resets the streak; a long enough streak degrades the
		// record to "STR not running" (field case 2026-09-06: after an
		// uninstall the stock speaker kept its old agent version badge for
		// as long as the app ran, because every refresh saw :8090 answer and
		// served the cached STR record). A box mid-OTA is excluded: its
		// reboot is covered by the pin below, which outlasts this window.
		if presenceOnly[key] && cached && b.Kind == "str" {
			e.strMisses = prev.strMisses + 1
			e.strMissSince = prev.strMissSince
			if e.strMissSince.IsZero() {
				e.strMissSince = now
			}
			if _, pinned := a.otaPinned[key]; !pinned &&
				e.strMisses >= strGoneMisses && now.Sub(e.strMissSince) >= strGoneMinSpan {
				b = degradeToSTRNotRunning(b)
				e = discEntry{box: b, seen: now}
				delete(a.strKnown, b.DeviceID)
				if a.logger != nil {
					// Says only what was measured. The old wording asserted that the
					// stock Bose port answered, and the path that reaches here never
					// contacts :8090: an mDNS announcement with no successful probe
					// qualifies, which is also what a speaker that is switched off
					// looks like while the host OS still serves its cached
					// announcement. Telling that owner to reinstall STR is wrong.
					a.logger.Info("discovery: STR agent has not answered for the full window; box degraded to STR not running",
						"host", b.Host, "misses", prev.strMisses+1, "since", prev.strMissSince.Format(time.RFC3339))
				}
			}
		}
		seen[key] = b
		a.discCache[key] = e
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
	// Addresses evicted below because their speaker turned up at a new one.
	// The post-OTA pin must not bring them back (see the pin loop).
	movedAway := map[string]bool{}
	// Re-add boxes the current cycle missed; evict after a full grace window
	// of consecutive misses.
	for key, e := range a.discCache {
		if _, ok := seen[key]; ok {
			continue
		}
		// Same physical box found at a NEW IP this cycle -> this cached record is its
		// dead old IP. Carry its display identity over to the live record first:
		// a speaker that comes back from an install/reboot on a fresh DHCP lease
		// has no cache entry at the new IP, so the fresh record arrives thin
		// (agent up, name not readable yet) and deleting the old record with it
		// threw the name memory away. The tile then renamed to the technical
		// fallback ("STR-xxxxxx" / "str-<ip>") until the box could serve its
		// name again (#709, triple-ST10 install). Then evict the dead IP so the
		// speaker shows once, at its live address.
		if e.box.DeviceID != "" {
			if liveHost, moved := freshDevices[e.box.DeviceID]; moved && liveHost != key {
				if cur, ok := a.discCache[liveHost]; ok {
					cur.box = mergeBoxInfo(e.box, cur.box)
					a.discCache[liveHost] = cur
					if _, inSeen := seen[liveHost]; inSeen {
						seen[liveHost] = cur.box
					}
				}
				delete(a.discCache, key)
				movedAway[key] = true
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
			a.dropOTAPinLocked(host)
			continue
		}
		pinID := a.otaPinnedID[host]
		// The speaker this pin was for came back at a NEW address this cycle,
		// and the eviction above already removed its old one. Re-creating a
		// record there left a nameless entry that nothing could evict again,
		// because it carried no deviceID, and it stayed in the list for up to
		// a day as a speaker "with an address but no data" (field case
		// 2026-10-26, a fleet update on a router that re-leases on every
		// reboot). The pin's job is done: the speaker is visible, as STR, at
		// its new address.
		if movedAway[host] {
			a.dropOTAPinLocked(host)
			if a.logger != nil {
				a.logger.Info("post-OTA: the speaker came back at a new address; dropping the pin on its old one", "host", host)
			}
			continue
		}
		if live, ok := freshDevices[pinID]; pinID != "" && ok && live != host {
			a.dropOTAPinLocked(host)
			if a.logger != nil {
				a.logger.Info("post-OTA: the speaker came back at a new address; dropping the pin on its old one", "host", host, "newHost", live)
			}
			continue
		}
		b, ok := seen[host]
		if ok && pinID != "" && b.DeviceID != "" && !boxIsDevice(b, pinID) {
			// Another speaker answers at the pinned address now. Its own record
			// stands; the pin was for a speaker that is no longer here.
			a.dropOTAPinLocked(host)
			if a.logger != nil {
				a.logger.Info("post-OTA: another speaker answers at the pinned address; dropping the pin", "host", host, "deviceID", b.DeviceID)
			}
			continue
		}
		synthesized := false
		if !ok {
			// Not visible this cycle (mid-reboot): keep the last-known record, or
			// synthesise a minimal STR one so the box neither vanishes nor gets
			// offered for reinstall.
			if e, cached := a.discCache[host]; cached {
				b = e.box
			} else {
				synthesized = true
				b = BoxInfo{Host: host, Port: 8888, DeviceID: pinID}
				if memo, known := a.strKnown[pinID]; pinID != "" && known {
					// The speaker's last confirmed record keeps its name and
					// model on the tile while it restarts.
					b = memo.box
					b.Host = host
					b.Offline = false
					b.OfflineSinceSec = 0
				}
			}
		}
		b.Kind = "str"
		b.STRNotRunning = false
		// Cache the record with the STR kind but WITHOUT any version claim,
		// and annotate only the served copy. The old code stamped the APP's
		// version (plus build) here and persisted it, which falsified the
		// displayed version of a box the update never reached and outlived
		// the pin through the cache (#775: unreachable speakers showed the
		// target version, boxNeedsUpdate went false fleet-wide, a retry
		// toasted "already up to date"). The anti-flap purpose (no spurious
		// "update available" while the agent restarts) now rides on the
		// transient OTAPending flag, which the frontend's boxNeedsUpdate
		// honours; the version shown stays the box's last confirmed
		// self-report. Same annotate-only pattern as Offline below.
		//
		// A record made up here without a deviceID is served, never cached:
		// once cached, it could not be tied to its speaker again, so nothing
		// evicted it when the speaker turned up at another address.
		if !synthesized || b.DeviceID != "" {
			a.discCache[host] = discEntry{box: b, seen: now}
		}
		b.OTAPending = true
		seen[host] = b
	}

	// Post-uninstall pin: the mirror image of the OTA pin above. This app just
	// removed STR from the host, so it is a stock Bose speaker until an agent
	// answers a live probe again. Any STR label that arrives without a verified
	// agent port behind it inside the grace (a stale mDNS announcement the OS
	// still serves, or the merge's own STR promotion of a stock :8090 sighting)
	// is corrected to stock here, BEFORE the identity memory below could
	// re-record the box as STR. A verified agent sighting means a reinstall:
	// the pin is lifted and the STR record stands.
	for host, t := range a.stockPinned {
		if now.Sub(t) > stockPinGrace {
			delete(a.stockPinned, host)
			continue
		}
		b, ok := seen[host]
		if !ok {
			continue
		}
		if _, updating := a.otaPinned[host]; updating {
			// notePostOTA drops the stock pin, so this only guards a pin that
			// arrived between the two; the OTA pin above already won.
			delete(a.stockPinned, host)
			continue
		}
		if b.Kind == "str" && b.PortVerified {
			delete(a.stockPinned, host)
			if a.logger != nil {
				a.logger.Info("discovery: STR agent answered on a box STR was removed from; treating it as reinstalled", "host", host)
			}
			continue
		}
		if b.Kind == "stock" && !b.STRNotRunning && b.Version == "" {
			continue
		}
		b = degradeToSTRNotRunning(b)
		b.STRNotRunning = false
		seen[host] = b
		e := a.discCache[host]
		e.box = b
		e.seen = now
		a.discCache[host] = e
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
		if _, pinned := a.stockPinned[key]; pinned {
			// STR was just removed from this host by the app: a stock
			// sighting is the truth, not a misclassification.
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
//
// The same collapse covers a speaker on the cable AND on Wi-Fi (#165): it
// answers on two live addresses at once, and both carry the same deviceID. The
// agent's own address report (BoxInfo.LANAddrs) links the two as well, which
// matters where one sighting carries the firmware id and the other the agent's
// radio MAC. pickLiveBoxKey then keeps the cable address while it answers.
func (a *App) dedupeByDeviceID(seen map[string]BoxInfo) {
	var dropped []string
	for _, keys := range sameSpeakerGroups(seen) {
		winner := pickLiveBoxKey(seen, keys)
		for _, key := range keys {
			if key == winner {
				continue
			}
			if a.logger != nil {
				a.logger.Info("discovery: collapsed duplicate box (same speaker at another address)",
					"deviceID", seen[key].DeviceID, "droppedHost", key, "keptHost", winner,
					"keptWired", hostIsWired(seen, keys, winner))
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

// sameSpeakerGroups returns the host keys of seen that belong to one physical
// speaker, one slice per speaker with at least two keys, each sorted. Two keys
// are the same speaker when they share a non-empty DeviceID, or when one
// record's own address report (LANAddrs, from the agent) names the other's
// host. Records with neither stay alone: stock mDNS sometimes omits the MAC,
// and grouping on anything weaker would merge distinct speakers.
func sameSpeakerGroups(seen map[string]BoxInfo) [][]string {
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parent := make(map[string]string, len(keys))
	var find func(string) string
	find = func(k string) string {
		if parent[k] == k {
			return k
		}
		r := find(parent[k])
		parent[k] = r
		return r
	}
	union := func(x, y string) {
		rx, ry := find(x), find(y)
		if rx != ry {
			parent[ry] = rx
		}
	}
	byDevice := map[string]string{}
	byHost := map[string]string{}
	for _, k := range keys {
		parent[k] = k
		if h := seen[k].Host; h != "" {
			byHost[h] = k
		}
		byHost[k] = k
	}
	for _, k := range keys {
		b := seen[k]
		if b.DeviceID != "" {
			if first, ok := byDevice[b.DeviceID]; ok {
				union(first, k)
			} else {
				byDevice[b.DeviceID] = k
			}
		}
		for _, addr := range splitAddrList(b.LANAddrs) {
			if other, ok := byHost[addr]; ok && other != k {
				union(k, other)
			}
		}
	}
	groups := map[string][]string{}
	for _, k := range keys {
		r := find(k)
		groups[r] = append(groups[r], k)
	}
	var out [][]string
	for _, g := range groups {
		if len(g) > 1 {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

// splitAddrList parses the agent's comma-joined address list.
func splitAddrList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// hostIsWired reports whether any record of the group says that key's host is
// on the speaker's network cable. Any record, because the agent sends the same
// report on every address it answers on, and the record at the cable address
// may be the thinner one this cycle.
func hostIsWired(seen map[string]BoxInfo, group []string, key string) bool {
	host := seen[key].Host
	if host == "" {
		host = key
	}
	for _, k := range group {
		for _, w := range splitAddrList(seen[k].WiredAddrs) {
			if w == host {
				return true
			}
		}
	}
	return false
}

// boxKeyReachableFn is the reachability probe pickLiveBoxKey uses; a variable
// so the tests can decide which address answers.
var boxKeyReachableFn = boxKeyReachable

// pickLiveBoxKey chooses which of several host keys for one physical box to keep.
// A currently-reachable IP wins outright; among reachable ones the speaker's
// cable address beats its Wi-Fi address (#165: steadier, and the one a
// speaker with both is meant to be used on); an STR record breaks ties over a
// stock one, and the host string is the final deterministic tiebreaker (seen is
// a map, so keys arrive in random order). Reachability outranks the cable, so a
// cable address that stopped answering hands over to the Wi-Fi one. If none is
// reachable — every IP mid-reboot, or a transient probe miss — it still returns
// the best-scored key so the box never drops off the list entirely
// (fallback-first).
func pickLiveBoxKey(seen map[string]BoxInfo, keys []string) string {
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	best := sorted[0]
	bestScore := -1
	for _, key := range sorted {
		b := seen[key]
		score := 0
		if boxKeyReachableFn(b) {
			score += 4
		}
		if hostIsWired(seen, sorted, key) {
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

// markHostStock records that this app just removed STR from host: the box is a
// stock Bose speaker now. Unlike forgetSTRDeviceByHost, which only DROPPED the
// cached record and left the next discovery to rediscover the box from scratch,
// this rewrites the cached record in place as the stock speaker it has become
// (name, model, deviceID, serial and host kept; agent version, build and port
// gone), so the speaker list shows it as a box without STR right away and the
// periodic refresh cannot serve the old agent version from the cache while the
// stock :8090 answers. The deviceID identity memory is dropped as before, the
// OTA pin (if any) too, and the host is pinned stock for stockPinGrace so a
// stale announcement cannot relabel it (see App.stockPinned).
func (a *App) markHostStock(host string) {
	if host == "" {
		return
	}
	a.discMu.Lock()
	defer a.discMu.Unlock()
	now := time.Now()
	var rec BoxInfo
	have := false
	for key, e := range a.discCache {
		if key != host && e.box.Host != host {
			continue
		}
		if e.box.DeviceID != "" {
			delete(a.strKnown, e.box.DeviceID)
		}
		if !have || key == host {
			rec = e.box
			have = true
		}
		delete(a.discCache, key)
	}
	for id, e := range a.strKnown {
		if e.box.Host == host {
			delete(a.strKnown, id)
		}
	}
	a.dropOTAPinLocked(host)
	if a.discCache == nil {
		a.discCache = map[string]discEntry{}
	}
	if have {
		rec.Host = host
		rec = degradeToSTRNotRunning(rec)
		// A box the app itself just returned to stock is an ordinary stock
		// speaker, not one whose agent went missing: plain "Ready for STR".
		rec.STRNotRunning = false
		rec.Offline = false
		rec.OfflineSinceSec = 0
		a.discCache[host] = discEntry{box: rec, seen: now}
	}
	if a.stockPinned == nil {
		a.stockPinned = map[string]time.Time{}
	}
	a.stockPinned[host] = now
	if a.logger != nil {
		a.logger.Info("discovery: box marked stock after STR removal", "host", host, "hadRecord", have, "grace", stockPinGrace.String())
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
	// The live probe prefers the port each box was last verified on, so a
	// speaker that answers on both :8888 and :17008 keeps its port from one
	// refresh to the next instead of flipping (#1190).
	probe := a.probeSTRFn
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
			var probed BoxInfo
			var probeOK bool
			if probe != nil {
				probed, probeOK = probe(ctx, kb.Host)
			} else {
				probed, probeOK = probeSTRPreferring(ctx, kb.Host, knownAgentPort(kb))
			}
			// The address the tile holds is silent, but the speaker said it
			// also answers elsewhere (cable and Wi-Fi at once, #165): try those
			// before calling it unreachable. The merge below then moves the
			// tile to the address that answered and evicts the silent one, and
			// the next full discovery moves it back to the cable once that
			// answers again.
			if !probeOK && kb.Kind == "str" {
				for _, alt := range splitAddrList(kb.LANAddrs) {
					if alt == kb.Host || ctx.Err() != nil {
						continue
					}
					var altBox BoxInfo
					var altOK bool
					if probe != nil {
						altBox, altOK = probe(ctx, alt)
					} else {
						altBox, altOK = probeSTRPreferring(ctx, alt, knownAgentPort(kb))
					}
					if altOK {
						if a.logger != nil {
							a.logger.Info("refresh: speaker silent at its address, answered at its other one",
								"silentHost", kb.Host, "answeredHost", alt, "deviceID", kb.DeviceID)
						}
						probed, probeOK = altBox, true
						// Keep the identity the tile had, so the merge can tie
						// the new address to it even when the probe came back
						// thin (no :8090 /info answer yet).
						if probed.DeviceID == "" {
							probed.DeviceID = kb.DeviceID
						}
						break
					}
				}
			}
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
	// Count what this cycle REACHED before merging, because the merge writes
	// into seen. The log below used to read len(seen) afterwards and call it
	// "live", so a three-speaker fleet with one speaker asleep reported
	// "count=3 live=3 offline=1": three speakers, three of them live, and one
	// offline as well. That reads as a fourth speaker appearing from nowhere,
	// and it is how an evening went looking for a phantom cache entry that does
	// not exist (2026-09-28). Two reached, one did not.
	reached := len(seen)
	a.mergeDiscoveryCacheWith(seen, presenceOnly)
	out := make([]BoxInfo, 0, len(seen)+len(offline))
	for h, b := range seen {
		// Served copy only, never cached: this cycle the stock :8090 answered
		// but the agent did not, on a box still counted as STR. Settings uses
		// it to say "answers its Bose firmware, STR is not running" instead of
		// "unplug the speaker".
		if presenceOnly[h] && b.Kind == "str" {
			b.STRSilent = true
		}
		out = append(out, b)
	}
	for h, b := range offline {
		if _, ok := seen[h]; !ok {
			out = append(out, b)
		}
	}
	a.logger.Info("refresh known boxes done",
		"count", len(out), "reached", reached, "fromCache", len(seen)-reached,
		"unreachable", len(offline))
	return out, nil
}

// knownAgentPort is the agent port a cached record was verified on, or 0
// (no preference) when the record carries none.
func knownAgentPort(kb BoxInfo) int {
	if kb.Kind == "str" && kb.PortVerified {
		return kb.Port
	}
	return 0
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
	cached.ForeignCloudURL = ""
	cached.Internet = ""
	cached.InternetReason = ""
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
