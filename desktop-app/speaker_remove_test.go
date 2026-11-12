package main

import (
	"testing"
	"time"
)

// answersAt makes the reachability probe of RemoveSpeakerFromList answer for
// the given addresses only, so no test opens a socket.
func answersAt(t *testing.T, hosts ...string) {
	t.Helper()
	old := boxKeyReachableFn
	boxKeyReachableFn = func(b BoxInfo) bool {
		for _, h := range hosts {
			if b.Host == h {
				return true
			}
		}
		return false
	}
	t.Cleanup(func() { boxKeyReachableFn = old })
}

// Taking a silent speaker off the list forgets its ADDRESS everywhere the entry
// could come back from, and nothing that belongs to the speaker itself or to
// another speaker (#1852).
func TestRemoveSpeakerFromListForgetsTheAddressOnly(t *testing.T) {
	isolateConfigDir(t)
	answersAt(t)
	const (
		gone    = "192.0.2.80"
		goneID  = "DEV-GONE"
		stays   = "192.0.2.81"
		staysID = "DEV-STAYS"
	)
	a := moveTestApp()
	a.discCache[gone] = discEntry{box: BoxInfo{Host: gone, DeviceID: goneID, Kind: "str", Port: 8888}, seen: time.Now(), firstMiss: time.Now().Add(-time.Hour)}
	a.discCache[stays] = discEntry{box: BoxInfo{Host: stays, DeviceID: staysID, Kind: "stock", Port: 8090}, seen: time.Now()}
	a.strKnown = map[string]discEntry{goneID: a.discCache[gone]}
	a.rememberPort(gone, 17008)
	a.rememberPort(stays, 8888)
	boxSSHClients.slot(gone)
	boxSSHClients.slot(stays)
	t.Cleanup(func() { boxSSHClients.forgetHost(stays) })
	a.RecordUpdateIntent(gone, 17008, "v9.9.9", goneID, "Gone", true)
	if err := a.SetStereoPairName(goneID+"+"+staysID, "Pair"); err != nil {
		t.Fatal(err)
	}
	a.persistKnownSpeakers()
	if list, ok := readKnownSpeakersFile(t); !ok || len(list) != 2 {
		t.Fatalf("setup: known-speakers.json should list both speakers, got %v (present=%v)", list, ok)
	}

	got, err := a.RemoveSpeakerFromList(" " + gone + " ")
	if err != nil || got != speakerRemoved {
		t.Fatalf("RemoveSpeakerFromList = %q, %v, want %q", got, err, speakerRemoved)
	}

	a.discMu.Lock()
	_, inCache := a.discCache[gone]
	_, inKnown := a.strKnown[goneID]
	_, staysCache := a.discCache[stays]
	a.discMu.Unlock()
	if inCache || inKnown {
		t.Errorf("discovery memory kept the removed entry: cache=%v strKnown=%v", inCache, inKnown)
	}
	if !staysCache {
		t.Error("the other speaker lost its entry")
	}
	if _, ok := a.cachedPort(gone); ok {
		t.Error("the port remembered for the address was kept")
	}
	if p, ok := a.cachedPort(stays); !ok || p != 8888 {
		t.Errorf("the other speaker lost its remembered port: %d %v", p, ok)
	}
	boxSSHClients.mu.Lock()
	_, sshGone := boxSSHClients.hosts[gone]
	_, sshStays := boxSSHClients.hosts[stays]
	boxSSHClients.mu.Unlock()
	if sshGone || !sshStays {
		t.Errorf("SSH slots: removed kept=%v, other kept=%v", sshGone, sshStays)
	}
	if list, ok := readKnownSpeakersFile(t); !ok || len(list) != 1 || list[0].Host != stays {
		t.Errorf("known-speakers.json = %v (present=%v), want only the other speaker", list, ok)
	}

	// What belongs to the speaker, not to the address, stays: the same speaker
	// is usually still there, at another address.
	if names := readStereoNames(); names[goneID+"+"+staysID] != "Pair" {
		t.Errorf("the stereo-pair name was dropped: %v", names)
	}
	if intents := loadUpdateIntentsFrom(configFile(t, "update-intent.json")); len(intents) != 1 {
		t.Errorf("the unfinished update was dropped: %+v", intents)
	}

	// The entry does not come back on the next refresh, and a second removal
	// of the same address is harmless.
	seen := map[string]BoxInfo{}
	a.mergeDiscoveryCache(seen)
	if _, ok := seen[gone]; ok {
		t.Error("the removed entry came back on the next cycle")
	}
	if got, err := a.RemoveSpeakerFromList(gone); err != nil || got != speakerRemoved {
		t.Errorf("second removal = %q, %v", got, err)
	}
}

// The last entry going must leave an empty file, not the stale line.
func TestRemoveSpeakerFromListRewritesAnEmptyFile(t *testing.T) {
	isolateConfigDir(t)
	answersAt(t)
	const gone = "192.0.2.82"
	a := moveTestApp()
	a.discCache[gone] = discEntry{box: BoxInfo{Host: gone, Kind: "stock", Port: 8090}, seen: time.Now()}
	a.persistKnownSpeakers()
	if got, err := a.RemoveSpeakerFromList(gone); err != nil || got != speakerRemoved {
		t.Fatalf("RemoveSpeakerFromList = %q, %v", got, err)
	}
	if list, ok := readKnownSpeakersFile(t); !ok || len(list) != 0 {
		t.Errorf("known-speakers.json = %v (present=%v), want an empty list", list, ok)
	}
}

// A removed speaker that is really found again is listed again.
func TestRemovedSpeakerComesBackWhenFound(t *testing.T) {
	isolateConfigDir(t)
	answersAt(t)
	const ip = "192.0.2.83"
	a := moveTestApp()
	a.discCache[ip] = discEntry{box: BoxInfo{Host: ip, DeviceID: idA, Kind: "str", Port: 8888}, seen: time.Now()}
	if got, _ := a.RemoveSpeakerFromList(ip); got != speakerRemoved {
		t.Fatalf("RemoveSpeakerFromList = %q", got)
	}
	seen := map[string]BoxInfo{ip: {Host: ip, DeviceID: idA, Kind: "str", Port: 8888, PortVerified: true}}
	a.mergeDiscoveryCache(seen)
	if b, ok := seen[ip]; !ok || b.Kind != "str" || b.Offline {
		t.Errorf("the speaker did not come back as found: %+v (present %v)", b, ok)
	}
}

// An entry whose address answers is not removed: it would only reappear, or a
// speaker connected by its address would be lost.
func TestRemoveSpeakerFromListKeepsAnAnsweringSpeaker(t *testing.T) {
	isolateConfigDir(t)
	const ip = "192.0.2.84"
	answersAt(t, ip)
	a := moveTestApp()
	a.discCache[ip] = discEntry{box: BoxInfo{Host: ip, DeviceID: idA, Kind: "str", Port: 8888}, seen: time.Now()}
	a.rememberPort(ip, 8888)
	a.persistKnownSpeakers()

	got, err := a.RemoveSpeakerFromList(ip)
	if err != nil || got != speakerRemoveAnswers {
		t.Fatalf("RemoveSpeakerFromList = %q, %v, want %q", got, err, speakerRemoveAnswers)
	}
	if _, ok := a.discCache[ip]; !ok {
		t.Error("an answering speaker was taken off the list")
	}
	if _, ok := a.cachedPort(ip); !ok {
		t.Error("an answering speaker lost its remembered port")
	}
	if list, ok := readKnownSpeakersFile(t); !ok || len(list) != 1 {
		t.Errorf("known-speakers.json = %v (present=%v), want the speaker kept", list, ok)
	}
}

// A speaker this app is updating is silent on purpose: its entry, and the pin
// that keeps it listed through the restart, stay. Once the pin has run out
// the entry can go.
func TestRemoveSpeakerFromListKeepsASpeakerBeingUpdated(t *testing.T) {
	isolateConfigDir(t)
	answersAt(t)
	const pinnedIP, busyIP = "192.0.2.85", "192.0.2.86"
	a := moveTestApp()
	a.discCache[pinnedIP] = discEntry{box: BoxInfo{Host: pinnedIP, DeviceID: idA, Kind: "str", Port: 8888}, seen: time.Now()}
	a.discCache[busyIP] = discEntry{box: BoxInfo{Host: busyIP, DeviceID: idB, Kind: "stock", Port: 8090}, seen: time.Now()}
	a.notePostOTA(pinnedIP)
	release, err := writesInFlight.claim(busyIP, "an update")
	if err != nil {
		t.Fatal(err)
	}

	for _, ip := range []string{pinnedIP, busyIP} {
		got, err := a.RemoveSpeakerFromList(ip)
		if err != nil || got != speakerRemoveUpdating {
			t.Errorf("%s: RemoveSpeakerFromList = %q, %v, want %q", ip, got, err, speakerRemoveUpdating)
		}
		if _, ok := a.discCache[ip]; !ok {
			t.Errorf("%s: a speaker being updated was taken off the list", ip)
		}
	}
	if _, ok := a.otaPinned[pinnedIP]; !ok {
		t.Error("the post-update pin was dropped")
	}
	seen := map[string]BoxInfo{}
	a.mergeDiscoveryCache(seen)
	if b, ok := seen[pinnedIP]; !ok || !b.OTAPending {
		t.Errorf("the pinned speaker is no longer served as mid-update: %+v (present %v)", b, ok)
	}

	release()
	a.discMu.Lock()
	a.otaPinned[pinnedIP] = time.Now().Add(-otaRebootGrace - time.Minute)
	a.discMu.Unlock()
	for _, ip := range []string{pinnedIP, busyIP} {
		if got, err := a.RemoveSpeakerFromList(ip); err != nil || got != speakerRemoved {
			t.Errorf("%s after the update: RemoveSpeakerFromList = %q, %v, want %q", ip, got, err, speakerRemoved)
		}
	}
}

func TestRemoveSpeakerFromListNeedsAnAddress(t *testing.T) {
	a := moveTestApp()
	if _, err := a.RemoveSpeakerFromList("  "); err == nil {
		t.Error("an empty address was accepted")
	}
}
