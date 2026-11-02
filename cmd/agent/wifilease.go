package main

// Wi-Fi lease recovery: a speaker that stays associated but gets no address.
//
// Field case (SoundTouch 10, TI wl18xx, agent v1.3.0, bundle 2026-11-02): after
// 28 h of uptime the network forensics logged "LAN IP disappeared; default
// gateway fell out of the ARP table" and the box never came back on its own.
// A day later its syslog still showed the radio associated (WIFI_STATION_
// CONNECTED, -65 dBm, group rekeys with the access point completing normally)
// while the firmware's own DHCP client (NetManager's udhcpc on wlan0) sent a
// DISCOVER every three seconds into silence. The owner re-ran setup, the box
// joined a DIFFERENT access point of the same mesh network and got its address
// at once.
//
// So the radio link was fine and the access point it was stuck on had stopped
// passing its DHCP traffic. Nothing on the box ever reconsiders the access
// point while the association holds, so the state can last indefinitely.
//
// The recovery is the smallest lever that reaches it: ask the running
// wpa_supplicant to reassociate. That makes it scan, pick the best access point
// again and re-run the handshake, and the firmware's DHCP client starts over on
// the fresh association. From the second attempt on, the access point the box
// was stuck on is first put on the supplicant's in-memory ignore list, so a
// mesh can hand it to another node. That list is self-clearing inside
// wpa_supplicant (when no other candidate is found it clears it and retries),
// so a single-AP home simply reassociates to the same one, and STR clears the
// entry again as soon as the lease is back.
//
// Hands-off rules, because the radio belongs to the firmware:
//   - never touches /etc/wpa_supplicant.conf, never adds, removes or selects a
//     network, never touches the setup access point;
//   - acts only while wpa_supplicant reports COMPLETED in station mode on a
//     configured network, the interface has carrier, the speaker is not in its
//     own setup (hostapd / setupAPUpdated / source SETUP) and no STR Wi-Fi
//     change is running;
//   - only after the lease has been missing for leaseLossThreshold;
//   - at most once per leaseAttemptGap, and once per leaseBackoffGap after
//     leaseBackoffAfter attempts in a row that did not bring the lease back.
//
// Cost: it rides the existing 5-minute network forensics tick. A healthy tick is
// two os.Stat calls, two /proc reads and one interface lookup, no exec and no
// log line above Debug. wpa_cli runs only once a decision is due. State lives in
// memory only (no NAND write); every attempt and its outcome is logged at WARN
// and exposed as the wifi_lease_recovery debug section.

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// leaseLossThreshold is how long the speaker must have been without a
	// usable lease before STR touches the association. Long enough that a
	// router reboot or a slow DHCP server sorts itself out first.
	leaseLossThreshold = 10 * time.Minute
	// leaseAttemptGap is the minimum gap between two reassociations.
	leaseAttemptGap = 30 * time.Minute
	// leaseBackoffAfter attempts in a row without a lease switch the gap to
	// leaseBackoffGap: whatever is wrong is not something a reassociation
	// fixes, and the radio should be left alone most of the time.
	leaseBackoffAfter = 3
	leaseBackoffGap   = 2 * time.Hour
	// leaseHistoryLen bounds the attempt history kept for the debug section.
	leaseHistoryLen = 8
)

// leaseObservation is one cheap reading of the Wi-Fi lease, taken without exec.
type leaseObservation struct {
	Iface    string // the wpa_supplicant interface, "" when this chassis has none
	Carrier  bool   // IFF_RUNNING on Iface
	HasLease bool   // the speaker has a usable IPv4 path to its gateway
	Reason   string // why HasLease is false (or what makes it true), for the log
}

// wpaStatus is the parsed part of "wpa_cli status" the decision needs.
type wpaStatus struct {
	State string // wpa_state
	Mode  string // mode (station / AP / ...), "" on supplicants that omit it
	SSID  string // only checked for presence, never logged or exposed
	ID    string // network id; present when a configured network is selected
	BSSID string // only used as an argument to the ignore list, never logged
}

// parseWPAStatus reads the key=value lines of "wpa_cli status".
func parseWPAStatus(raw string) wpaStatus {
	var st wpaStatus
	for _, line := range strings.Split(raw, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "wpa_state":
			st.State = v
		case "mode":
			st.Mode = v
		case "ssid":
			st.SSID = v
		case "id":
			st.ID = v
		case "bssid":
			st.BSSID = v
		}
	}
	return st
}

// wpaCommandOK reports whether a wpa_cli control command was accepted. wpa_cli
// exits 0 even for "FAIL" and "UNKNOWN COMMAND", so the output decides.
func wpaCommandOK(out string, err error) bool {
	return err == nil && strings.TrimSpace(out) == "OK"
}

// leaseAttempt is one reassociation, kept for the debug section.
type leaseAttempt struct {
	At          time.Time `json:"at"`
	NoLeaseMin  int       `json:"noLeaseMin"`
	IgnoredAP   bool      `json:"ignoredStuckAP"` // the stuck AP was put on the ignore list first
	CommandOK   bool      `json:"commandOK"`
	Outcome     string    `json:"outcome"` // pending | lease back | still no lease | command failed
	OutcomeAtIn int       `json:"outcomeAfterSec,omitempty"`
}

// leaseRecovery holds the in-memory state of the watcher. All decisions go
// through check, which is pure apart from the injected seams.
type leaseRecovery struct {
	logger *slog.Logger
	now    func() time.Time
	// observe reads the lease state (no exec).
	observe func() leaseObservation
	// wpa runs "wpa_cli -i <iface> args..." and returns its output. errWPAMissing
	// means there is no wpa_cli on this speaker.
	wpa func(iface string, args ...string) (string, error)
	// inSetup reports whether the speaker is in its own setup mode.
	inSetup func() bool
	// wlanBusy reports whether an STR Wi-Fi change is running right now.
	wlanBusy func() bool

	mu sync.Mutex
	// unsupported is set once this chassis turned out to have nothing to act
	// on; it is logged once and the watcher goes quiet.
	unsupported string
	// noLeaseSince is the first tick of the current lease-less stretch.
	noLeaseSince time.Time
	lastAttempt  time.Time
	// pending is true between an attempt and the next tick that judges it.
	pending bool
	// failedInARow counts attempts that did not bring the lease back.
	failedInARow int
	attempts     int
	recovered    int
	// ignoredBSSID is set while STR keeps an access point on the
	// supplicant's ignore list, so it can be cleared once the lease is back.
	ignoredBSSID bool
	ignoredIface string
	lastSkip     string
	history      []leaseAttempt
}

var errWPAMissing = errors.New("no wpa_cli on this speaker")

// newLeaseRecovery wires the real seams. inSetup and wlanBusy may be nil.
func newLeaseRecovery(logger *slog.Logger, inSetup, wlanBusy func() bool) *leaseRecovery {
	if inSetup == nil {
		inSetup = func() bool { return false }
	}
	if wlanBusy == nil {
		wlanBusy = func() bool { return false }
	}
	return &leaseRecovery{
		logger:   logger,
		now:      time.Now,
		observe:  observeLease,
		wpa:      runWPACLI,
		inSetup:  func() bool { return inSetup() || hostapdRunning("/proc") },
		wlanBusy: wlanBusy,
	}
}

// check is called on every network forensics tick.
func (r *leaseRecovery) check() {
	r.step(r.observe())
}

// step is the whole decision for one tick.
func (r *leaseRecovery) step(obs leaseObservation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.unsupported != "" {
		return
	}
	if obs.Iface == "" {
		r.markUnsupported("no wpa_supplicant Wi-Fi interface (wlan0/wlan1) on this chassis")
		return
	}
	now := r.now()

	if obs.HasLease {
		if r.pending {
			r.judgeLast(now, true)
		}
		if r.ignoredBSSID {
			out, err := r.wpa(r.ignoredIface, "blacklist", "clear")
			if !wpaCommandOK(out, err) {
				out, err = r.wpa(r.ignoredIface, "bssid_ignore", "clear")
			}
			r.logger.Warn("wifi lease recovery: lease is back, cleared the access point STR had put on the ignore list",
				"iface", r.ignoredIface, "ok", wpaCommandOK(out, err))
			r.ignoredBSSID = false
		}
		if !r.noLeaseSince.IsZero() {
			r.logger.Debug("wifi lease recovery: lease present again", "iface", obs.Iface,
				"noLeaseFor", now.Sub(r.noLeaseSince).Round(time.Second).String())
		}
		r.noLeaseSince = time.Time{}
		r.failedInARow = 0
		r.lastSkip = ""
		return
	}

	if r.noLeaseSince.IsZero() {
		r.noLeaseSince = now
		r.logger.Info("wifi lease recovery: no usable address on the Wi-Fi; will ask Wi-Fi to reconnect if it stays like this",
			"iface", obs.Iface, "reason", obs.Reason, "after", leaseLossThreshold.String())
		return
	}
	if r.pending {
		r.judgeLast(now, false)
	}
	without := now.Sub(r.noLeaseSince)
	if without < leaseLossThreshold {
		return
	}
	gap := leaseAttemptGap
	if r.failedInARow >= leaseBackoffAfter {
		gap = leaseBackoffGap
	}
	if !r.lastAttempt.IsZero() && now.Sub(r.lastAttempt) < gap {
		r.skip("rate limited (next attempt in "+(gap-now.Sub(r.lastAttempt)).Round(time.Minute).String()+")", slog.LevelDebug, obs)
		return
	}
	if !obs.Carrier {
		r.skip("interface has no carrier (not associated; the firmware's own reconnect is in charge)", slog.LevelInfo, obs)
		return
	}
	if r.inSetup() {
		r.skip("speaker is in setup mode, hands off the radio", slog.LevelInfo, obs)
		return
	}
	if r.wlanBusy() {
		r.skip("an STR Wi-Fi change is running", slog.LevelInfo, obs)
		return
	}
	raw, err := r.wpa(obs.Iface, "status")
	if errors.Is(err, errWPAMissing) {
		r.markUnsupported("no wpa_cli on this speaker, the radio is not driven by wpa_supplicant here")
		return
	}
	if err != nil || strings.Contains(raw, "Failed to connect") {
		r.skip("wpa_cli status did not answer", slog.LevelWarn, obs)
		return
	}
	st := parseWPAStatus(raw)
	switch {
	case st.Mode != "" && !strings.EqualFold(st.Mode, "station"):
		r.skip("Wi-Fi interface is not in station mode ("+st.Mode+"), hands off", slog.LevelInfo, obs)
		return
	case st.State != "COMPLETED":
		r.skip("wpa_supplicant is not associated (wpa_state="+st.State+"), the firmware's own reconnect is in charge", slog.LevelInfo, obs)
		return
	case st.SSID == "" || st.ID == "":
		r.skip("no configured network is selected", slog.LevelInfo, obs)
		return
	}

	r.attempt(now, obs, st, without)
}

// attempt performs one reassociation. Caller holds r.mu.
func (r *leaseRecovery) attempt(now time.Time, obs leaseObservation, st wpaStatus, without time.Duration) {
	r.attempts++
	r.lastAttempt = now
	r.lastSkip = ""
	rec := leaseAttempt{At: now, NoLeaseMin: int(without.Minutes())}

	// From the second attempt in a row on, the plain reassociation has already
	// failed to move the box off the access point it is stuck on, so put that
	// one on the supplicant's in-memory ignore list first. "blacklist" is the
	// spelling every wpa_cli generation accepts; 2.10+ also knows it as
	// bssid_ignore.
	if r.failedInARow >= 1 && st.BSSID != "" {
		out, err := r.wpa(obs.Iface, "blacklist", st.BSSID)
		if !wpaCommandOK(out, err) {
			out, err = r.wpa(obs.Iface, "bssid_ignore", st.BSSID)
		}
		if wpaCommandOK(out, err) {
			rec.IgnoredAP = true
			r.ignoredBSSID = true
			r.ignoredIface = obs.Iface
		}
	}

	out, err := r.wpa(obs.Iface, "reassociate")
	rec.CommandOK = wpaCommandOK(out, err)
	if rec.CommandOK {
		rec.Outcome = "pending"
		r.pending = true
	} else {
		rec.Outcome = "command failed"
		r.failedInARow++
	}
	r.pushHistory(rec)
	r.logger.Warn("wifi lease recovery: Wi-Fi is associated but the speaker has had no address for "+
		without.Round(time.Minute).String()+", asked wpa_supplicant to reassociate",
		"iface", obs.Iface, "reason", obs.Reason, "attempt", r.attempts,
		"failedInARow", r.failedInARow, "ignoredStuckAP", rec.IgnoredAP,
		"commandOK", rec.CommandOK, "reply", strings.TrimSpace(out), "err", errString(err))
}

// judgeLast records how the pending attempt ended. Caller holds r.mu.
func (r *leaseRecovery) judgeLast(now time.Time, leaseBack bool) {
	r.pending = false
	if len(r.history) == 0 {
		return
	}
	last := &r.history[len(r.history)-1]
	last.OutcomeAtIn = int(now.Sub(last.At).Seconds())
	if leaseBack {
		last.Outcome = "lease back"
		r.recovered++
		r.logger.Warn("wifi lease recovery: the speaker has an address again after the reassociation",
			"attempt", r.attempts, "after", now.Sub(last.At).Round(time.Second).String())
		return
	}
	last.Outcome = "still no lease"
	r.failedInARow++
	next := leaseAttemptGap
	if r.failedInARow >= leaseBackoffAfter {
		next = leaseBackoffGap
	}
	r.logger.Warn("wifi lease recovery: still no address after the reassociation",
		"attempt", r.attempts, "failedInARow", r.failedInARow, "nextGap", next.String())
}

// skip records why a due attempt did not happen, logging only on a change of
// reason so a long stand-down costs one line, not one per tick.
func (r *leaseRecovery) skip(reason string, level slog.Level, obs leaseObservation) {
	if reason == r.lastSkip {
		return
	}
	if strings.HasPrefix(reason, "rate limited") && strings.HasPrefix(r.lastSkip, "rate limited") {
		r.lastSkip = reason
		return
	}
	r.lastSkip = reason
	r.logger.Log(context.Background(), level, "wifi lease recovery: no lease but not acting: "+reason,
		"iface", obs.Iface, "reason", obs.Reason)
}

func (r *leaseRecovery) markUnsupported(why string) {
	r.unsupported = why
	r.logger.Info("wifi lease recovery: disabled on this speaker: " + why)
}

func (r *leaseRecovery) pushHistory(a leaseAttempt) {
	r.history = append(r.history, a)
	if len(r.history) > leaseHistoryLen {
		r.history = r.history[len(r.history)-leaseHistoryLen:]
	}
}

// debugSection is the wifi_lease_recovery bundle section. No addresses, no
// network names, no BSSIDs.
func (r *leaseRecovery) debugSection() any {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	out := map[string]any{
		"supported":    r.unsupported == "",
		"attempts":     r.attempts,
		"recovered":    r.recovered,
		"failedInARow": r.failedInARow,
		"backoff":      r.failedInARow >= leaseBackoffAfter,
		"pending":      r.pending,
		"thresholdMin": int(leaseLossThreshold.Minutes()),
	}
	if r.unsupported != "" {
		out["unsupportedReason"] = r.unsupported
	}
	if !r.noLeaseSince.IsZero() {
		out["noLeaseForSec"] = int(now.Sub(r.noLeaseSince).Seconds())
	}
	if !r.lastAttempt.IsZero() {
		out["lastAttemptAgoSec"] = int(now.Sub(r.lastAttempt).Seconds())
	}
	if r.lastSkip != "" {
		out["lastSkip"] = r.lastSkip
	}
	h := make([]leaseAttempt, len(r.history))
	copy(h, r.history)
	out["history"] = h
	return out
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ---- real seams -----------------------------------------------------------

// wpaCLIPath resolves wpa_cli: PATH first, then /usr/local/sbin, which is where
// the rhino/sm2 chassis keeps it and which is not on every PATH.
func wpaCLIPath() string {
	if p, err := exec.LookPath("wpa_cli"); err == nil {
		return p
	}
	if fi, err := os.Stat("/usr/local/sbin/wpa_cli"); err == nil && !fi.IsDir() {
		return "/usr/local/sbin/wpa_cli"
	}
	return ""
}

func runWPACLI(iface string, args ...string) (string, error) {
	tool := wpaCLIPath()
	if tool == "" {
		return "", errWPAMissing
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, tool, append([]string{"-i", iface}, args...)...).CombinedOutput()
	return string(out), err
}

// wpaInterface is the wpa_supplicant-driven interface, mirroring run.sh's and
// the webui's detection: wlan0 first, then wlan1. "" on the coprocessor
// chassis (scm/BCO), where the Wi-Fi chip is presented as eth0.
func wpaInterface(sysNet string) string {
	for _, w := range []string{"wlan0", "wlan1"} {
		if _, err := os.Stat(filepath.Join(sysNet, w)); err == nil {
			return w
		}
	}
	return ""
}

// hostapdRunning reports whether a hostapd process exists, the firmware's setup
// access point. Only called when an attempt is due, never on a healthy tick.
func hostapdRunning(proc string) bool {
	ents, err := os.ReadDir(proc)
	if err != nil {
		return false
	}
	for _, e := range ents {
		n := e.Name()
		if n == "" || n[0] < '0' || n[0] > '9' {
			continue
		}
		b, err := os.ReadFile(filepath.Join(proc, n, "comm"))
		if err == nil && strings.TrimSpace(string(b)) == "hostapd" {
			return true
		}
	}
	return false
}

// observeLease takes the cheap reading: /proc/net/route, /proc/net/arp and the
// interface's own flags and addresses. No exec.
func observeLease() leaseObservation {
	iface := wpaInterface("/sys/class/net")
	if iface == "" {
		return leaseObservation{}
	}
	obs := leaseObservation{Iface: iface}
	var wifiV4 []string
	if ifc, err := net.InterfaceByName(iface); err == nil {
		obs.Carrier = ifc.Flags&net.FlagUp != 0 && ifc.Flags&net.FlagRunning != 0
		wifiV4 = ifaceV4(ifc)
	}
	gw, gwIface := defaultRouteV4()
	otherUsable := false
	if gwIface != "" && gwIface != iface {
		if ifc, err := net.InterfaceByName(gwIface); err == nil {
			otherUsable = anyUsableV4(ifaceV4(ifc))
		}
	}
	obs.HasLease, obs.Reason = leaseVerdict(iface, wifiV4, gw, gwIface, gatewayARPState(gw), otherUsable)
	return obs
}

func ifaceV4(ifc *net.Interface) []string {
	var v4 []string
	addrs, _ := ifc.Addrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			if ip := ipn.IP.To4(); ip != nil {
				v4 = append(v4, ip.String())
			}
		}
	}
	return v4
}

// anyUsableV4 reports whether one of the addresses is a real lease: not
// unspecified, loopback or link-local (169.254/16, the "no DHCP" fallback).
func anyUsableV4(v4s []string) bool {
	for _, s := range v4s {
		ip := net.ParseIP(s)
		if ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		return true
	}
	return false
}

// leaseVerdict decides whether the speaker has a usable IPv4 path. Pure.
//
// A default route through ANOTHER interface that carries a usable address (a
// network cable) counts as healthy: the cable carries the box and the radio is
// not STR's business then. A gateway entry that is merely "absent" from the ARP
// table does not count as a failure on its own, because an idle box may simply
// not have talked to it; "incomplete" means it was asked and did not answer.
func leaseVerdict(iface string, wifiV4 []string, gw, gwIface, gwARP string, otherIfaceUsable bool) (bool, string) {
	if gwIface != "" && gwIface != iface && otherIfaceUsable {
		return true, "default route via " + gwIface
	}
	if !anyUsableV4(wifiV4) {
		return false, "no usable IPv4 address on " + iface
	}
	if gw == "" || gwIface != iface {
		return false, "no IPv4 default route via " + iface
	}
	if gwARP == "incomplete" {
		return false, "default gateway does not answer ARP"
	}
	return true, "lease ok"
}
