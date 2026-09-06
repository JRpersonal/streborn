package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// isolateConfigDir points os.UserConfigDir at a temp dir for the test, on every
// platform it is read from: %AppData% on Windows, $XDG_CONFIG_HOME on Linux,
// $HOME/Library/Application Support on macOS. The stores under test
// (known-speakers.json, update-intent.json, stereo-pair-names.json,
// app-state.json) all resolve their path through it, so nothing touches the
// developer's real "ST Reborn" folder.
func isolateConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AppData", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	return dir
}

func configFile(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "ST Reborn", name)
}

func readKnownSpeakersFile(t *testing.T) ([]knownSpeaker, bool) {
	t.Helper()
	b, err := os.ReadFile(configFile(t, "known-speakers.json"))
	if err != nil {
		return nil, false
	}
	var list []knownSpeaker
	if err := json.Unmarshal(b, &list); err != nil {
		t.Fatalf("known-speakers.json does not parse: %v", err)
	}
	return list, true
}

// A removed speaker must leave nothing behind on this PC: not in memory, not
// on disk. Every record the app keeps per speaker is planted here, for the
// removed speaker AND for a bystander, and the purge must take exactly the
// removed one.
func TestPurgeSpeakerStateDropsEveryRecord(t *testing.T) {
	isolateConfigDir(t)
	const (
		gone     = "192.0.2.10"
		goneID   = "DEV-GONE"
		stays    = "192.0.2.11"
		staysID  = "DEV-STAYS"
		thirdID  = "DEV-THIRD"
		invited  = "str.worldMapInvited"
		provGone = worldMapProvisionedFlagPrefix + goneID
		provHost = worldMapProvisionedFlagPrefix + gone
		provStay = worldMapProvisionedFlagPrefix + staysID
	)
	a := &App{logger: slog.Default()}
	a.discCache = map[string]discEntry{
		gone:  {box: BoxInfo{Host: gone, DeviceID: goneID, Kind: "str", Port: 8888}, seen: time.Now()},
		stays: {box: BoxInfo{Host: stays, DeviceID: staysID, Kind: "stock", Port: 8090}, seen: time.Now()},
	}
	a.strKnown = map[string]discEntry{
		goneID:  a.discCache[gone],
		staysID: a.discCache[stays],
	}
	a.rememberPort(gone, 17008)
	a.rememberPort(stays, 8888)
	a.notePostOTA(gone)
	a.notePostOTA(stays)
	boxSSHClients.slot(gone)
	boxSSHClients.slot(stays)
	t.Cleanup(func() { boxSSHClients.forgetHost(stays) })

	a.RecordUpdateIntent(gone, 17008, "v9.9.9", goneID, "Gone", true)
	a.RecordUpdateIntent(stays, 8888, "v9.9.9", staysID, "Stays", true)
	for _, n := range []string{invited, provGone, provHost, provStay} {
		if err := a.SetAppFlag(n); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.SetStereoPairName(goneID+"+"+staysID, "Old pair"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetStereoPairName(staysID+"+"+thirdID, "Other pair"); err != nil {
		t.Fatal(err)
	}
	a.persistKnownSpeakers()
	if list, ok := readKnownSpeakersFile(t); !ok || len(list) != 2 {
		t.Fatalf("setup: known-speakers.json should list both speakers, got %v (present=%v)", list, ok)
	}

	// deviceID left empty on purpose: UninstallSTR only knows the host, the
	// purge has to resolve the identity from the discovery memory itself.
	a.purgeSpeakerState(gone, "")

	// In memory: the removed speaker is gone, the bystander untouched.
	a.discMu.Lock()
	_, inCache := a.discCache[gone]
	_, inKnown := a.strKnown[goneID]
	_, pinned := a.otaPinned[gone]
	_, staysCache := a.discCache[stays]
	_, staysKnown := a.strKnown[staysID]
	_, staysPinned := a.otaPinned[stays]
	a.discMu.Unlock()
	if inCache || inKnown || pinned {
		t.Errorf("discovery memory kept the removed speaker: cache=%v strKnown=%v otaPinned=%v", inCache, inKnown, pinned)
	}
	if !staysCache || !staysKnown || !staysPinned {
		t.Errorf("purge took the bystander's discovery memory: cache=%v strKnown=%v otaPinned=%v", staysCache, staysKnown, staysPinned)
	}
	if _, ok := a.cachedPort(gone); ok {
		t.Error("portCache kept the removed speaker")
	}
	if p, ok := a.cachedPort(stays); !ok || p != 8888 {
		t.Errorf("portCache lost the bystander: %d %v", p, ok)
	}
	boxSSHClients.mu.Lock()
	_, sshGone := boxSSHClients.hosts[gone]
	_, sshStays := boxSSHClients.hosts[stays]
	boxSSHClients.mu.Unlock()
	if sshGone {
		t.Error("SSH client cache kept the removed speaker's slot")
	}
	if !sshStays {
		t.Error("SSH client cache lost the bystander's slot")
	}

	// On disk: update-intent.json.
	intents := loadUpdateIntentsFrom(configFile(t, "update-intent.json"))
	if len(intents) != 1 || intents[0].Host != stays {
		t.Errorf("update-intent.json = %+v, want only the bystander", intents)
	}
	// stereo-pair-names.json: only the pair the removed speaker was half of.
	names := readStereoNames()
	if _, ok := names[goneID+"+"+staysID]; ok {
		t.Error("stereo-pair-names.json kept the removed speaker's pair name")
	}
	if names[staysID+"+"+thirdID] != "Other pair" {
		t.Errorf("stereo-pair-names.json lost an unrelated pair: %v", names)
	}
	// app-state.json: only the per-speaker flags, both spellings.
	flags := readAppFlags()
	if flags[provGone] || flags[provHost] {
		t.Errorf("app-state.json kept the removed speaker's flags: %v", flags)
	}
	if !flags[invited] || !flags[provStay] {
		t.Errorf("app-state.json lost flags that are not about the removed speaker: %v", flags)
	}
	// known-speakers.json: rewritten without the removed speaker.
	list, ok := readKnownSpeakersFile(t)
	if !ok || len(list) != 1 || list[0].Host != stays {
		t.Errorf("known-speakers.json = %v (present=%v), want only the bystander", list, ok)
	}
}

// The speaker may have moved IPs since the app last saw it as STR: the
// identity memory then carries the deviceID under the OLD host and the
// per-IP cache has nothing for the host being removed. The purge still finds
// and drops it.
func TestPurgeSpeakerStateResolvesTheDeviceIDFromTheIdentityMemory(t *testing.T) {
	isolateConfigDir(t)
	const host, oldHost, id = "192.0.2.20", "192.0.2.21", "DEV-MOVED"
	a := &App{logger: slog.Default()}
	a.discCache = map[string]discEntry{
		oldHost: {box: BoxInfo{Host: oldHost, DeviceID: id, Kind: "str"}, seen: time.Now()},
	}
	a.strKnown = map[string]discEntry{
		id: {box: BoxInfo{Host: host, DeviceID: id, Kind: "str"}, seen: time.Now()},
	}
	if err := a.SetAppFlag(worldMapProvisionedFlagPrefix + id); err != nil {
		t.Fatal(err)
	}

	a.purgeSpeakerState(host, "")

	a.discMu.Lock()
	_, inKnown := a.strKnown[id]
	_, oldInCache := a.discCache[oldHost]
	a.discMu.Unlock()
	if inKnown {
		t.Error("strKnown kept the moved speaker")
	}
	if oldInCache {
		t.Error("the cache entry under the speaker's old IP survived")
	}
	if readAppFlags()[worldMapProvisionedFlagPrefix+id] {
		t.Error("the deviceID-keyed app flag survived, so the deviceID was not resolved")
	}
}

// Removing the LAST speaker must write an empty list. The discovery path's
// guard against wiping the file on an empty cycle stays in place.
func TestPurgeWritesAnEmptyKnownSpeakersList(t *testing.T) {
	isolateConfigDir(t)
	const host, id = "192.0.2.30", "DEV-LAST"
	a := &App{logger: slog.Default()}
	a.discCache = map[string]discEntry{
		host: {box: BoxInfo{Host: host, DeviceID: id, Kind: "str"}, seen: time.Now()},
	}
	a.persistKnownSpeakers()
	if list, ok := readKnownSpeakersFile(t); !ok || len(list) != 1 {
		t.Fatalf("setup: known-speakers.json should list the speaker, got %v (present=%v)", list, ok)
	}

	a.purgeSpeakerState(host, id)

	list, ok := readKnownSpeakersFile(t)
	if !ok {
		t.Fatal("known-speakers.json vanished; it should be rewritten as an empty list")
	}
	if len(list) != 0 {
		t.Errorf("known-speakers.json still lists %v after the last speaker was removed", list)
	}

	// Discovery-driven persist with an empty cache must NOT wipe a file that
	// lists speakers: that file is the cold-start safety net.
	path := configFile(t, "known-speakers.json")
	if err := saveKnownSpeakersTo(path, []knownSpeaker{{Host: host, DeviceID: id, Kind: "str"}}); err != nil {
		t.Fatal(err)
	}
	a.knownSpeakersMu.Lock()
	a.knownSpeakersWritten = "something-else"
	a.knownSpeakersMu.Unlock()
	a.persistKnownSpeakers()
	if list, _ := readKnownSpeakersFile(t); len(list) != 1 {
		t.Errorf("an empty discovery cycle wiped known-speakers.json: %v", list)
	}
}

func TestRemoveStereoNamesFor(t *testing.T) {
	isolateConfigDir(t)
	a := &App{logger: slog.Default()}
	for key, name := range map[string]string{
		"AAA+BBB": "Living room",
		"BBB+CCC": "Office",
		"CCC+DDD": "Kitchen",
	} {
		if err := a.SetStereoPairName(key, name); err != nil {
			t.Fatal(err)
		}
	}

	// Case-insensitive on the member, exact on the segment: "BB" is not "BBB".
	n, err := removeStereoNamesFor("bbb")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("removed %d names, want 2", n)
	}
	got := readStereoNames()
	if len(got) != 1 || got["CCC+DDD"] != "Kitchen" {
		t.Errorf("store after removal = %v, want only CCC+DDD", got)
	}

	// No match: nothing written, no error.
	if n, err := removeStereoNamesFor("BB"); err != nil || n != 0 {
		t.Errorf("partial-segment match removed %d names (err %v), want 0", n, err)
	}
	if n, err := removeStereoNamesFor(""); err != nil || n != 0 {
		t.Errorf("empty deviceID removed %d names (err %v), want 0", n, err)
	}
	// A corrupt store is refused, never clobbered.
	if err := os.WriteFile(configFile(t, "stereo-pair-names.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := removeStereoNamesFor("CCC"); err == nil {
		t.Error("a corrupt store should be refused")
	}
	if b, _ := os.ReadFile(configFile(t, "stereo-pair-names.json")); string(b) != "{not json" {
		t.Error("a corrupt store was overwritten")
	}
}

func TestDeleteAppFlags(t *testing.T) {
	isolateConfigDir(t)
	a := &App{logger: slog.Default()}
	path := configFile(t, "app-state.json")

	// Deleting from a store that does not exist creates nothing.
	if err := deleteAppFlags("str.worldMapProvisioned.DEV-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("deleting an unset flag created app-state.json")
	}

	for _, n := range []string{"str.worldMapInvited", "str.worldMapProvisioned.DEV-1", "str.worldMapProvisioned.DEV-2"} {
		if err := a.SetAppFlag(n); err != nil {
			t.Fatal(err)
		}
	}
	if err := deleteAppFlags("str.worldMapProvisioned.DEV-1", "never-set"); err != nil {
		t.Fatal(err)
	}
	flags := readAppFlags()
	if flags["str.worldMapProvisioned.DEV-1"] {
		t.Error("the deleted flag is still set")
	}
	if !flags["str.worldMapInvited"] || !flags["str.worldMapProvisioned.DEV-2"] {
		t.Errorf("unrelated flags were lost: %v", flags)
	}
	// SetAppFlag stays one-way for its callers: setting again after a delete
	// works, and a second delete of an absent name is a no-op.
	if err := a.SetAppFlag("str.worldMapProvisioned.DEV-1"); err != nil {
		t.Fatal(err)
	}
	if !a.GetAppFlag("str.worldMapProvisioned.DEV-1") {
		t.Error("a flag deleted once can no longer be set")
	}
}

func TestWorldMapProvisionedFlags(t *testing.T) {
	got := worldMapProvisionedFlags("192.0.2.5", "DEV-5")
	want := []string{worldMapProvisionedFlagPrefix + "DEV-5", worldMapProvisionedFlagPrefix + "192.0.2.5"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("flags = %v, want %v", got, want)
	}
	if got := worldMapProvisionedFlags("192.0.2.5", ""); len(got) != 1 || got[0] != worldMapProvisionedFlagPrefix+"192.0.2.5" {
		t.Errorf("host-only flags = %v", got)
	}
	if got := worldMapProvisionedFlags("", ""); len(got) != 0 {
		t.Errorf("no identity should yield no flags, got %v", got)
	}
}
