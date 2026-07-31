package main

// Cold-start discovery hardening.
//
// Field report (maintainer's own multi-NIC Windows PC, 2026-07-09): on every
// app start the speaker list came up without the Portable and only a manual
// re-scan found it. Two compounding causes:
//
//   - mDNS returns zero instances on that machine (multi-NIC zeroconf
//     flakiness), so everything rides on the TCP fallback sweep.
//   - The sweep walked ALL local /24s through ONE worker pool inside one
//     shared 12 s budget, in interface-enumeration order. Hyper-V/WSL
//     "vEthernet" adapters enumerate before Wi-Fi and their 2x254 dead hosts
//     each hold a worker for the full 3 s probe timeout on a cold neighbor
//     cache - the budget dies before the REAL subnet is reached. A second
//     scan succeeds because Windows' negative neighbor-cache entries now fail
//     those probes instantly. Hence "always missing at start, found on the
//     first manual scan".
//
// Three fixes, layered (compatibility first):
//   1. Every subnet sweeps in PARALLEL with its own worker pool, so a dead
//      virtual subnet can never starve the real one.
//   2. The subnet of the primary outbound route is probed first.
//   3. The speakers seen last time are persisted (known-speakers.json) and
//      probed DIRECTLY at the start of every discovery - a hit needs one
//      3 s probe, no sweep at all.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// knownSpeaker is one persisted speaker from a previous discovery: just
// enough to re-probe it instantly on the next app start. No names or other
// metadata - the live probe re-fetches those.
type knownSpeaker struct {
	Host     string `json:"host"`
	DeviceID string `json:"deviceID,omitempty"`
	Kind     string `json:"kind,omitempty"`
}

// maxKnownSpeakers caps the persisted list; nobody has 16 SoundTouch
// speakers, and the direct-probe phase should stay a handful of sockets.
const maxKnownSpeakers = 16

func knownSpeakersPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ST Reborn", "known-speakers.json"), nil
}

func loadKnownSpeakersFrom(path string) []knownSpeaker {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var list []knownSpeaker
	if json.Unmarshal(b, &list) != nil {
		return nil
	}
	if len(list) > maxKnownSpeakers {
		list = list[:maxKnownSpeakers]
	}
	return list
}

func saveKnownSpeakersTo(path string, list []knownSpeaker) error {
	if len(list) > maxKnownSpeakers {
		list = list[:maxKnownSpeakers]
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// persistKnownSpeakers snapshots the discovery cache into known-speakers.json,
// skipping the write when the host set is unchanged since the last write.
// Best-effort: discovery must never fail on a config-dir hiccup.
func (a *App) persistKnownSpeakers() {
	a.discMu.Lock()
	list := make([]knownSpeaker, 0, len(a.discCache))
	for _, e := range a.discCache {
		if e.box.Host == "" {
			continue
		}
		list = append(list, knownSpeaker{Host: e.box.Host, DeviceID: e.box.DeviceID, Kind: e.box.Kind})
	}
	a.discMu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].Host < list[j].Host })
	fingerprint := ""
	for _, k := range list {
		// Kind is part of the fingerprint so a stock -> str conversion counts as
		// a change: it must re-trigger both the file write and the peer-seed
		// distribution below, even though the host set is identical.
		fingerprint += k.Host + "|" + k.Kind + ";"
	}
	a.knownSpeakersMu.Lock()
	changed := fingerprint != a.knownSpeakersWritten
	a.knownSpeakersWritten = fingerprint
	a.knownSpeakersMu.Unlock()
	if !changed || len(list) == 0 {
		return
	}
	path, err := knownSpeakersPath()
	if err == nil {
		if err := saveKnownSpeakersTo(path, list); err != nil {
			a.logger.Warn("discovery: persist known speakers failed", "err", err)
		}
	}
	// The speaker set changed: distribute it to the fleet so every speaker's
	// on-box picker knows the whole set (sticky picker, 2026-07-26).
	a.distributeKnownSpeakers()
}

// distributeKnownSpeakers pushes the currently known STR speaker set to every
// STR agent (POST /api/peers/seed). Each agent merges the list into its own
// sticky picker store, so a speaker whose local mDNS is lossy still lists the
// whole fleet on its phone page. Best-effort and async: agents predating the
// endpoint answer 404 and are simply unchanged; a speaker that is offline is
// skipped by the timeout. Runs only when the discovered set changed, so it is
// a handful of small POSTs per change, not per discovery cycle.
func (a *App) distributeKnownSpeakers() {
	type seed struct {
		Name string `json:"name"`
		Host string `json:"host"`
		Port int    `json:"port"`
	}
	a.discMu.Lock()
	var seeds []seed
	var targets []seed
	for _, e := range a.discCache {
		b := e.box
		// Offline speakers are not seeded: an agent counts a seed as a live
		// sighting (it un-dims the entry), and the app vouching for a speaker
		// it cannot currently reach would be false evidence.
		if b.Host == "" || b.Kind == "stock" || b.Offline {
			continue
		}
		port := b.Port
		if port == 0 {
			port = 8888
		}
		// Prefer the display name. b.Name is the mDNS instance / "str-<ip>"
		// stand-in for a TCP-fallback find, and it is never empty there, so the
		// old Name-first order seeded the whole fleet with the stand-in even
		// though FriendlyName was already known. On speakers that cannot reach
		// each other directly (live 2026-07-31: the two rhino ST10s), this seed
		// is the ONLY name source, and the placeholder stuck forever.
		name := b.FriendlyName
		if name == "" {
			name = b.Name
		}
		s := seed{Name: name, Host: b.Host, Port: port}
		seeds = append(seeds, s)
		targets = append(targets, s)
	}
	a.discMu.Unlock()
	if len(seeds) < 2 {
		return // with 0-1 STR speakers there is nothing to cross-distribute
	}
	body, err := json.Marshal(seeds)
	if err != nil {
		return
	}
	go func() {
		ok := 0
		for _, t := range targets {
			// boxDo, not a direct POST: mDNS announces port 8888 for every
			// agent, but BCO chassis (Portable/taigan, scm, Wave) are only
			// reachable from outside via :17008 - a direct POST to the
			// announced port silently failed for exactly those boxes (live,
			// 2026-07-26: the Portable never received the seed). boxDo walks
			// the cached/announced/alternate ports the way every other agent
			// call in the app does.
			resp, err := a.boxDo(t.Host, t.Port, "POST", "/api/peers/seed", "application/json", string(body))
			if err != nil {
				a.logger.Warn("discovery: peer-seed push failed", "host", t.Host, "err", err)
				continue
			}
			if resp.StatusCode == http.StatusNoContent {
				ok++
			}
			_ = resp.Body.Close()
		}
		a.logger.Info("discovery: distributed the speaker set to the fleet", "speakers", len(seeds), "delivered", ok)
	}()
}

// probeKnownSpeakers directly probes the speakers persisted from earlier runs
// and streams every hit. This is the cold-start safety net: it needs no mDNS
// and no subnet sweep, so a speaker at its last-known IP appears on the very
// first discovery even when both of those fail or run out of budget.
func probeKnownSpeakers(ctx context.Context, logger *slog.Logger, hits chan<- BoxInfo) {
	defer close(hits)
	path, err := knownSpeakersPath()
	if err != nil {
		return
	}
	known := loadKnownSpeakersFrom(path)
	if len(known) == 0 {
		return
	}
	var wg sync.WaitGroup
	for _, k := range known {
		host := k.Host
		if host == "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			// STR first (the common case for a returning speaker), stock as
			// the fallback so a re-flashed or reset box still appears.
			if b, ok := probeSTR(ctx, host); ok {
				hits <- b
				return
			}
			if b, ok := probeStock(ctx, host); ok {
				hits <- b
			}
		}()
	}
	wg.Wait()
	if logger != nil {
		logger.Debug("discovery: known-speaker direct probes done", "known", len(known))
	}
}

// primaryOutboundIP returns the local IPv4 the OS would use for a default-route
// send (no packet leaves: a UDP "dial" only performs the route lookup). Zero
// IP when there is no route - the caller keeps enumeration order then.
func primaryOutboundIP() net.IP {
	conn, err := net.Dial("udp4", "8.8.8.8:53")
	if err != nil {
		return nil
	}
	defer conn.Close()
	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return addr.IP.To4()
	}
	return nil
}

// orderSubnetsPrimaryFirst moves the subnet containing primary to the front,
// keeping the rest in their original order. The sweep probes subnets in
// parallel anyway, but the primary-first order also makes the sequential
// spawn favor the LAN the speakers actually live on.
func orderSubnetsPrimaryFirst(subnets []string, primary net.IP) []string {
	if primary == nil || len(subnets) < 2 {
		return subnets
	}
	prefix := fmt.Sprintf("%d.%d.%d.", primary[0], primary[1], primary[2])
	for i, s := range subnets {
		if s == prefix && i > 0 {
			reordered := make([]string, 0, len(subnets))
			reordered = append(reordered, s)
			reordered = append(reordered, subnets[:i]...)
			reordered = append(reordered, subnets[i+1:]...)
			return reordered
		}
	}
	return subnets
}

// sweepSubnets probes every host of every subnet, each subnet through its OWN
// worker pool running in parallel with the others. A subnet full of dead
// hosts (Hyper-V/WSL vEthernet on a cold neighbor cache: every probe holds a
// worker for the full timeout) then only wastes its own workers instead of
// starving the real LAN inside the shared discovery budget.
func sweepSubnets(ctx context.Context, subnets []string, probe func(ip string)) {
	const workersPerSubnet = 24
	var outer sync.WaitGroup
	for _, subnet := range subnets {
		base := subnet
		outer.Add(1)
		go func() {
			defer outer.Done()
			sem := make(chan struct{}, workersPerSubnet)
			var wg sync.WaitGroup
			for i := 1; i <= 254; i++ {
				select {
				case <-ctx.Done():
					wg.Wait()
					return
				case sem <- struct{}{}:
				}
				ip := base + fmt.Sprintf("%d", i)
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer func() { <-sem }()
					probe(ip)
				}()
			}
			wg.Wait()
		}()
	}
	outer.Wait()
}
