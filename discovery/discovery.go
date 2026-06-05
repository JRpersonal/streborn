// Package discovery announces the stick agent via mDNS / DNS-SD on
// the local network so the desktop app and other clients find it
// without the user having to enter an IP. It also browses for stock
// Bose SoundTouch speakers that do not yet run STR, so the desktop
// app can offer to flash them.
//
// Service types:
//   _streborn._tcp.local         current STR service
//   _soundtouchstick._tcp.local  legacy STR pre-rename, still in use
//                                on speakers that have not been
//                                OTA-updated yet
//   _soundtouch._tcp.local       stock Bose speakers, primary name
//                                observed in the wild (ST10/20/30)
//   _bose-soundtouch._tcp.local  alternate stock spelling seen on
//                                some firmware variants
//
// Multiple speakers on the same network are supported. The desktop
// app lists every announced stick via DNS-SD browse plus every
// detected stock speaker.
package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"

	"github.com/grandcat/zeroconf"
)

const (
	// ServiceType is the current DNS-SD service identifier. New agents
	// announce as _streborn._tcp; clients browse the same.
	ServiceType = "_streborn._tcp"

	// LegacyServiceType is the pre-rename service identifier. NAND-
	// installed agents from earlier releases still announce under this
	// name and cannot be reached by clients that only browse the new
	// one. Browse() and Announce() handle both so a mixed-version
	// network still discovers every box.
	LegacyServiceType = "_soundtouchstick._tcp"

	// StockServiceType is the primary mDNS service that stock Bose
	// SoundTouch firmware advertises out of the box (observed on
	// ST10/20/30 with firmware 27.0.6). Browse() scans for it so the
	// desktop app can show "needs STR install" speakers next to
	// already-flashed ones. STR itself does not announce under it.
	StockServiceType = "_soundtouch._tcp"

	Domain = "local."
)

// StockServiceTypeAliases lists additional mDNS service names that
// some Bose SoundTouch firmware variants use instead of (or in
// addition to) StockServiceType. Browse() iterates over all of them
// so we do not depend on a single spelling being correct everywhere.
var StockServiceTypeAliases = []string{
	"_bose-soundtouch._tcp",
}

// Kind enumerates how a discovered speaker reports itself.
//   - KindSTR:   announces an STR agent (current or legacy service)
//   - KindStock: stock Bose firmware, no STR yet
type Kind string

const (
	KindSTR   Kind = "str"
	KindStock Kind = "stock"
)

// Announcer holds the active mDNS servers. We announce on both the
// current and the legacy service type so clients running either
// vintage can discover this stick. FriendlyName can be changed at
// runtime (UpdateFriendlyName).
type Announcer struct {
	logger       *slog.Logger
	mu           sync.Mutex
	server       *zeroconf.Server // current ServiceType
	legacyServer *zeroconf.Server // LegacyServiceType
	cfg          Config
}

// Config beschreibt was im mDNS Record steht.
type Config struct {
	// InstanceName ist der menschen-lesbare Name. Default:
	// "STR <deviceID>".
	InstanceName string
	// Port ist der TCP Port des Webui/REST API (default 8888).
	Port int
	// DeviceID ist die Bose Box MAC im uppercase ohne Trenner.
	DeviceID string
	// FriendlyName ist der Bose Box Display Name, z.B. "Wohnzimmer Bose".
	FriendlyName string
	// Model ist der Bose Modellname, z.B. "SoundTouch 10".
	Model string
	// Version ist die Stick Agent Version.
	Version string
	// Build is the agent build stamp (YYYY-MM-DD-HHMM). Announced
	// alongside Version so the desktop app's "update available"
	// indicators can detect stamp drift between two binaries that
	// happen to share the same git-describe version string.
	Build string
}

// Announce startet einen mDNS Server der den Stick announciert. Stop mit
// Close().
func Announce(logger *slog.Logger, cfg Config) (*Announcer, error) {
	if cfg.Port == 0 {
		cfg.Port = 8888
	}
	if cfg.InstanceName == "" {
		if cfg.DeviceID != "" {
			cfg.InstanceName = "STR-" + lastN(cfg.DeviceID, 6)
		} else {
			cfg.InstanceName = "STR"
		}
	}
	a := &Announcer{logger: logger, cfg: cfg}
	if err := a.register(); err != nil {
		return nil, err
	}
	return a, nil
}

// register builds the TXT record from a.cfg and registers both the
// current and legacy service entries. Caller must hold a.mu except on
// the first call.
func (a *Announcer) register() error {
	txt := []string{
		"version=" + nz(a.cfg.Version, "dev"),
		"build=" + nz(a.cfg.Build, ""),
		"deviceID=" + nz(a.cfg.DeviceID, ""),
		"model=" + nz(a.cfg.Model, ""),
		"friendlyName=" + nz(a.cfg.FriendlyName, ""),
		"path=/api",
	}
	ifaces := pickAnnounceIfaces(a.logger)

	server, err := zeroconf.Register(a.cfg.InstanceName, ServiceType, Domain, a.cfg.Port, txt, ifaces)
	if err != nil {
		return fmt.Errorf("mDNS register: %w", err)
	}
	a.server = server

	// Legacy announce is best-effort: if it fails we keep the current
	// one running rather than aborting the whole agent startup.
	legacy, lerr := zeroconf.Register(a.cfg.InstanceName, LegacyServiceType, Domain, a.cfg.Port, txt, ifaces)
	if lerr != nil {
		a.logger.Warn("legacy mDNS register failed, continuing with current only",
			slog.String("legacy", LegacyServiceType), slog.Any("err", lerr))
	} else {
		a.legacyServer = legacy
	}

	// Phase marker at WARN so a remote diagnostic bundle pinpoints
	// when each (re-)announce happened. Critical for #60-style
	// "speaker disappeared from STR after standby" investigations:
	// the bundle must show whether mDNS ever announced, and whether
	// it was re-announced around the time the desktop app lost it.
	a.logger.Warn("mDNS phase: announce active",
		slog.String("instance", a.cfg.InstanceName),
		slog.String("friendlyName", a.cfg.FriendlyName),
		slog.String("model", a.cfg.Model),
		slog.String("service", ServiceType),
		slog.String("legacyService", LegacyServiceType),
		slog.Bool("legacyAnnounced", legacy != nil),
		slog.Int("port", a.cfg.Port),
		slog.Int("ifaces", len(ifaces)),
	)
	return nil
}

// UpdateFriendlyName updates the display name in the TXT record and
// re-announces both service types. No-op if the name has not changed.
func (a *Announcer) UpdateFriendlyName(name string) error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if name == a.cfg.FriendlyName {
		return nil
	}
	a.logger.Warn("mDNS phase: re-announce trigger",
		slog.String("reason", "friendlyName change"),
		slog.String("old", a.cfg.FriendlyName),
		slog.String("new", name))
	a.cfg.FriendlyName = name
	if a.server != nil {
		a.server.Shutdown()
		a.server = nil
	}
	if a.legacyServer != nil {
		a.legacyServer.Shutdown()
		a.legacyServer = nil
	}
	return a.register()
}

// UpdateModel updates the model field in the TXT record and re-announces
// both service types. No-op if the model has not changed. Used to recover
// from the boot-time race where the Bose firmware's :8090 endpoint is
// not yet listening when the agent first tries to read /info — the
// agent then announces with a generic fallback ("SoundTouch") and this
// method is called once the real model can be read so the desktop app's
// box picker shows the proper model name.
func (a *Announcer) UpdateModel(model string) error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if model == a.cfg.Model {
		return nil
	}
	a.logger.Warn("mDNS phase: re-announce trigger",
		slog.String("reason", "model change"),
		slog.String("old", a.cfg.Model),
		slog.String("new", model))
	a.cfg.Model = model
	if a.server != nil {
		a.server.Shutdown()
		a.server = nil
	}
	if a.legacyServer != nil {
		a.legacyServer.Shutdown()
		a.legacyServer = nil
	}
	return a.register()
}

// Snapshot returns the friendlyName and model currently held in the TXT
// record. The agent serves these through its version endpoint so the desktop
// app can read the box display name straight from the running agent. That
// path is independent of the cross-LAN /info probe, which is often slow for a
// few seconds right after an OTA agent restart — exactly the window in which
// a flashed speaker otherwise shows as "str-<ip>" with no name (#108).
func (a *Announcer) Snapshot() (friendlyName, model string) {
	if a == nil {
		return "", ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.FriendlyName, a.cfg.Model
}

// Close stops the mDNS announce on both service types.
func (a *Announcer) Close() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	stopped := false
	if a.server != nil {
		a.server.Shutdown()
		a.server = nil
		stopped = true
	}
	if a.legacyServer != nil {
		a.legacyServer.Shutdown()
		a.legacyServer = nil
		stopped = true
	}
	if stopped {
		a.logger.Warn("mDNS phase: announce stopped")
	}
}

// Run blockiert bis ctx abgebrochen wird und schliesst dann den Announcer.
// Convenience fuer Use in goroutines.
func (a *Announcer) Run(ctx context.Context) {
	<-ctx.Done()
	a.Close()
}

// Instance is the result of a Browse() call. Kind distinguishes
// STR-equipped speakers from stock Bose speakers; for stock entries
// Version and Build are empty.
type Instance struct {
	Name         string
	Host         string
	IPv4         []string
	Port         int
	DeviceID     string
	Model        string
	FriendlyName string
	Version      string
	Build        string
	Kind         Kind
}

// Browse searches the LAN for sticks announcing either the current
// or the legacy STR service type and for stock Bose SoundTouch
// speakers, then returns a single merged channel. Duplicate
// speakers (announced under both STR names by a single new agent,
// or surfacing on both STR and Bose service types) are deduplicated
// by instance name. STR announcements win over stock for the same
// speaker so a flashed speaker never shows up as "needs install".
func Browse(ctx context.Context, logger *slog.Logger) (<-chan Instance, error) {
	out := make(chan Instance, 16)

	// Pick the same interface set we announce on. zeroconf's default
	// (NewResolver(nil)) lets the underlying multicast library choose
	// one interface and on multi-NIC Windows hosts that pick is
	// frequently the wrong one — observed live 2026-05-23 on a laptop
	// with Intel Wi-Fi on the home LAN plus a Realtek USB Wi-Fi dongle
	// on the Bose setup AP: Browse returned 0 instances even though
	// the ST10 on the home LAN was announcing 3 mDNS services on :5353
	// and ARP for it was cached on the right adapter. Filtering on
	// "up, non-loopback, non-usb-gadget, has a non-TEST-NET-3 IPv4"
	// (pickAnnounceIfaces) picks both real interfaces and zeroconf
	// sends the query on each — Bonjour responses come back over
	// whichever interface the speaker is on.
	// IPv4-only: zeroconf's default IPv4AndIPv6 listenOn forces an
	// IPv6 multicast join too. On Windows hosts where IPv6 multicast
	// is funky (Bonjour Service holding the port, no usable v6
	// interface for ff02::fb, etc.) the v6 join can succeed-but-eat-
	// responses or surface as "no suitable IPv6 interface" — observed
	// in a 2026-05-23 agent log (#80). The Bose speakers only announce on IPv4
	// and the desktop app's home LAN is IPv4 in every realistic
	// deployment, so pinning the resolver to v4 removes a class of
	// silent-failure paths without losing any reachable speaker.
	resolverOpts := func() []zeroconf.ClientOption {
		opts := []zeroconf.ClientOption{
			zeroconf.SelectIPTraffic(zeroconf.IPv4),
		}
		ifaces := pickAnnounceIfaces(logger)
		if len(ifaces) > 0 {
			opts = append(opts, zeroconf.SelectIfaces(ifaces))
		}
		return opts
	}

	// One resolver per service type. zeroconf.NewResolver returns a
	// short-lived resolver tied to a single Browse call.
	curResolver, err := zeroconf.NewResolver(resolverOpts()...)
	if err != nil {
		return nil, fmt.Errorf("resolver (current): %w", err)
	}
	legacyResolver, err := zeroconf.NewResolver(resolverOpts()...)
	if err != nil {
		return nil, fmt.Errorf("resolver (legacy): %w", err)
	}

	curEntries := make(chan *zeroconf.ServiceEntry, 8)
	legacyEntries := make(chan *zeroconf.ServiceEntry, 8)
	stockEntries := make(chan *zeroconf.ServiceEntry, 16)

	if err := curResolver.Browse(ctx, ServiceType, Domain, curEntries); err != nil {
		return nil, fmt.Errorf("browse current: %w", err)
	}
	if err := legacyResolver.Browse(ctx, LegacyServiceType, Domain, legacyEntries); err != nil {
		return nil, fmt.Errorf("browse legacy: %w", err)
	}

	// Stock browse is best-effort across every spelling we know about.
	// Each alias needs its own resolver and its own per-alias entries
	// channel because zeroconf.Browse closes the channel when ctx ends
	// and we cannot have multiple Browse() writing into one channel
	// without risking a double-close panic. We fan them all into
	// stockEntries via forwarder goroutines and close stockEntries
	// when every alias is done.
	stockNames := append([]string{StockServiceType}, StockServiceTypeAliases...)
	var stockWG sync.WaitGroup
	for _, svc := range stockNames {
		r, err := zeroconf.NewResolver(resolverOpts()...)
		if err != nil {
			if logger != nil {
				logger.Warn("stock mDNS resolver create failed",
					slog.String("service", svc), slog.Any("err", err))
			}
			continue
		}
		per := make(chan *zeroconf.ServiceEntry, 8)
		if err := r.Browse(ctx, svc, Domain, per); err != nil {
			if logger != nil {
				logger.Warn("stock mDNS browse failed, continuing",
					slog.String("service", svc), slog.Any("err", err))
			}
			continue
		}
		stockWG.Add(1)
		go func() {
			defer stockWG.Done()
			for e := range per {
				stockEntries <- e
			}
		}()
	}
	go func() {
		stockWG.Wait()
		close(stockEntries)
	}()

	go func() {
		defer close(out)
		// seen[instanceName] holds the Kind that won. STR wins over
		// stock so an already-flashed speaker is not re-announced
		// as a stock entry from a stale Bose-service record.
		seen := map[string]Kind{}
		emit := func(e *zeroconf.ServiceEntry, kind Kind, legacy bool) {
			key := e.Instance + "|" + e.HostName
			prev, exists := seen[key]
			if exists && prev == KindSTR {
				return // STR wins
			}
			seen[key] = kind
			inst := Instance{
				Name: e.Instance,
				Host: e.HostName,
				Port: e.Port,
				Kind: kind,
			}
			for _, ip := range e.AddrIPv4 {
				inst.IPv4 = append(inst.IPv4, ip.String())
			}
			// TXT key handling is case-insensitive. STR uses
			// camelCase, stock Bose firmware tends to use UPPERCASE
			// (MAC, MODEL, NAME, SOFTWAREVERSION) on most variants
			// but we have also seen lowercase on a few. Treat them
			// uniformly so a casing change in a future firmware does
			// not silently break discovery.
			for _, kv := range e.Text {
				k, v, _ := strings.Cut(kv, "=")
				switch strings.ToLower(k) {
				case "deviceid", "mac":
					if inst.DeviceID == "" {
						inst.DeviceID = strings.ToUpper(strings.ReplaceAll(v, ":", ""))
					}
				case "model":
					if inst.Model == "" {
						inst.Model = stockModelLabel(v)
					}
				case "friendlyname", "name":
					if inst.FriendlyName == "" {
						inst.FriendlyName = v
					}
				case "version", "softwareversion":
					if inst.Version == "" {
						inst.Version = v
					}
				case "build":
					if inst.Build == "" {
						inst.Build = v
					}
				}
			}
			if logger != nil {
				logger.Debug("mDNS discovered",
					slog.String("instance", inst.Name),
					slog.String("kind", string(kind)),
					slog.Bool("legacyServiceType", legacy),
					slog.Any("ipv4", inst.IPv4))
			}
			out <- inst
		}
		for curEntries != nil || legacyEntries != nil || stockEntries != nil {
			select {
			case e, ok := <-curEntries:
				if !ok {
					curEntries = nil
					continue
				}
				emit(e, KindSTR, false)
			case e, ok := <-legacyEntries:
				if !ok {
					legacyEntries = nil
					continue
				}
				emit(e, KindSTR, true)
			case e, ok := <-stockEntries:
				if !ok {
					stockEntries = nil
					continue
				}
				emit(e, KindStock, false)
			}
		}
	}()

	return out, nil
}

// stockModelLabel turns Bose's short product code from the stock
// mDNS TXT record into a human-readable label that matches what STR
// already shows for flashed speakers ("SoundTouch 10", etc.).
// Falls back to the raw code if no mapping is known.
func stockModelLabel(code string) string {
	switch strings.ToLower(code) {
	case "soundtouch_10", "st10":
		return "SoundTouch 10"
	case "soundtouch_20", "st20":
		return "SoundTouch 20"
	case "soundtouch_30", "st30":
		return "SoundTouch 30"
	case "soundtouch_portable", "stp":
		return "SoundTouch Portable"
	default:
		return code
	}
}

// pickAnnounceIfaces filtert net.Interfaces auf die Interfaces auf denen
// wir announcen wollen. Excludiert Loopback, Down, usb0 (Bose USB Gadget
// mit TEST-NET-3 IP).
func pickAnnounceIfaces(logger *slog.Logger) []net.Interface {
	all, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []net.Interface
	for _, iface := range all {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Name == "usb0" || strings.HasPrefix(iface.Name, "usb") {
			logger.Debug("mDNS skip USB Gadget Interface", slog.String("iface", iface.Name))
			continue
		}
		// Extra Sicherheit: pruefen ob nur TEST-NET-3 IPs zugewiesen sind
		addrs, _ := iface.Addrs()
		hasUsable := false
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ipnet.IP.To4() == nil {
				continue
			}
			s := ipnet.IP.String()
			if strings.HasPrefix(s, "203.0.113.") {
				continue
			}
			hasUsable = true
		}
		if !hasUsable {
			// Debug, not Info: macOS hosts have a long list of virtual
			// interfaces (awdl0, llw0, p2p0, utunN) with no usable IPv4,
			// and this fires for each of them on every discovery cycle.
			// At Info it floods str.log (the dominant source of log
			// growth reported by users).
			logger.Debug("mDNS skip Interface ohne brauchbare IP", slog.String("iface", iface.Name))
			continue
		}
		out = append(out, iface)
	}
	if len(out) == 0 {
		// Fallback: zeroconf default mit allen Interfaces
		return nil
	}
	return out
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func nz(s, dflt string) string {
	if s == "" {
		return dflt
	}
	return s
}
