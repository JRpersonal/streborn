package webui

// Which network is this speaker ACTUALLY on right now?
//
// wlanlist.go answers what the speaker is configured for, from two stores. On a
// wpa chassis it also gets liveness for free, because wpa_cli flags the
// selected network [CURRENT]. On a coprocessor chassis (scm: taigan Portable,
// mojo ST30) there is no wpa_cli, the stored profiles are read out of BoseApp's
// own XML, and nothing in that file says which one the speaker joined. Every
// profile came back current=false.
//
// So on exactly the two boxes whose Wi-Fi change is applied by a REBOOT, with no
// live rollback to catch a bad switch, STR could not tell whether the switch had
// worked. Measured on a fleet 2026-09-30: an ST30 and a Portable both reported a
// single stored profile at current=false while sitting happily on the network.
//
// The signal was there the whole time. /networkInfo, which the box answers on
// localhost and which boxapi already parses, reports the association. The catch
// is that the firmware presents the Wi-Fi chip on those boxes as an ETHERNET
// interface:
//
//	ST30      eth0   ETHERNET_INTERFACE  NETWORK_ETHERNET_CONNECTED  ssid set
//	Portable  eth0   ETHERNET_INTERFACE  NETWORK_ETHERNET_CONNECTED  ssid set
//	ST10      wlan0  WIFI_INTERFACE      NETWORK_WIFI_CONNECTED      ssid set
//	ST10      wlan1  WIFI_INTERFACE      NETWORK_WIFI_DISCONNECTED   ssid set
//
// It fills the SSID in anyway, which is what makes this answerable. Note the
// last row: a DISCONNECTED interface still carries an SSID, and on the measured
// ST10 it also carried a frequency and a signal grade copied from the radio that
// is up. Anything reading these has to go by the state, never by the presence of
// a name.

import (
	"context"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// wlanLive is the speaker's current association, as the firmware reports it.
// The network NAME never enters this struct: it carries the salted tag instead,
// because this ends up in the diagnostic state that the phone remote downloads
// and users mail in (see wlanConfigured.redacted).
type wlanLive struct {
	Interface string `json:"interface,omitempty"`
	Type      string `json:"type,omitempty"`
	State     string `json:"state,omitempty"`
	SSIDTag   string `json:"ssidTag,omitempty"`
	// FreqKHz and Signal are absent on the coprocessor chassis (both reported
	// 0 and empty there), so they are omitted rather than shown as zeroes.
	FreqKHz int    `json:"frequencyKHz,omitempty"`
	Signal  string `json:"signal,omitempty"`
}

// interfaceIsAssociated reports whether this interface is actually joined to a
// network.
//
// The state strings are the trap. "NETWORK_WIFI_DISCONNECTED" contains the
// substring "CONNECTED", so a Contains check reads a dead radio as a live one,
// and on the measured ST10 that radio also carried a plausible SSID, frequency
// and signal grade. Reject the negative spellings first, then require the
// positive one as a suffix.
func interfaceIsAssociated(i boxapi.NetworkInterface) bool {
	st := strings.ToUpper(strings.TrimSpace(i.State))
	if strings.Contains(st, "DISCONNECT") || strings.Contains(st, "NOT_CONNECTED") {
		return false
	}
	if !strings.HasSuffix(st, "_CONNECTED") {
		return false
	}
	return strings.TrimSpace(i.SSID) != ""
}

// liveAssociation picks the associated interface and returns the network name in
// clear (for matching against the stored lists, never for output) alongside the
// record that is safe to publish.
//
// A Wi-Fi interface wins over an Ethernet-typed one when both are associated: on
// a chassis that has real wlan interfaces, an eth0 that also claims a network is
// the less trustworthy of the two. On the coprocessor chassis there is no wlan
// interface at all, so the Ethernet-typed one is the answer rather than a
// fallback.
func liveAssociation(ifaces []boxapi.NetworkInterface) (ssid string, live *wlanLive) {
	var best *boxapi.NetworkInterface
	for i := range ifaces {
		if !interfaceIsAssociated(ifaces[i]) {
			continue
		}
		if best == nil {
			best = &ifaces[i]
			continue
		}
		bestIsWiFi := strings.Contains(strings.ToUpper(best.Type), "WIFI")
		thisIsWiFi := strings.Contains(strings.ToUpper(ifaces[i].Type), "WIFI")
		if thisIsWiFi && !bestIsWiFi {
			best = &ifaces[i]
		}
	}
	if best == nil {
		return "", nil
	}
	name := strings.TrimSpace(best.SSID)
	return name, &wlanLive{
		Interface: best.Name,
		Type:      best.Type,
		State:     best.State,
		SSIDTag:   ssidTag(name),
		FreqKHz:   best.Frequency,
		Signal:    best.Signal,
	}
}

// withLiveAssociation records the current association and marks the matching
// entries in both stores as current.
//
// An entry that is ALREADY current is left alone: on a wpa chassis wpa_cli's
// [CURRENT] flag comes from the supplicant's own selection, which is a better
// authority than a name match, and a disagreement between the two is itself
// worth seeing rather than smoothing over.
//
// Called before redaction, because matching needs the names.
func withLiveAssociation(out wlanConfigured, ifaces []boxapi.NetworkInterface) wlanConfigured {
	name, live := liveAssociation(ifaces)
	if live == nil {
		return out
	}
	out.Live = live
	if name == "" {
		return out
	}
	out.Networks = markCurrentBySSID(out.Networks, name)
	out.Stored = markCurrentBySSID(out.Stored, name)
	return out
}

// markCurrentBySSID flags every entry whose name matches the live association.
//
// Every match is flagged, not just the first: two stores can hold the same
// network twice (the ST30 measured wifiProfileCount=2 against one distinct
// name), and marking only one would present the duplicate as a second, inactive
// network the user does not have.
func markCurrentBySSID(nets []wlanNetwork, name string) []wlanNetwork {
	out := make([]wlanNetwork, len(nets))
	copy(out, nets)
	for i := range out {
		if out[i].Current {
			continue
		}
		if strings.TrimSpace(out[i].SSID) == name {
			out[i].Current = true
			if out[i].Flags == "" {
				out[i].Flags = "[LIVE]"
			} else {
				out[i].Flags += "+[LIVE]"
			}
		}
	}
	return out
}

// liveWLANInterfaces reads the speaker's own association state, or nothing.
//
// Best effort on purpose: this feeds a diagnostic, so a box that will not
// answer /networkInfo must degrade to the old picture rather than fail the
// whole section. The timeout is short because the request is local.
func (s *Server) liveWLANInterfaces(ctx context.Context) []boxapi.NetworkInterface {
	if s.boxHost == "" {
		return nil
	}
	nctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	n, err := boxapi.New(s.boxHost).GetNetwork(nctx)
	if err != nil {
		return nil
	}
	return n.Interfaces
}

// wlanConfiguredDebug is the whole Wi-Fi picture for the diagnostic state:
// both stores, the live association, and the names removed.
func (s *Server) wlanConfiguredDebug(ctx context.Context) wlanConfigured {
	return withLiveAssociation(listConfiguredWLANs(ctx, wpaConfPath), s.liveWLANInterfaces(ctx)).redacted()
}
