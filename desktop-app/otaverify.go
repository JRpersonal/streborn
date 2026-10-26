package main

// Post-OTA verification memory: which agent port the update went through, and
// whether the verify window ended without a verdict.
//
// Two things went wrong on a SoundTouch 10 (rhino, sm2 chassis) on 2026-09-06.
// The update itself went through :8888, the box rebooted, its Wi-Fi came back
// late, and the verify window ran out. The journal then said the box was
// "unreachable" on :17008, a port that chassis DROPs in its own iptables INPUT
// chain, because boxDo reported whichever port it had tried LAST. Two minutes
// later discovery found the box on :8888 running the new build, and nothing
// wrote that down: the journal's last word on a successful update stayed
// "NOT CONFIRMED".
//
// So the update flow now remembers the port that worked, the version poll
// asks that port first on every pass, the transport error leads with that
// port (app_transport.go), and a discovery sighting of the box on the new
// build appends a corrective outcome line.

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// otaVerifyMemo is what the app remembers about one speaker's latest update.
type otaVerifyMemo struct {
	// port is the agent port the update went through (the one the preflight
	// and upload used); zero when unknown.
	port int
	// at is when the update started; the memo expires otaVerifyMemoTTL later.
	at time.Time
	// unconfirmedAt is when the verify window ended without a verdict, zero
	// while the update is still in flight or once it was confirmed.
	unconfirmedAt time.Time
	// verdict is the classification the window ended on ("unreachable",
	// "agent-gone"), for the corrective line.
	verdict string
	// deviceID is the speaker the update was for, taken from discovery when
	// the update started. A router that hands out new addresses on every
	// reboot can move the speaker while it restarts, and give its address to
	// another speaker: the late sighting is then matched by this id, at
	// whatever address the speaker turned up on, and never on the other
	// speaker's record at the old address.
	deviceID string
}

// otaVerifyMemoTTL bounds how long the memo steers the version poll and how
// long a late discovery sighting may still correct the journal. Long enough
// to cover the 6 min verify window, a slow Wi-Fi reconnect and a couple of
// discovery cycles; short enough that a much later update is never confused
// with this one.
const otaVerifyMemoTTL = 30 * time.Minute

// rememberOTAPort records the agent port an update on host is using. Called
// when the update starts (with the caller's best guess) and again once the
// preflight has proven a port, which overrides the guess.
func (a *App) rememberOTAPort(host string, port int) {
	if host == "" || port == 0 {
		return
	}
	id := a.cachedDeviceID(host)
	a.otaVerifyMu.Lock()
	defer a.otaVerifyMu.Unlock()
	if a.otaVerify == nil {
		a.otaVerify = map[string]otaVerifyMemo{}
	}
	a.otaVerify[host] = otaVerifyMemo{port: port, at: time.Now(), deviceID: id}
}

// cachedDeviceID is the deviceID discovery holds for host, or "".
func (a *App) cachedDeviceID(host string) string {
	a.discMu.Lock()
	defer a.discMu.Unlock()
	if e, ok := a.discCache[host]; ok {
		return e.box.DeviceID
	}
	return ""
}

// boxIsDevice reports whether a discovery record names the speaker id.
func boxIsDevice(b BoxInfo, id string) bool {
	if id == "" {
		return false
	}
	return strings.EqualFold(b.DeviceID, id) || (b.BoxDeviceID != "" && strings.EqualFold(b.BoxDeviceID, id))
}

// boxIsOtherDevice reports whether a discovery record provably names a
// different speaker than id: both sides carry an id and they differ.
func boxIsOtherDevice(b BoxInfo, id string) bool {
	return id != "" && b.DeviceID != "" && !boxIsDevice(b, id)
}

// agentPortInUse is the port the app is talking to host's agent on right now:
// the cached port when one is pinned, else the caller's.
func (a *App) agentPortInUse(host string, port int) int {
	if cp, ok := a.cachedPort(host); ok {
		return cp
	}
	return port
}

// postOTAPort returns the port host's most recent update went through, while
// that memo is fresh. The version poll puts it first so a box that dropped
// off the network is asked on the port that will answer, not on the one its
// firewall drops.
func (a *App) postOTAPort(host string) (int, bool) {
	a.otaVerifyMu.Lock()
	defer a.otaVerifyMu.Unlock()
	m, ok := a.otaVerify[host]
	if !ok || m.port == 0 || time.Since(m.at) > otaVerifyMemoTTL {
		return 0, false
	}
	return m.port, true
}

// noteOTAUnconfirmed records that the verify window for host ended on
// verdict without seeing the new build, so a later discovery sighting can
// append the corrective line.
func (a *App) noteOTAUnconfirmed(host, verdict string) {
	if host == "" {
		return
	}
	a.otaVerifyMu.Lock()
	defer a.otaVerifyMu.Unlock()
	if a.otaVerify == nil {
		a.otaVerify = map[string]otaVerifyMemo{}
	}
	m := a.otaVerify[host]
	if m.at.IsZero() {
		m.at = time.Now()
	}
	m.unconfirmedAt = time.Now()
	m.verdict = verdict
	a.otaVerify[host] = m
}

// forgetOTAVerify drops the memo for host: the update was confirmed, or a
// fresh attempt supersedes it.
func (a *App) forgetOTAVerify(host string) {
	a.otaVerifyMu.Lock()
	defer a.otaVerifyMu.Unlock()
	delete(a.otaVerify, host)
}

// confirmLateOTA is called with the boxes a discovery cycle genuinely saw.
// For any of them whose last update ended unconfirmed, it appends one
// corrective outcome line to the journal: "confirmed late by discovery" when
// the box runs this app's build, or what it does run when it does not. Either
// way the memo is cleared, so a journal gets exactly one such line per
// attempt.
//
// presenceOnly names the hosts that answered on the stock :8090 only this
// cycle. RefreshKnownBoxes puts the CACHED record into seen for those, so
// its Kind and Build describe the box as it was before the update, not as
// it is now; treating that as a sighting would journal "runs the OLD build"
// about a box that is merely still booting, and burn the one corrective line
// the memo allows. Such hosts are left for a later cycle.
func (a *App) confirmLateOTA(seen map[string]BoxInfo, presenceOnly map[string]bool) {
	if len(seen) == 0 {
		return
	}
	a.otaVerifyMu.Lock()
	if len(a.otaVerify) == 0 {
		a.otaVerifyMu.Unlock()
		return
	}
	type hit struct {
		host string
		at   string // where the speaker was seen; differs from host after a move
		box  BoxInfo
		memo otaVerifyMemo
	}
	var hits []hit
	now := time.Now()
	for host, m := range a.otaVerify {
		if m.unconfirmedAt.IsZero() {
			continue
		}
		if now.Sub(m.unconfirmedAt) > otaVerifyMemoTTL {
			delete(a.otaVerify, host)
			continue
		}
		// At the address the update ran on, unless discovery shows another
		// speaker there now; then at whatever address this speaker's id turned
		// up on.
		var b BoxInfo
		at, found := host, false
		if sb, ok := seen[host]; ok && !presenceOnly[host] && !boxIsOtherDevice(sb, m.deviceID) {
			b, found = sb, true
		}
		if !found && m.deviceID != "" {
			keys := make([]string, 0, len(seen))
			for k := range seen {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if k == host || presenceOnly[k] || !boxIsDevice(seen[k], m.deviceID) {
					continue
				}
				b, at, found = seen[k], k, true
				break
			}
		}
		if !found || b.Kind != "str" || b.Build == "" {
			continue
		}
		hits = append(hits, hit{host: host, at: at, box: b, memo: m})
		delete(a.otaVerify, host)
	}
	a.otaVerifyMu.Unlock()

	for _, h := range hits {
		late := now.Sub(h.memo.unconfirmedAt).Round(time.Second)
		if h.at != h.host {
			if appBuild != "" && h.box.Build == appBuild {
				a.recordOTA(h.host, fmt.Sprintf("outcome: confirmed late by discovery - the speaker got a new address while it restarted and is on build %s (version %s) at %s:%d, found %s after the verify window ended %s; the earlier NOT CONFIRMED line is superseded",
					h.box.Build, h.box.Version, h.at, h.box.Port, late, h.memo.verdict))
				continue
			}
			a.recordOTA(h.host, fmt.Sprintf("outcome: seen again by discovery at its new address %s, %s after the verify window ended %s - box runs version %s build %s on :%d, not this app's build %s",
				h.at, late, h.memo.verdict, h.box.Version, h.box.Build, h.box.Port, appBuild))
			continue
		}
		if appBuild != "" && h.box.Build == appBuild {
			a.recordOTA(h.host, fmt.Sprintf("outcome: confirmed late by discovery - box is on build %s (version %s), found on :%d %s after the verify window ended %s; the earlier NOT CONFIRMED line is superseded",
				h.box.Build, h.box.Version, h.box.Port, late, h.memo.verdict))
			continue
		}
		a.recordOTA(h.host, fmt.Sprintf("outcome: seen again by discovery %s after the verify window ended %s - box runs version %s build %s on :%d, not this app's build %s",
			late, h.memo.verdict, h.box.Version, h.box.Build, h.box.Port, appBuild))
	}
}
