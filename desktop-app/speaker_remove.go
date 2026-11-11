package main

// Taking a speaker that no longer answers off the list by hand (#1852).
//
// A speaker that got a new address is tied to its old entry by its deviceID,
// and the old entry goes by itself (readdOrEvictLocked). That needs the
// speaker to be FOUND at the new address, though. Across a routed network
// nothing finds it: no mDNS, and the sweep only covers this PC's own subnets.
// The entry at the old address then stays greyed out for
// discoveryOfflineRetention, with nothing the owner could do about it.
//
// RemoveSpeakerFromList is that way out. It forgets the ADDRESS: the discovery
// cache entry, the line in known-speakers.json, the agent port and the SSH
// slot remembered for the host. It contacts no speaker beyond asking whether
// the address answers, changes nothing on one, and keeps what belongs to the
// speaker rather than to the address (stereo-pair names, the preset stash, an
// unfinished update, the update history), because the same speaker usually is
// still there, at another address. purgeSpeakerState is the place that forgets
// a speaker for good.
//
// A speaker that is found again simply comes back into the list.

import (
	"fmt"
	"strings"
	"time"
)

// The answers of RemoveSpeakerFromList. The frontend words them
// (speakerremove.js).
const (
	speakerRemoved        = "removed"
	speakerRemoveAnswers  = "reachable"
	speakerRemoveUpdating = "updating"
)

// RemoveSpeakerFromList takes the entry at host off the speaker list, when its
// speaker does not answer. It returns speakerRemoved, or why the entry stays:
// speakerRemoveUpdating while an install or update from this app is running or
// finishing on the host (the entry is what keeps that speaker visible through
// its restart, see otaPinned), speakerRemoveAnswers when something answers at
// the address after all (the caller refreshes the list instead: removing a
// speaker that answers would only make it reappear, or lose one that was
// connected by its address).
func (a *App) RemoveSpeakerFromList(host string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", fmt.Errorf("no speaker address given")
	}
	a.discMu.Lock()
	pinnedAt, pinned := a.otaPinned[host]
	pinned = pinned && time.Since(pinnedAt) <= otaRebootGrace
	rec := BoxInfo{Host: host}
	if e, ok := a.discCache[host]; ok {
		rec = e.box
		rec.Host = host
	}
	a.discMu.Unlock()
	if pinned || writesInFlight.isBusy(host) {
		a.logger.Info("remove speaker from list: refused, an install or update is running on it", "host", host)
		return speakerRemoveUpdating, nil
	}
	if boxKeyReachableFn(rec) {
		a.logger.Info("remove speaker from list: refused, the address answers", "host", host)
		return speakerRemoveAnswers, nil
	}
	a.forgetSTRDeviceByHost(host)
	a.forgetPort(host)
	boxSSHClients.forgetHost(host)
	// Forced, like after an uninstall: taking the last entry off must write an
	// empty list, or the next start probes the dead address again.
	a.persistKnownSpeakersWith(true)
	a.logger.Info("remove speaker from list: entry forgotten", "host", host, "deviceID", rec.DeviceID)
	return speakerRemoved, nil
}
