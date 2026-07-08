package main

// Manual media servers (#341).
//
// SSDP discovery cannot see every server: a firewall that filters
// UDP, a server on another subnet, or an exotic same-host setup can
// all leave the Library list empty even though the server is
// perfectly reachable over HTTP. As a last-resort fallback the user
// can add a server by IP or description URL; those entries are
// persisted in the user-config dir (same location as app-state.json)
// and merged into every ListMediaServers result.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/dlna"
)

type manualMediaServer struct {
	// Location is the device-description URL the entry was added
	// with; re-fetched on every scan so the snapshot stays current.
	Location string `json:"location"`
	// Server is the last successfully fetched description, kept so
	// the entry still lists (and can be removed) while the server is
	// temporarily off.
	Server dlna.Server `json:"server"`
}

var manualServersMu sync.Mutex

func manualServersPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ST Reborn", "media-servers.json"), nil
}

func readManualServers() []manualMediaServer {
	path, err := manualServersPath()
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var list []manualMediaServer
	_ = json.Unmarshal(b, &list)
	return list
}

func writeManualServers(list []manualMediaServer) error {
	path, err := manualServersPath()
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

// refreshedManualServers returns the persisted manual servers, each
// re-described in parallel so names/control URLs are current. A
// server that does not answer right now falls back to its stored
// snapshot: it stays listed (browse will fail with a clear error) and
// remains removable.
func (a *App) refreshedManualServers() []dlna.Server {
	manualServersMu.Lock()
	list := readManualServers()
	manualServersMu.Unlock()
	if len(list) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(a.appCtx(), 5*time.Second)
	defer cancel()
	out := make([]dlna.Server, len(list))
	var wg sync.WaitGroup
	for i, m := range list {
		wg.Add(1)
		go func(i int, m manualMediaServer) {
			defer wg.Done()
			if s, err := dlna.DescribeServer(ctx, m.Location); err == nil {
				out[i] = s
				return
			}
			out[i] = m.Server // offline right now: keep the snapshot
		}(i, m)
	}
	wg.Wait()
	return out
}

// wellKnownDescriptionEndpoints are the device-description locations
// of the DLNA servers users most commonly run on a PC or NAS, probed
// when AddMediaServerByURL gets a bare IP/hostname without a full
// description URL. Deliberately short: anything else can always be
// added via its exact URL.
//
//	:8200/rootDesc.xml                  MiniDLNA / ReadyMedia
//	:9000/plugins/UPnP/MediaServer.xml  Twonky
//	:50001/desc/device.xml              Synology Media Server
//	:8895/rootDesc.xml                  Serviio and MiniDLNA variants
//	:5000/DeviceDescription.xml         generic NAS media servers
var wellKnownDescriptionEndpoints = []struct {
	port string
	path string
}{
	{"8200", "/rootDesc.xml"},
	{"9000", "/plugins/UPnP/MediaServer.xml"},
	{"50001", "/desc/device.xml"},
	{"8895", "/rootDesc.xml"},
	{"5000", "/DeviceDescription.xml"},
}

// manualServerCandidates normalizes the user's input into the list of
// description URLs to probe, most specific first. Accepted forms:
// a full http(s) URL (tried as-is when it has a path), "ip:port"
// (well-known description paths tried on that port), or a bare
// ip/hostname (well-known port+path combinations tried).
func manualServerCandidates(input string) []string {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}
	host, port := "", ""
	if strings.Contains(input, "://") {
		u, err := url.Parse(input)
		if err != nil || u.Host == "" {
			return []string{input}
		}
		if u.Path != "" && u.Path != "/" {
			return []string{input} // exact description URL given
		}
		host, port = u.Hostname(), u.Port()
	} else if h, p, err := net.SplitHostPort(input); err == nil {
		host, port = h, p
	} else {
		host = input
	}
	var out []string
	if port != "" {
		// Port known: try each well-known description PATH on it.
		seen := map[string]bool{}
		for _, e := range wellKnownDescriptionEndpoints {
			if seen[e.path] {
				continue
			}
			seen[e.path] = true
			out = append(out, "http://"+net.JoinHostPort(host, port)+e.path)
		}
		return out
	}
	for _, e := range wellKnownDescriptionEndpoints {
		out = append(out, "http://"+net.JoinHostPort(host, e.port)+e.path)
	}
	return out
}

// AddMediaServerByURL adds a DLNA media server the SSDP sweep did not
// find, from a bare IP, "ip:port", or a full device-description URL.
// All candidate locations are probed in parallel; the first (in
// candidate order) that yields a valid MediaServer description is
// persisted and returned. The server immediately becomes browsable
// (it is placed into the UDN cache) and is part of every subsequent
// ListMediaServers result.
func (a *App) AddMediaServerByURL(urlOrIP string) (LibraryServer, error) {
	cands := manualServerCandidates(urlOrIP)
	if len(cands) == 0 {
		return LibraryServer{}, fmt.Errorf("empty address")
	}
	ctx, cancel := context.WithTimeout(a.appCtx(), 10*time.Second)
	defer cancel()

	type probe struct {
		s   dlna.Server
		err error
	}
	results := make([]probe, len(cands))
	var wg sync.WaitGroup
	for i, loc := range cands {
		wg.Add(1)
		go func(i int, loc string) {
			defer wg.Done()
			s, err := dlna.DescribeServer(ctx, loc)
			results[i] = probe{s: s, err: err}
		}(i, loc)
	}
	wg.Wait()

	idx := -1
	for i := range results {
		if results[i].err == nil {
			idx = i
			break
		}
	}
	if idx < 0 {
		a.logger.Info("library: manual media server probe failed",
			"input", urlOrIP, "candidates", len(cands), "firstErr", results[0].err.Error())
		return LibraryServer{}, fmt.Errorf("no media server found at %q: %w", urlOrIP, results[0].err)
	}
	s, loc := results[idx].s, cands[idx]

	manualServersMu.Lock()
	list := readManualServers()
	replaced := false
	for i := range list {
		if list[i].Server.UDN == s.UDN {
			list[i] = manualMediaServer{Location: loc, Server: s}
			replaced = true
			break
		}
	}
	if !replaced {
		list = append(list, manualMediaServer{Location: loc, Server: s})
	}
	err := writeManualServers(list)
	manualServersMu.Unlock()
	if err != nil {
		return LibraryServer{}, fmt.Errorf("saving manual server list: %w", err)
	}

	// Make it browsable right away, without requiring a rescan.
	a.libraryMu.Lock()
	a.libraryServers[s.UDN] = s
	a.libraryMu.Unlock()

	a.logger.Info("library: manual media server added",
		"location", loc, "udn", s.UDN, "name", s.FriendlyName)
	return LibraryServer{
		UDN:          s.UDN,
		FriendlyName: s.FriendlyName,
		Manufacturer: s.Manufacturer,
		ModelName:    s.ModelName,
		IconURL:      s.IconURL,
		Address:      s.Address,
		Manual:       true,
	}, nil
}

// RemoveManualMediaServer removes a manually added media server (by
// UDN) from the persisted list and the browse cache. Servers found
// via SSDP are unaffected.
func (a *App) RemoveManualMediaServer(udn string) error {
	manualServersMu.Lock()
	list := readManualServers()
	out := list[:0]
	for _, m := range list {
		if m.Server.UDN != udn {
			out = append(out, m)
		}
	}
	err := writeManualServers(out)
	manualServersMu.Unlock()
	if err != nil {
		return err
	}
	a.libraryMu.Lock()
	delete(a.libraryServers, udn)
	a.libraryMu.Unlock()
	a.logger.Info("library: manual media server removed", "udn", udn)
	return nil
}
