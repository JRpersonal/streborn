// Peer roster: discovery, NAND persistence, and reachability probing
// of other STR speakers on the LAN.

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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/discovery"
	"github.com/JRpersonal/streborn/internal/webui"
)

// --- Peer discovery for the on-box web UI "Other speakers" section ---

// peerEntry accumulates what we know about one other STR speaker across mDNS
// sweeps, so a peer missed in a single lossy round is not dropped from the list.
type peerEntry struct {
	name string
	// deviceID is the speaker's own identity, which survives an address
	// change. The roster is keyed by IP, so without it a speaker that moves
	// (cable to Wi-Fi, a new DHCP lease) arrives as a brand new entry under a
	// stand-in name while its old entry lingers alongside, and the owner sees
	// both a wrong name and a duplicate until the stale one ages out (#494).
	deviceID  string
	port      int // last web port that answered (0 = never reached)
	lastSeen  time.Time
	reachable bool // answered a web-port probe on the most recent sweep
}

var (
	peersMu       sync.Mutex
	peersByIP     = map[string]*peerEntry{}
	peersBrowseAt time.Time
	peersProbing  bool      // one fallback-probe goroutine at a time
	peersSavedFP  string    // membership fingerprint of the last NAND write
	peersSavedAt  time.Time // last NAND write (rate-limits lastSeen refreshes)
)

// peersStorePath persists the peer list across reboots so the on-box page's
// speaker picker is complete from the first load. Requirement (Jens,
// 2026-07-26): better one speaker too many than one missing because a single
// mDNS round failed - the list only shrinks after peerTTL, never because of a
// momentary lookup failure or a reboot.
const peersStorePath = "/mnt/nv/streborn/peers.json"

// peerDiskEntry is the JSON shape of one persisted peer.
type peerDiskEntry struct {
	IP       string    `json:"ip"`
	Name     string    `json:"name"`
	DeviceID string    `json:"deviceID,omitempty"`
	Port     int       `json:"port"`
	LastSeen time.Time `json:"lastSeen"`
}

// adoptPeerEntryLocked returns the roster entry for ip, moving an existing
// entry over when the SAME speaker has simply changed address.
//
// The roster is keyed by IP, so without this a speaker that moves (cable to
// Wi-Fi, a fresh DHCP lease) arrives as a brand new entry: it shows up under
// the stand-in name it announces until its real name is fetched, while its old
// entry lingers beside it as a stale twin. The owner sees a wrong name and a
// duplicate for as long as that takes (#494, reported again on v0.9.25 after
// the stand-in names themselves were fixed). The device ID survives the
// address change, so the entry follows the speaker and keeps the name that was
// already known.
//
// Caller holds peersMu.
func adoptPeerEntryLocked(ip, deviceID string) *peerEntry {
	e := peersByIP[ip]
	if e == nil {
		if deviceID != "" {
			for oldIP, old := range peersByIP {
				if old.deviceID == deviceID && oldIP != ip {
					e = old
					delete(peersByIP, oldIP)
					break
				}
			}
		}
		if e == nil {
			e = &peerEntry{}
		}
		peersByIP[ip] = e
	}
	if deviceID != "" {
		e.deviceID = deviceID
	}
	return e
}

// loadPersistedPeers seeds peersByIP from NAND at agent start. Entries come
// back dimmed (reachable=false); the browse sweep's fallback probe promotes
// the ones that actually answer within about a minute.
func loadPersistedPeers(logger *slog.Logger) {
	b, err := os.ReadFile(peersStorePath)
	if err != nil {
		return
	}
	var list []peerDiskEntry
	if json.Unmarshal(b, &list) != nil || len(list) == 0 {
		return
	}
	now := time.Now()
	peersMu.Lock()
	n := 0
	for _, d := range list {
		if d.IP == "" || now.Sub(d.LastSeen) > peerTTL {
			continue
		}
		peersByIP[d.IP] = &peerEntry{name: d.Name, deviceID: d.DeviceID, port: d.Port, lastSeen: d.LastSeen}
		n++
	}
	peersMu.Unlock()
	if n > 0 {
		logger.Info("peer list restored from NAND", "count", n)
	}
}

// savePersistedPeersLocked writes the peer list to NAND when its membership
// (IPs, names, ports) changed, or at most every 6 h to refresh lastSeen
// stamps. Rate-limited on purpose: lastSeen moves on every sweep and NAND
// writes are precious. Caller holds peersMu.
func savePersistedPeersLocked(logger *slog.Logger) {
	list := make([]peerDiskEntry, 0, len(peersByIP))
	fp := ""
	for ip, e := range peersByIP {
		list = append(list, peerDiskEntry{IP: ip, Name: e.name, DeviceID: e.deviceID, Port: e.port, LastSeen: e.lastSeen})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].IP < list[j].IP })
	for _, d := range list {
		fp += d.IP + "|" + d.Name + "|" + d.DeviceID + "|" + strconv.Itoa(d.Port) + ";"
	}
	if fp == peersSavedFP && time.Since(peersSavedAt) < 6*time.Hour {
		return
	}
	b, err := json.MarshalIndent(list, "", " ")
	if err != nil {
		return
	}
	tmp := peersStorePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		logger.Debug("peer list persist failed", "err", err)
		return
	}
	if err := os.Rename(tmp, peersStorePath); err != nil {
		logger.Debug("peer list persist rename failed", "err", err)
		return
	}
	peersSavedFP = fp
	peersSavedAt = time.Now()
}

// seedPeers merges a speaker list pushed by the desktop app (POST
// /api/peers/seed) into the peer map, so speakers the local mDNS never
// managed to see still appear in the on-box picker. A seed counts as a
// sighting (the app just saw that speaker); reachability is then confirmed by
// the same async probe the browse sweep uses.
// peerSelfNameFn reports this speaker's own display name, so the roster can
// recognise an entry that is really us. Wired at startup from the mDNS
// announcer, which is the same source the version envelope uses.
var peerSelfNameFn func() string

func seedPeers(seeds []webui.PeerSeed, logger *slog.Logger) {
	if len(seeds) == 0 {
		return
	}
	mine := ownIPv4s()
	now := time.Now()
	peersMu.Lock()
	added := 0
	for _, s := range seeds {
		ip := strings.TrimSpace(s.Host)
		if ip == "" || mine[ip] {
			continue
		}
		e := peersByIP[ip]
		if e == nil {
			e = &peerEntry{}
			peersByIP[ip] = e
			added++
		}
		// Same guard as the mDNS merge: a placeholder seed must never clobber a
		// real name (#494 by the back door).
		if s.Name != "" && (!placeholderPeerName(s.Name) || e.name == "" || placeholderPeerName(e.name)) {
			e.name = s.Name
		}
		if s.Port != 0 && e.port == 0 {
			e.port = s.Port
		}
		if e.lastSeen.Before(now) {
			e.lastSeen = now
		}
		// The app reached this speaker moments ago, or it would not be in the
		// seed. That sighting outranks our own dial probe: two rhino ST10s
		// cannot reach each other's web port at all (live 2026-07-31, every
		// other pairing works), so their sister entries sat dimmed as
		// "str-<ip>" forever even though the PHONE reading the picker reaches
		// both speakers fine. Reachability for the picker means "a client can
		// open this link", and the app just proved that.
		e.reachable = true
	}
	savePersistedPeersLocked(logger)
	peersMu.Unlock()
	if added > 0 {
		logger.Info("peer list seeded by the app", "added", added, "total", len(seeds))
	}
	probeStalePeers(logger)
}

// forgetPeer removes one entry from the sticky picker (POST /api/peers/forget):
// the manual way out for an entry that does not belong there (a mistyped seed,
// a speaker that moved households) without waiting out the 12 h TTL.
func forgetPeer(host string, logger *slog.Logger) bool {
	ip := strings.TrimSpace(host)
	if ip == "" {
		return false
	}
	peersMu.Lock()
	defer peersMu.Unlock()
	if _, ok := peersByIP[ip]; !ok {
		return false
	}
	delete(peersByIP, ip)
	savePersistedPeersLocked(logger)
	logger.Info("peer removed from the sticky picker", "host", ip)
	return true
}

// probeStalePeers re-checks up to eight longest-unseen listed peers over plain
// TCP. This is what keeps the list honest INDEPENDENTLY of mDNS: a speaker
// whose announcements get lost (the exact "missing because one lookup failed"
// complaint) is still confirmed alive by its web port and stays listed and
// clickable; a genuinely gone one stays dimmed until peerTTL expires it. One
// goroutine at a time, a few 300 ms dials per round.
func probeStalePeers(logger *slog.Logger) {
	peersMu.Lock()
	if peersProbing {
		peersMu.Unlock()
		return
	}
	peersProbing = true
	type cand struct {
		ip   string
		seen time.Time
	}
	var stale []cand
	now := time.Now()
	for ip, e := range peersByIP {
		if now.Sub(e.lastSeen) > 30*time.Second {
			stale = append(stale, cand{ip: ip, seen: e.lastSeen})
		}
	}
	peersMu.Unlock()
	sort.Slice(stale, func(i, j int) bool { return stale[i].seen.Before(stale[j].seen) })
	if len(stale) > 8 {
		stale = stale[:8]
	}
	go func() {
		defer func() {
			peersMu.Lock()
			peersProbing = false
			peersMu.Unlock()
		}()
		for _, c := range stale {
			port := reachableWebPort(c.ip)
			if port == 0 {
				continue
			}
			// While we have the speaker on the line anyway, turn a placeholder
			// name into the real one. A roster entry restored from NAND, or one
			// seeded by the app before the speaker was identified, keeps the
			// "str-<ip>" stand-in forever otherwise: the mDNS path never revisits
			// a name it did not just receive, so the owner kept seeing an address
			// where a name belongs (#494, seen live 2026-07-28).
			var friendly string
			peersMu.Lock()
			if e := peersByIP[c.ip]; e != nil && placeholderPeerName(e.name) {
				peersMu.Unlock()
				friendly = peerFriendlyName(c.ip)
				peersMu.Lock()
			}
			if e := peersByIP[c.ip]; e != nil {
				e.lastSeen = time.Now()
				e.reachable = true
				e.port = port
				if friendly != "" {
					e.name = friendly
				}
			}
			peersMu.Unlock()
		}
		if logger != nil {
			logger.Debug("peer fallback probe done", "checked", len(stale))
		}
	}()
}

// browsePeers discovers the other STR speakers on the LAN over mDNS and returns a
// link to each one's web UI, so a phone on the on-box page can hop between
// speakers without re-typing an address.
//
// A single 2.5s mDNS window plus a drop-on-unreachable filter and a 45s wholesale
// cache made each speaker show a DIFFERENT, often incomplete subset (8-box fleets
// saw 5-6; a box that failed one probe vanished for 45s): #404 / disc-381 /
// disc-385. Now the results MERGE into a longer-lived per-IP map: each sweep
// refreshes the peers it sees, a peer stays listed for peerTTL after it was last
// seen (marked offline if it is not currently reachable, so the page can dim it
// rather than drop it), and sweeps are throttled to rebrowseEvery so repeated page
// loads stay cheap. The browse window is widened so more peers answer per round.
// peerTTL is how long a speaker stays in the picker after it was last
// confirmed (mDNS, HTTP fallback probe, or an app seed). Hours on purpose
// (Jens, 2026-07-26): a box in overnight deep standby or behind a flaky mDNS
// round must not vanish; it is shown dimmed until it answers again, and only
// a box unseen for a full peerTTL is dropped.
const peerTTL = 12 * time.Hour

// peerDimAfter is how long after the last confirmation a listed peer renders
// dimmed/non-clickable. Sized against the confirmation cadence (60 s background
// tick, probes in batches of 8) so a live box is always re-confirmed well
// inside the window even in a large fleet, and only genuinely silent boxes dim.
const peerDimAfter = 3 * time.Minute

func browsePeers(ctx context.Context, logger *slog.Logger) []webui.PeerLink {
	const (
		rebrowseEvery = 15 * time.Second
		browseWindow  = 3500 * time.Millisecond
	)
	peersMu.Lock()
	needBrowse := peersBrowseAt.IsZero() || time.Since(peersBrowseAt) >= rebrowseEvery
	peersMu.Unlock()

	if needBrowse {
		bctx, cancel := context.WithTimeout(ctx, browseWindow)
		ch, err := discovery.Browse(bctx, logger)
		mine := ownIPv4s()
		type found struct {
			ip, name, deviceID string
			port               int
		}
		var fresh []found
		if err == nil {
			for inst := range ch {
				if inst.Kind != discovery.KindSTR {
					continue // only STR speakers, not stock Bose
				}
				ip, self := "", false
				for _, a := range inst.IPv4 {
					if mine[a] {
						self = true
						break
					}
					if ip == "" {
						ip = a
					}
				}
				if self || ip == "" {
					continue
				}
				name := inst.FriendlyName
				if name == "" {
					name = inst.Name
				}
				if placeholderPeerName(name) {
					// mDNS sometimes carries only the instance name, which is the
					// "str-<ip>" placeholder the app also uses before a speaker is
					// identified. Rendering that in the picker is the same defect as
					// rendering a bare address (#494): the owner sees an address where
					// a name belongs. The speaker knows its own name, so ask it.
					if friendly := peerFriendlyName(ip); friendly != "" {
						name = friendly
					}
				}
				fresh = append(fresh, found{ip: ip, name: name, deviceID: inst.DeviceID, port: reachableWebPort(ip)})
			}
		} else {
			logger.Debug("peers browse failed", "err", err)
		}
		cancel()

		peersMu.Lock()
		now := time.Now()
		for _, f := range fresh {
			e := adoptPeerEntryLocked(f.ip, f.deviceID)
			// Never let a placeholder overwrite a name we already know: mDNS can
			// answer with the instance name only, and replacing "Kitchen" with
			// "str-192.0.2.5" is the #494 defect arriving by the back door.
			if f.name != "" && (!placeholderPeerName(f.name) || e.name == "" || placeholderPeerName(e.name)) {
				e.name = f.name
			}
			e.lastSeen = now
			e.reachable = f.port != 0
			if f.port != 0 {
				e.port = f.port
			}
		}
		peersBrowseAt = now
		savePersistedPeersLocked(logger)
		peersMu.Unlock()
		// Independently of mDNS, re-confirm the longest-unseen listed peers over
		// TCP so a speaker with lost announcements stays in the picker (async).
		probeStalePeers(logger)
	}

	selfName := ""
	if peerSelfNameFn != nil {
		selfName = strings.TrimSpace(peerSelfNameFn())
	}
	mineNow := ownIPv4s()
	peersMu.Lock()
	defer peersMu.Unlock()
	now := time.Now()
	// One physical speaker can sit in the map under two addresses after a DHCP
	// lease change: the sticky roster keeps the old entry until its TTL while
	// the new one is already live, so the phone page listed the same speaker
	// twice, once of them nameless (#494). Keep only the freshest entry per
	// name, and prefer a reachable one over a dimmed one.
	bestByName := map[string]string{} // display name -> ip of the entry to keep
	for ip, e := range peersByIP {
		if e.name == "" || now.Sub(e.lastSeen) > peerTTL {
			continue
		}
		cur, ok := bestByName[e.name]
		if !ok {
			bestByName[e.name] = ip
			continue
		}
		prev := peersByIP[cur]
		if e.reachable != prev.reachable {
			if e.reachable {
				bestByName[e.name] = ip
			}
			continue
		}
		if e.lastSeen.After(prev.lastSeen) {
			bestByName[e.name] = ip
		}
	}
	out := make([]webui.PeerLink, 0, len(peersByIP))
	for ip, e := range peersByIP {
		if now.Sub(e.lastSeen) > peerTTL {
			delete(peersByIP, ip)
			continue
		}
		if mineNow[ip] {
			delete(peersByIP, ip) // one of our own current addresses
			continue
		}
		// A speaker whose name we never learned would render as its bare IP
		// address, which reads like a broken entry next to the named ones
		// (#494). Skip it: mDNS or the next app seed supplies the name shortly,
		// and an unnamed duplicate helps nobody.
		if e.name == "" {
			continue
		}
		// Never list ourselves. The mDNS and seed paths already compare against
		// our CURRENT addresses, but neither can catch an entry left behind at an
		// address we used to have: after a new DHCP lease the old one is no longer
		// "ours", so the speaker kept offering a link to itself under its own name
		// until the entry aged out (seen live on an ST30, 2026-07-28). Our own
		// name is the one signal that survives an address change.
		if selfName != "" && strings.EqualFold(e.name, selfName) {
			delete(peersByIP, ip)
			continue
		}
		if keep, ok := bestByName[e.name]; ok && keep != ip {
			continue // an entry for the same speaker at a fresher address wins
		}
		port := e.port
		if port == 0 {
			port = 8888 // never reached yet: best-effort URL so the entry still resolves once it comes up
		}
		out = append(out, webui.PeerLink{
			Name: e.name,
			URL:  fmt.Sprintf("http://%s:%d/", ip, port),
			// Carried so the phone can put this speaker into a group: the
			// firmware names zone members by device, and this entry already
			// holds the id from the speaker's mDNS record.
			DeviceID: e.deviceID,
			IP:       ip,
			// A peer confirmed within peerDimAfter is clickable; anything older
			// stays listed (sticky by design) but dims until it answers again.
			Reachable: e.reachable && now.Sub(e.lastSeen) <= peerDimAfter,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].URL < out[j].URL
	})
	return out
}

// ownIPv4s returns this speaker's own LAN IPv4 addresses, used to drop the
// speaker itself from the discovered peer list.
func ownIPv4s() map[string]bool {
	m := map[string]bool{}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			if v4 := ipn.IP.To4(); v4 != nil {
				m[v4.String()] = true
			}
		}
	}
	return m
}

// reachableWebPort returns a peer's externally reachable web port: STR's direct
// webui port (8888 on sm2/rhino/mojo) or the BCO REDIRECT port (17008 on
// taigan/scm), whichever accepts a connection; 0 when neither answers. This
// probe is why no per-model port has to be carried in mDNS.
// placeholderPeerName reports whether a discovered name is the "str-<ip>"
// stand-in rather than something a person chose.
func placeholderPeerName(name string) bool {
	rest, ok := cutPrefixFold(name, "str-")
	if !ok {
		return false
	}
	// "str-<ip>" is what the app calls a speaker it has not identified yet.
	if net.ParseIP(rest) != nil {
		return true
	}
	// "STR-3E6CE1" is what a speaker announces over mDNS when its friendly name
	// is not available: the tail of its device ID, which is its MAC. Users read
	// that as gibberish, and one reported seeing "part of their MAC addresses"
	// in the picker (#494, 2026-07-28). Hex-only and at least four characters,
	// so a real name that merely starts with "str-" is left alone.
	if len(rest) < 4 {
		return false
	}
	for _, r := range rest {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

// cutPrefixFold is strings.CutPrefix, case-insensitively: the placeholder
// arrives as "str-" from the app and "STR-" from mDNS.
func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	return s[len(prefix):], true
}

// peerFriendlyName asks a peer what it calls itself. The agent's version
// envelope carries friendlyName, so one short request turns a placeholder into
// the name on the speaker. Best effort and tightly bounded: this runs inside a
// discovery sweep, and a silent peer must not hold it up.
func peerFriendlyName(ip string) string {
	port := reachableWebPort(ip)
	if port == 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://%s:%d/api/agent/version", ip, port), nil)
	if err != nil {
		return ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var out struct {
		FriendlyName string `json:"friendlyName"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&out); err != nil {
		return ""
	}
	return strings.TrimSpace(out.FriendlyName)
}

func reachableWebPort(ip string) int {
	for _, p := range []int{8888, 17008} {
		if dialable(ip, p) {
			return p
		}
	}
	return 0
}

func dialable(ip string, port int) bool {
	// One fast attempt, then one patient retry. A speaker in Wi-Fi power save
	// (an idle rhino ST10 wakes its radio per DTIM beacon) can take longer
	// than 300 ms to move a first SYN in either direction, and a single tight
	// attempt then reads a healthy speaker as permanently unreachable
	// (fleet comparison 2026-07-31). The slow retry only runs when the fast
	// path failed, so the common case stays cheap.
	for _, tmo := range []time.Duration{300 * time.Millisecond, 1200 * time.Millisecond} {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", ip, port), tmo)
		if err == nil {
			_ = c.Close()
			return true
		}
	}
	return false
}
