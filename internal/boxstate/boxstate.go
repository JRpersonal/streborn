// Package boxstate klassifiziert den Betriebszustand einer Bose-Box aus
// ihren REST-Endpunkten (:8090). Es ersetzt die verstreuten Ad-hoc-Checks
// (boxInSetupOOB, IsPaired, Setup-AP-Gates) durch einen einzigen,
// testbaren Zustandsautomaten, den Agent, Desktop-App und Diagnose nutzen.
//
// Hintergrund (live taigan 2026-06-10): eine BCO-Box kann auf Chipset-Ebene
// ins WLAN gejoint sein (echte LAN-IP) und trotzdem auf :8090 noch
// SETUP_AP_OOB melden, bis der SoundTouch-Setup über das LAN abgeschlossen
// wird. Der Zustand ergibt sich daher aus mehreren Fakten zusammen, nicht
// aus einem einzelnen Endpunkt.
package boxstate

import (
	"context"
	"strings"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// State ist der grob klassifizierte Box-Zustand.
type State int

const (
	// StateUnreachable: :8090 antwortet nicht.
	StateUnreachable State = iota
	// StateOOBSetupAP: Box spannt ihren Bose-Setup-AP auf (eigene IP
	// 192.168.1.1, /setup=SETUP_AP_OOB). Noch nicht im LAN. LED gelb.
	StateOOBSetupAP
	// StateOnlineNotConfigured: Box hat eine echte LAN-IP (assoziiert),
	// aber die SoundTouch-Schicht ist noch nicht fertig konfiguriert
	// (Setup noch OOB / Sprache nicht gesetzt / kein Account). Typisch
	// direkt nach einem Chipset-Join. LED meist noch gelb.
	StateOnlineNotConfigured
	// StateOnlineConfigured: im LAN und fertig konfiguriert
	// (Setup verlassen, Account gesetzt). LED weiß.
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

// IsOnline meldet, ob die Box im normalen LAN erreichbar ist (nicht im
// eigenen Setup-AP).
func (s State) IsOnline() bool {
	return s == StateOnlineNotConfigured || s == StateOnlineConfigured
}

// IsConfigured meldet, ob der SoundTouch-Setup abgeschlossen ist.
func (s State) IsConfigured() bool { return s == StateOnlineConfigured }

// setupAPAddr ist die feste Gateway-IP, unter der eine Box im eigenen
// Setup-AP erreichbar ist. Eine Box, die diese IP als eigene Adresse
// meldet, IST der AP (im STA-Modus bekäme sie eine DHCP-Adresse, die im
// üblichen Heimnetz nie exakt .1.1 ist, da das der Router ist).
const setupAPAddr = "192.168.1.1"

// Facts bündelt die Rohfakten, aus denen der Zustand abgeleitet wird.
type Facts struct {
	Reachable        bool
	SetupState       string // z.B. SETUP_AP_OOB, SETUP_INACTIVE
	SystemState      string // z.B. SETUP_LANG_SET, SETUP_LANG_NOT_SET
	MargeAccountUUID string // leer = nicht gepaart
	IP               string // gemeldete eigene IP (info/networkInfo)
	SSID             string // gespeichertes WLAN-Profil (kann ohne Assoziation gesetzt sein)
	ModuleType       string // scm, sm2, ...
	Variant          string // taigan, rhino, spotty, ...
	Firmware         string
	WifiProfileCount int
	InterfaceState   string // Zustand des ersten Interfaces
}

// Classify leitet aus den Rohfakten den Zustand ab. Reine Funktion, ohne
// Netzwerk, damit sie gegen Fixtures unit-getestet werden kann.
func Classify(f Facts) State {
	if !f.Reachable {
		return StateUnreachable
	}
	// Eigene IP == Setup-AP-Gateway -> die Box IST ihr eigener AP.
	if f.IP == setupAPAddr {
		return StateOOBSetupAP
	}
	onLAN := f.IP != "" && f.IP != "0.0.0.0"
	if !onLAN {
		// Erreichbar, aber keine brauchbare IP gemeldet: nur dann sicher
		// OOB, wenn der Setup-State das bestätigt; sonst unbekannt ->
		// als "im AP" behandeln (konservativ, blockt Preset-Push etc.).
		if strings.EqualFold(f.SetupState, "SETUP_AP_OOB") {
			return StateOOBSetupAP
		}
		return StateOOBSetupAP
	}
	// Echte LAN-IP vorhanden. Fertig konfiguriert nur, wenn der Setup
	// verlassen wurde UND ein Account gesetzt ist.
	leftSetup := !strings.EqualFold(f.SetupState, "SETUP_AP_OOB")
	hasAccount := f.MargeAccountUUID != ""
	if leftSetup && hasAccount {
		return StateOnlineConfigured
	}
	return StateOnlineNotConfigured
}

// Detector probt eine Box und klassifiziert ihren Zustand.
type Detector struct {
	client *boxapi.Client
}

// New erzeugt einen Detector für den Box-Host (typisch eine LAN-IP oder
// 192.168.1.1 im Setup-AP).
func New(host string) *Detector {
	return &Detector{client: boxapi.New(host)}
}

// Detect probt /info, /networkInfo, /setup und /getActiveWirelessProfile
// und liefert Zustand + Rohfakten. Wenn /info nicht erreichbar ist, gilt
// die Box als unerreichbar; die übrigen Endpunkte sind best effort.
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
