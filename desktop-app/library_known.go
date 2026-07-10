package main

// Known-server memory for the Library tab (#341).
//
// SSDP alone leaves a cold-start blind window: the passive NOTIFY
// listener only helps once the server happens to re-announce (up to
// its max-age, commonly 30 minutes), and a server on the SAME PC never
// answers the M-SEARCH sweep on Windows. So every server the app has
// EVER successfully described is remembered ({udn, location,
// friendlyName}) in known-servers.json next to media-servers.json, and
// every scan re-probes the remembered locations directly in parallel
// with the SSDP sweep. A remembered server that is off right now is
// simply not listed (unlike a user-pinned manual server); its entry is
// kept so it reappears instantly on the next scan after it comes back.
//
// The same scan also probes the well-known description endpoints
// (wellKnownDescriptionEndpoints in library_manual.go) against
// 127.0.0.1 and each of this host's own interface addresses, which
// finds a just-installed same-PC server on the very first scan with no
// announcement heard at all.

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/dlna"
)

// knownMediaServer is one persisted discovery success. LastSeen exists
// only to pick eviction victims when the file exceeds knownServersCap;
// liveness is always decided by the describe probe, never by age.
type knownMediaServer struct {
	UDN          string    `json:"udn"`
	Location     string    `json:"location"`
	FriendlyName string    `json:"friendlyName"`
	LastSeen     time.Time `json:"lastSeen"`
}

// knownServersCap bounds the file so a LAN with churning DHCP leases
// cannot grow it (and the per-scan probe fan-out) without limit.
const knownServersCap = 64

var knownServersMu sync.Mutex

func knownServersPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ST Reborn", "known-servers.json"), nil
}

func readKnownServers() []knownMediaServer {
	path, err := knownServersPath()
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var list []knownMediaServer
	_ = json.Unmarshal(b, &list)
	return list
}

func writeKnownServers(list []knownMediaServer) error {
	path, err := knownServersPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(list, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// mergeKnownServers upserts this scan's live responders into the
// persisted list, keyed by UDN, and evicts the longest-unseen entries
// past knownServersCap. Pure so the merge policy is unit-testable.
func mergeKnownServers(list []knownMediaServer, servers []dlna.Server, now time.Time) []knownMediaServer {
	byUDN := make(map[string]int, len(list))
	for i, k := range list {
		byUDN[k.UDN] = i
	}
	for _, s := range servers {
		if s.UDN == "" || s.Location == "" {
			continue
		}
		entry := knownMediaServer{
			UDN:          s.UDN,
			Location:     s.Location,
			FriendlyName: s.FriendlyName,
			LastSeen:     now,
		}
		if i, ok := byUDN[s.UDN]; ok {
			list[i] = entry
			continue
		}
		byUDN[s.UDN] = len(list)
		list = append(list, entry)
	}
	if len(list) > knownServersCap {
		sort.Slice(list, func(i, j int) bool { return list[i].LastSeen.After(list[j].LastSeen) })
		list = list[:knownServersCap]
	}
	return list
}

// rememberKnownServers persists this scan's successfully described
// servers for future cold starts. Best-effort: a failed write only
// costs the shortcut, never the scan result.
func (a *App) rememberKnownServers(servers []dlna.Server) {
	if len(servers) == 0 {
		return
	}
	knownServersMu.Lock()
	defer knownServersMu.Unlock()
	list := mergeKnownServers(readKnownServers(), servers, time.Now())
	if err := writeKnownServers(list); err != nil {
		a.logger.Warn("library: could not persist known media servers", "err", err.Error())
	}
}

// probeKnownServers re-describes every remembered server location in
// parallel and returns the ones that answer right now. This is what
// removes the cold-start blind window: a server that neither answered
// this M-SEARCH nor announced since the app started still lists,
// because a previous run proved where its description lives.
func (a *App) probeKnownServers(ctx context.Context) []dlna.Server {
	knownServersMu.Lock()
	list := readKnownServers()
	knownServersMu.Unlock()
	if len(list) == 0 {
		return nil
	}
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	results := make([]dlna.Server, len(list))
	live := make([]bool, len(list))
	var wg sync.WaitGroup
	for i, k := range list {
		if k.Location == "" {
			continue
		}
		wg.Add(1)
		go func(i int, loc string) {
			defer wg.Done()
			if s, err := dlna.DescribeServer(pctx, loc); err == nil {
				results[i], live[i] = s, true
			}
		}(i, k.Location)
	}
	wg.Wait()
	out := make([]dlna.Server, 0, len(list))
	for i := range results {
		if live[i] {
			out = append(out, results[i])
		}
	}
	return out
}

// probeWellKnownLocalServers checks the well-known description
// endpoints against this host's own addresses (loopback plus every
// interface IPv4). A media server on the SAME PC as the app is the
// #341 failure mode: it does not answer same-host M-SEARCH on Windows
// and its NOTIFY may be half an hour away, but its description URL is
// trivially reachable. A cheap TCP dial gates each candidate so the
// (common) closed ports cost milliseconds and no failed-fetch log
// noise; only listening ports get the real describe.
func (a *App) probeWellKnownLocalServers(ctx context.Context) []dlna.Server {
	hosts := append([]string{"127.0.0.1"}, localIPv4Addresses()...)
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var mu sync.Mutex
	var out []dlna.Server
	var wg sync.WaitGroup
	for _, host := range hosts {
		for _, e := range wellKnownDescriptionEndpoints {
			wg.Add(1)
			go func(host, port, path string) {
				defer wg.Done()
				portN, err := strconv.Atoi(port)
				if err != nil || !portOpen(host, portN, 400) {
					return
				}
				loc := "http://" + net.JoinHostPort(host, port) + path
				if s, err := dlna.DescribeServer(pctx, loc); err == nil {
					mu.Lock()
					out = append(out, s)
					mu.Unlock()
				}
			}(host, e.port, e.path)
		}
	}
	wg.Wait()
	return out
}

// localIPv4Addresses returns this host's own non-loopback IPv4
// addresses. Unlike localIPv4Subnets (discovery) there is no RFC1918
// filter: these are OUR addresses, and a same-host media server is
// reachable on any of them.
func localIPv4Addresses() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
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
		s := ip4.String()
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
