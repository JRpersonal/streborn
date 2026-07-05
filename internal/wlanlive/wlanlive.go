// Package wlanlive reads the live Wi-Fi association from the OS on wlan0/wlan1
// (wpa_supplicant) boxes.
//
// STR changes a box's Wi-Fi by rewriting wpa_supplicant directly, bypassing the
// Bose firmware. When it does, Bose's /networkInfo keeps reporting the OLD
// profile (stale SSID / frequency / signal), so the settings screen shows the
// wrong network even though the box is really associated elsewhere. This package
// asks wpa_supplicant itself, via wpa_cli, for the current association and maps
// the RSSI onto STR's signal-class strings so a wlan0 box renders identically to
// a BCO box.
//
// It is deliberately read-only: fixed "wpa_cli" argv (no shell, no untrusted
// interpolation), short exec timeouts so a wedged wpa_supplicant can never block
// a settings fetch. It applies ONLY to wpa boxes; BCO/eth0 boxes have no
// wpa_supplicant and must keep their existing gabbo/provisioned path.
package wlanlive

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// cmdTimeout bounds each wpa_cli invocation. wpa_cli talks to a local ctrl
// socket, so a healthy call returns in milliseconds; a few seconds is a generous
// ceiling that still guarantees a settings fetch never hangs on a wedged daemon.
const cmdTimeout = 3 * time.Second

// Status is the live association read from wpa_supplicant.
type Status struct {
	SSID         string // current SSID, "" if unknown
	FrequencyKHz int    // channel frequency in kHz (wpa_cli reports MHz; *1000), 0 if unknown
	Signal       string // STR signal class (EXCELLENT_SIGNAL/.../POOR_SIGNAL), "" if unknown
	Associated   bool   // wpa_state=COMPLETED
}

// Read returns the live Wi-Fi association on iface via wpa_cli. It runs
// "wpa_cli -i <iface> status" (for SSID + freq) and "wpa_cli -i <iface>
// signal_poll" (for RSSI), each under a short timeout. If the box is not
// associated, or wpa_cli is unavailable, it returns a zero Status
// (Associated=false) so callers fall back to Bose /networkInfo.
//
// Callers MUST gate on a wpa mechanism (e.g. detectWlanMechanism()=="wpa")
// before calling; wpa_cli does not exist on BCO/eth0 boxes.
func Read(ctx context.Context, iface string) Status {
	out, err := runWpaCli(ctx, iface, "status")
	if err != nil {
		return Status{}
	}
	ssid, freqKHz, associated := parseStatus(out)
	if !associated {
		return Status{}
	}
	st := Status{SSID: ssid, FrequencyKHz: freqKHz, Associated: true}

	// Live RSSI via signal_poll (nl80211 driver these boxes use). Falls back to
	// the kernel's /proc/net/wireless if signal_poll is unsupported/FAILs.
	if pollOut, perr := runWpaCli(ctx, iface, "signal_poll"); perr == nil {
		if rssi, ok := parseSignalPoll(pollOut); ok {
			st.Signal = RSSIToClass(rssi)
		}
	}
	if st.Signal == "" {
		if rssi, ok := readProcWireless(iface); ok {
			st.Signal = RSSIToClass(rssi)
		}
	}
	return st
}

// runWpaCli runs "wpa_cli -i <iface> <sub>" under a bounded timeout and returns
// its stdout. Fixed argv, no shell.
func runWpaCli(ctx context.Context, iface, sub string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()
	out, err := exec.CommandContext(cctx, "wpa_cli", "-i", iface, sub).Output()
	return string(out), err
}

// parseStatus extracts ssid, frequency (kHz), and association from the
// key=value lines of "wpa_cli status". ssid/freq are only reported when
// wpa_state=COMPLETED, mirroring what wpaAssociatedTo already relies on.
func parseStatus(out string) (ssid string, freqKHz int, associated bool) {
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "wpa_state":
			associated = v == "COMPLETED"
		case "ssid":
			ssid = v
		case "freq":
			if mhz, err := strconv.Atoi(v); err == nil && mhz > 0 {
				freqKHz = mhz * 1000
			}
		}
	}
	if !associated {
		return "", 0, false
	}
	return ssid, freqKHz, true
}

// parseSignalPoll extracts RSSI (dBm) from "wpa_cli signal_poll" output. It
// rejects values outside a sane Wi-Fi range so a bogus/sentinel reading never
// mislabels the signal.
func parseSignalPoll(out string) (rssi int, ok bool) {
	for _, line := range strings.Split(out, "\n") {
		k, v, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found || k != "RSSI" {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		// Require a strictly negative dBm: a real associated-station RSSI is always
		// below 0. Some drivers emit RSSI=0 as an "unavailable" sentinel, which
		// would otherwise map to EXCELLENT_SIGNAL and show a full bar on a link of
		// unknown strength.
		if err != nil || n >= 0 || n < -100 {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// RSSIToClass buckets an RSSI (dBm) into STR's box-native signal classes so a
// wpa box renders identically to a BCO box in the settings UI. Thresholds are
// the common Wi-Fi quality bands (~-55 excellent, ~-67 reliable, ~-75 the floor
// for reliable service).
func RSSIToClass(rssi int) string {
	switch {
	case rssi >= -55:
		return "EXCELLENT_SIGNAL"
	case rssi >= -67:
		return "GOOD_SIGNAL"
	case rssi >= -75:
		return "MARGINAL_SIGNAL"
	default:
		return "POOR_SIGNAL"
	}
}

// readProcWireless is the wpa_cli-free RSSI fallback: the kernel's
// /proc/net/wireless "level" column is dBm for the cfg80211/nl80211 drivers
// these boxes run.
func readProcWireless(iface string) (int, bool) {
	b, err := os.ReadFile("/proc/net/wireless")
	if err != nil {
		return 0, false
	}
	return parseProcWireless(string(b), iface)
}

// parseProcWireless pulls the level (dBm) for iface from /proc/net/wireless
// content. The line is "iface: status link level noise ..."; level is the third
// numeric column and carries a trailing '.'.
func parseProcWireless(content, iface string) (int, bool) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, iface+":") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, iface+":"))
		fields := strings.Fields(rest)
		if len(fields) < 3 {
			return 0, false
		}
		lvl := strings.TrimSuffix(fields[2], ".")
		n, err := strconv.Atoi(lvl)
		if err != nil || n >= 0 || n < -100 { // strictly negative dBm only (0 = sentinel)
			return 0, false
		}
		return n, true
	}
	return 0, false
}
