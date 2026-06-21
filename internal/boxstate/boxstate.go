// Package boxstate classifies the operating state of a Bose box from
// its REST endpoints (:8090). It replaces the scattered ad-hoc checks
// (boxInSetupOOB, IsPaired, setup-AP gates) with a single, testable
// state machine that the agent, desktop app, and diagnostics use.
//
// Background (live taigan 2026-06-10): a BCO box can be joined to the
// Wi-Fi at the chipset level (real LAN IP) and still report SETUP_AP_OOB
// on :8090, until the SoundTouch setup is completed over the LAN. The
// state therefore follows from several facts together, not from a single
// endpoint.
package boxstate

import (
	"context"
	"strings"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// State is the coarsely classified box state.
type State int

const (
	// StateUnreachable: :8090 does not answer.
	StateUnreachable State = iota
	// StateOOBSetupAP: box is spinning up its Bose setup AP (own IP
	// 192.168.1.1, /setup=SETUP_AP_OOB). Not yet on the LAN. LED yellow.
	StateOOBSetupAP
	// StateOnlineNotConfigured: box has a real LAN IP (associated),
	// but the SoundTouch layer is not yet fully configured (setup
	// still OOB / language not set / no account). Typical right after
	// a chipset join. LED usually still yellow.
	StateOnlineNotConfigured
	// StateOnlineConfigured: on the LAN and fully configured
	// (setup left, account set). LED white.
	StateOnlineConfigured
)

func (s State) String() string {
	switch s {
	case StateOOBSetupAP:
		return "oob-setup-ap"
	case StateOnlineNotConfigured:
		return "online-not-configured"
	case StateOnlineConfigured:
		return "online-configured"
	default:
		return "unreachable"
	}
}

// IsOnline reports whether the box is reachable on the normal LAN (not in
// its own setup AP).
func (s State) IsOnline() bool {
	return s == StateOnlineNotConfigured || s == StateOnlineConfigured
}

// IsConfigured reports whether the SoundTouch setup is complete.
func (s State) IsConfigured() bool { return s == StateOnlineConfigured }

// setupAPAddr is the fixed gateway IP under which a box is reachable in
// its own setup AP. A box that reports this IP as its own address IS the
// AP (in STA mode it would get a DHCP address, which in a typical home
// network is never exactly .1.1, since that is the router).
const setupAPAddr = "192.168.1.1"

// Facts bundles the raw facts the state is derived from.
type Facts struct {
	Reachable        bool
	SetupState       string // e.g. SETUP_AP_OOB, SETUP_INACTIVE
	SystemState      string // e.g. SETUP_LANG_SET, SETUP_LANG_NOT_SET
	MargeAccountUUID string // empty = not paired
	IP               string // reported own IP (info/networkInfo)
	SSID             string // stored Wi-Fi profile (can be set without association)
	ModuleType       string // scm, sm2, ...
	Variant          string // taigan, rhino, spotty, ...
	Firmware         string
	WifiProfileCount int
	InterfaceState   string // state of the first interface
}

// Classify derives the state from the raw facts. Pure function, no
// network, so it can be unit-tested against fixtures.
func Classify(f Facts) State {
	if !f.Reachable {
		return StateUnreachable
	}
	// Own IP == setup-AP gateway -> the box IS its own AP.
	if f.IP == setupAPAddr {
		return StateOOBSetupAP
	}
	onLAN := f.IP != "" && f.IP != "0.0.0.0"
	if !onLAN {
		// Reachable, but no usable IP reported: only definitely OOB when
		// the setup state confirms it; otherwise unknown -> treat as "in
		// the AP" (conservative, blocks preset push etc.).
		if strings.EqualFold(f.SetupState, "SETUP_AP_OOB") {
			return StateOOBSetupAP
		}
		return StateOOBSetupAP
	}
	// Real LAN IP present. Fully configured only when the setup was left
	// AND an account is set.
	leftSetup := !strings.EqualFold(f.SetupState, "SETUP_AP_OOB")
	hasAccount := f.MargeAccountUUID != ""
	if leftSetup && hasAccount {
		return StateOnlineConfigured
	}
	return StateOnlineNotConfigured
}

// Detector probes a box and classifies its state.
type Detector struct {
	client *boxapi.Client
}

// New creates a Detector for the box host (typically a LAN IP or
// 192.168.1.1 in the setup AP).
func New(host string) *Detector {
	return &Detector{client: boxapi.New(host)}
}

// Detect probes /info, /networkInfo, /setup and /getActiveWirelessProfile
// and returns state + raw facts. If /info is not reachable, the box is
// considered unreachable; the remaining endpoints are best effort.
func (d *Detector) Detect(ctx context.Context) (State, Facts, error) {
	var f Facts
	info, err := d.client.GetInfo(ctx)
	if err != nil {
		return StateUnreachable, f, err
	}
	f.Reachable = true
	f.MargeAccountUUID = info.MargeAccountUUID
	f.ModuleType = info.ModuleType
	f.Variant = info.Variant
	f.Firmware = info.Version
	f.IP = info.IP

	if net, err := d.client.GetNetwork(ctx); err == nil {
		f.WifiProfileCount = net.WifiProfileCount
		if len(net.Interfaces) > 0 {
			f.InterfaceState = net.Interfaces[0].State
			if f.IP == "" {
				for _, i := range net.Interfaces {
					if i.IP != "" && i.IP != "0.0.0.0" {
						f.IP = i.IP
						break
					}
				}
			}
		}
	}
	if st, err := d.client.GetSetupStatus(ctx); err == nil {
		f.SetupState = st.State
		f.SystemState = st.SystemState
	}
	if ssid, err := d.client.GetActiveWirelessProfile(ctx); err == nil {
		f.SSID = ssid
	}
	return Classify(f), f, nil
}
