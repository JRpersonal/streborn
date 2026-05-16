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
	// ServiceType ist der DNS-SD Service Identifier. Clients browsen
	// _streborn._tcp.local um alle Sticks im LAN zu finden.
	ServiceType = "_streborn._tcp"
	Domain      = "local."
)

// Announcer haelt einen aktiven mDNS Server. Der FriendlyName kann zur
// Laufzeit geaendert werden (UpdateFriendlyName) — wenn der User die Box
// in der App umbenennt, soll sich das auch im mDNS Announce wiederfinden.
type Announcer struct {
	logger *slog.Logger
	mu     sync.Mutex
	server *zeroconf.Server
	cfg    Config
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

// register baut den TXT Record aus a.cfg und registriert den Service neu.
// Lock muss vom Aufrufer gehalten werden ausser beim ersten Aufruf.
func (a *Announcer) register() error {
	txt := []string{
		"version=" + nz(a.cfg.Version, "dev"),
		"deviceID=" + nz(a.cfg.DeviceID, ""),
		"model=" + nz(a.cfg.Model, ""),
		"friendlyName=" + nz(a.cfg.FriendlyName, ""),
		"path=/api",
	}
	ifaces := pickAnnounceIfaces(a.logger)
	server, err := zeroconf.Register(
		a.cfg.InstanceName,
		ServiceType,
		Domain,
		a.cfg.Port,
		txt,
		ifaces,
	)
	if err != nil {
		return fmt.Errorf("mDNS register: %w", err)
	}
	a.server = server
	a.logger.Info("mDNS Announce aktiv",
		slog.String("instance", a.cfg.InstanceName),
		slog.String("friendlyName", a.cfg.FriendlyName),
		slog.String("service", ServiceType),
		slog.Int("port", a.cfg.Port),
	)
	return nil
}

// UpdateFriendlyName aktualisiert den Display Namen im TXT Record und
// macht ein Re-Announce. No-op wenn der Name sich nicht geaendert hat.
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
	return a.register()
}

// Close stoppt den mDNS Server sauber.
func (a *Announcer) Close() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.server == nil {
		return
	}
	a.server.Shutdown()
	a.server = nil
	a.logger.Info("mDNS Announce gestoppt")
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
}

// Browse sucht alle Sticks im LAN. Blockiert bis ctx abgebrochen wird
// oder das Channel ausgelesen wird.
func Browse(ctx context.Context, logger *slog.Logger) (<-chan Instance, error) {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return nil, fmt.Errorf("resolver: %w", err)
	}
	entries := make(chan *zeroconf.ServiceEntry, 8)
	out := make(chan Instance, 8)

	go func() {
		defer close(out)
		for e := range entries {
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
				}
			}
			out <- inst
		}
	}()

	if err := resolver.Browse(ctx, ServiceType, Domain, entries); err != nil {
		return nil, fmt.Errorf("browse: %w", err)
	}
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
