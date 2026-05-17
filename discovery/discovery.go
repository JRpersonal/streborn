// Package discovery announciert den Stick via mDNS / DNS-SD im lokalen Netz
// damit die Desktop App und andere Clients ihn automatisch finden ohne
// dass der User eine IP eintippen muss.
//
// Service Typ: _streborn._tcp.local
// Damit kann ein User mehrere SoundTouch Boxen im Netz haben (jede mit
// eigenem Stick); die Desktop App listet alle Sticks via DNS-SD Browse.
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

	Domain = "local."
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

// Instance ist das Ergebnis eines Browse() Aufrufs.
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
}

// Browse searches the LAN for sticks announcing either the current or
// the legacy service type and returns a single merged channel.
// Duplicate boxes (announced under both names by a single new agent)
// are deduplicated by instance name.
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

	curEntries := make(chan *zeroconf.ServiceEntry, 8)
	legacyEntries := make(chan *zeroconf.ServiceEntry, 8)

	if err := curResolver.Browse(ctx, ServiceType, Domain, curEntries); err != nil {
		return nil, fmt.Errorf("browse current: %w", err)
	}
	if err := legacyResolver.Browse(ctx, LegacyServiceType, Domain, legacyEntries); err != nil {
		return nil, fmt.Errorf("browse legacy: %w", err)
	}

	go func() {
		defer close(out)
		seen := map[string]bool{}
		emit := func(e *zeroconf.ServiceEntry, legacy bool) {
			key := e.Instance + "|" + e.HostName
			if seen[key] {
				return
			}
			seen[key] = true
			inst := Instance{
				Name: e.Instance,
				Host: e.HostName,
				Port: e.Port,
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
				}
			}
			if logger != nil {
				logger.Debug("mDNS discovered",
					slog.String("instance", inst.Name),
					slog.Bool("legacyServiceType", legacy),
					slog.Any("ipv4", inst.IPv4))
			}
			out <- inst
		}
		for curEntries != nil || legacyEntries != nil {
			select {
			case e, ok := <-curEntries:
				if !ok {
					curEntries = nil
					continue
				}
				emit(e, false)
			case e, ok := <-legacyEntries:
				if !ok {
					legacyEntries = nil
					continue
				}
				emit(e, true)
			}
		}
	}()

	return out, nil
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
