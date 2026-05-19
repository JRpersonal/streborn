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
//   _bose-soundtouch._tcp.local  stock Bose speakers (informational
//                                only, not announced by STR)
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

	// StockServiceType is the mDNS service that stock Bose SoundTouch
	// firmware advertises out of the box. STR does not announce under
	// this name; Browse() scans for it so the desktop app can show
	// "needs STR install" speakers next to already-flashed ones.
	StockServiceType = "_bose-soundtouch._tcp"

	Domain = "local."
)

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

	a.logger.Info("mDNS announce active",
		slog.String("instance", a.cfg.InstanceName),
		slog.String("friendlyName", a.cfg.FriendlyName),
		slog.String("service", ServiceType),
		slog.String("legacyService", LegacyServiceType),
		slog.Bool("legacyAnnounced", legacy != nil),
		slog.Int("port", a.cfg.Port),
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
		a.logger.Info("mDNS announce stopped")
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

	// One resolver per service type. zeroconf.NewResolver returns a
	// short-lived resolver tied to a single Browse call.
	curResolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return nil, fmt.Errorf("resolver (current): %w", err)
	}
	legacyResolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return nil, fmt.Errorf("resolver (legacy): %w", err)
	}
	stockResolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return nil, fmt.Errorf("resolver (stock): %w", err)
	}

	curEntries := make(chan *zeroconf.ServiceEntry, 8)
	legacyEntries := make(chan *zeroconf.ServiceEntry, 8)
	stockEntries := make(chan *zeroconf.ServiceEntry, 8)

	if err := curResolver.Browse(ctx, ServiceType, Domain, curEntries); err != nil {
		return nil, fmt.Errorf("browse current: %w", err)
	}
	if err := legacyResolver.Browse(ctx, LegacyServiceType, Domain, legacyEntries); err != nil {
		return nil, fmt.Errorf("browse legacy: %w", err)
	}
	if err := stockResolver.Browse(ctx, StockServiceType, Domain, stockEntries); err != nil {
		// Stock browse is best-effort. If it fails, keep going with
		// STR discovery only so a flaky network stack does not
		// disable the desktop app's main path.
		if logger != nil {
			logger.Warn("stock mDNS browse failed, continuing without it",
				slog.String("service", StockServiceType), slog.Any("err", err))
		}
		stockEntries = nil
	}

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
			for _, kv := range e.Text {
				k, v, _ := strings.Cut(kv, "=")
				switch k {
				case "deviceID":
					inst.DeviceID = v
				case "model":
					inst.Model = v
				case "friendlyName":
					inst.FriendlyName = v
				case "version":
					inst.Version = v
				case "build":
					inst.Build = v
				// Stock Bose firmware uses different TXT keys.
				// MAC: hex MAC address of the speaker
				// MODEL: short product code (e.g. "soundtouch_10")
				// SOFTWAREVERSION: stock firmware version
				case "MAC":
					if inst.DeviceID == "" {
						inst.DeviceID = strings.ToUpper(v)
					}
				case "MODEL":
					if inst.Model == "" {
						inst.Model = stockModelLabel(v)
					}
				case "NAME":
					if inst.FriendlyName == "" {
						inst.FriendlyName = v
					}
				case "SOFTWAREVERSION":
					if inst.Version == "" {
						inst.Version = v
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
			logger.Info("mDNS skip USB Gadget Interface", slog.String("iface", iface.Name))
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
			logger.Info("mDNS skip Interface ohne brauchbare IP", slog.String("iface", iface.Name))
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
