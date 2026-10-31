package webui

// Did the owner just set this speaker up through Bose's own setup page?
//
// Field case (#1573, Series-I ST20, BCO chassis, 2026-10-28): the owner put
// the speaker into setup and gave it the Wi-Fi password through the setup page
// the speaker serves on its own access point. STR's wlan-creds still held the
// password STR had been given earlier, which the owner says was wrong. Nothing
// told STR that its copy was now stale, and the offline rescue in run.sh pushes
// wlan-creds through the chip's goform handler whenever the speaker has no
// lease and is not in setup. That handler is the call that PROGRAMS the chip,
// so one boot with a slow router would have replaced the owner's working
// password with STR's stale one.
//
// The supersede check (wlansupersede.go) only catches a NEW network; the same
// network with a new password looks identical to it, because it compares names.
//
// The evidence used here is the speaker's own Wi-Fi store. A setup through the
// Bose page ends with the firmware persisting the profile it was given
// (AirplayConfiguration.xml on a BCO chassis, NetworkProfiles.xml on sm2). So
// when a setup episode starts, the agent records what the store holds (as
// salted per-profile tags, never names or secrets), and when a profile shows
// up that was not there before, with STR not having written credentials of its
// own in between, the owner provisioned the speaker. STR then retires its
// records the same way the supersede check does.
//
// Deliberately NOT a "the owner pressed a key combination" mark. A combination
// that was started and then cancelled, or timed out, changes nothing in the
// store, and must not retire credentials that still work. And deliberately
// keyed on a profile being ADDED or CHANGED: a profile STR's own Wi-Fi forget
// removed, or a priority STR rewrote, is not the owner speaking.
//
// One NAND write per setup episode (the snapshot), so a setup that ends in a
// reboot is still compared on the next boot.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	// wlanSetupSnapshotPath holds the store tags taken when a setup episode
	// began. A var so the tests can point it at a temp dir.
	wlanSetupSnapshotPath = "/mnt/nv/streborn/wlan-setup-snapshot.json"
	// boseWifiStoreGlobs are the speaker's own persisted Wi-Fi stores, with the
	// element that carries one profile. wpa_supplicant.conf is left out on
	// purpose: STR itself rewrites it, and on sm2 the firmware regenerates it.
	boseWifiStoreGlobs = []struct{ glob, element string }{
		{networkProfilesGlob, "profile"},
		{airplayConfigGlob, "PersistentWifiProfile"},
	}
)

// wlanSetupSnapshotMaxAge bounds how long a snapshot stays meaningful. A setup
// that ends in a reboot is compared on the next boot; one whose snapshot is a
// day old is a leftover from an agent that never got to compare it.
const wlanSetupSnapshotMaxAge = 24 * time.Hour

// wlanSetupSnapshotSettle is how long a snapshot is kept once the speaker is
// out of setup again. Not dropped on the first pass out of setup: a speaker
// that flips between SETUP and another source for a quarter of an hour (#873)
// would otherwise cost a NAND write per flip. An hour covers a setup that ends
// in one or two reboots.
const wlanSetupSnapshotSettle = time.Hour

// wlanSetupSnapshot is the on-NAND record.
type wlanSetupSnapshot struct {
	At   int64    `json:"at"`
	Tags []string `json:"tags"`
}

// boseStoreProfileAttrs are the attributes that make a profile what it is.
// priority, lastConnected, dhcpStatus, ipAddress and the like change without
// the owner doing anything (and STR writes priority itself), so they are not
// part of the tag.
var boseStoreProfileAttrs = map[string]bool{
	"ssid": true, "passphrase": true, "security": true, "wepkey": true, "encrypted": true,
}

// boseStoreProfileTags is the pure part: one salted tag per stored profile in
// the given documents (element name -> documents). A tag changes when the
// profile's network, password or security changes, and only then.
func boseStoreProfileTags(docs map[string][]string) []string {
	var out []string
	seen := map[string]bool{}
	for element, list := range docs {
		for _, doc := range list {
			for _, tag := range scanElementTags(doc, element) {
				h := sha256.New()
				h.Write([]byte("str-wlan-store\x00" + strings.ToLower(element)))
				var ssid string
				// Attribute order inside the tag is the firmware's and stable;
				// the name filter is what keeps volatile fields out.
				for _, a := range tag.attrs {
					name := strings.ToLower(a.name)
					if !boseStoreProfileAttrs[name] {
						continue
					}
					v := doc[a.valStart:a.valEnd]
					if name == "ssid" {
						ssid = strings.TrimSpace(xmlAttrUnescape(v))
					}
					h.Write([]byte("\x00" + name + "=" + v))
				}
				if ssid == "" {
					continue // an empty slot is not a profile
				}
				t := hex.EncodeToString(h.Sum(nil)[:12])
				if !seen[t] {
					seen[t] = true
					out = append(out, t)
				}
			}
		}
	}
	return out
}

// ownerSetupAddedProfile is the decision, pure: did the store gain a profile
// (new, or the same network with a new password) since the snapshot, with STR
// not having written credentials of its own after the snapshot was taken?
//
// strWroteAt is the newest modification time of STR's own Wi-Fi records
// (wlan-creds, wlan-target); a write after the snapshot means STR provisioned
// the speaker during the episode, and the change in the store is STR's.
func ownerSetupAddedProfile(snap wlanSetupSnapshot, now []string, strWroteAt time.Time) bool {
	if !strWroteAt.IsZero() && strWroteAt.Unix() >= snap.At {
		return false
	}
	before := make(map[string]bool, len(snap.Tags))
	for _, t := range snap.Tags {
		before[t] = true
	}
	for _, t := range now {
		if !before[t] {
			return true
		}
	}
	return false
}

func readBoseStoreDocs() map[string][]string {
	docs := map[string][]string{}
	for _, spec := range boseWifiStoreGlobs {
		files, _ := filepath.Glob(spec.glob)
		for _, f := range files {
			if s := readFileString(f); s != "" {
				docs[spec.element] = append(docs[spec.element], s)
			}
		}
	}
	return docs
}

func readWlanSetupSnapshotAt(path string) (wlanSetupSnapshot, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return wlanSetupSnapshot{}, false
	}
	var s wlanSetupSnapshot
	if json.Unmarshal(b, &s) != nil || s.At == 0 {
		return wlanSetupSnapshot{}, false
	}
	return s, true
}

func writeWlanSetupSnapshotAt(path string, s wlanSetupSnapshot) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func newestModTime(paths ...string) time.Time {
	var t time.Time
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && fi.ModTime().After(t) {
			t = fi.ModTime()
		}
	}
	return t
}

// ownerSetupPassAt is one watcher pass against explicit paths. inSetup is
// whether the speaker's source is SETUP right now. It returns true when STR's
// records were retired on this pass.
//
//   - In setup with no (fresh) snapshot: take one.
//   - In setup with a snapshot: compare, retire on an owner's profile.
//   - Out of setup with a snapshot: compare, and drop it once it has settled.
func ownerSetupPassAt(p wlanIntentPaths, snapPath string, docs map[string][]string, inSetup bool, now time.Time, logger *slog.Logger) bool {
	snap, have := readWlanSetupSnapshotAt(snapPath)
	if have && now.Sub(time.Unix(snap.At, 0)) > wlanSetupSnapshotMaxAge {
		_ = os.Remove(snapPath)
		have = false
	}
	if !have {
		if inSetup {
			if err := writeWlanSetupSnapshotAt(snapPath, wlanSetupSnapshot{At: now.Unix(), Tags: boseStoreProfileTags(docs)}); err != nil && logger != nil {
				logger.Warn("wlan owner setup: could not record the speaker's Wi-Fi store at the start of its setup", "err", err)
			}
		}
		return false
	}
	retired := false
	if ownerSetupAddedProfile(snap, boseStoreProfileTags(docs), newestModTime(p.creds, p.target)) {
		credsSSID := readWlanCredsSSIDAt(p.creds)
		strSSID := credsSSID
		if t, ok := readWlanTargetAt(p.target); ok && t.SSID != "" {
			strSSID = t.SSID
		}
		if strSSID != "" {
			retireWlanIntentAt(p, credsSSID != "", logger)
			retired = true
			if logger != nil {
				logger.Warn("wlan owner setup: the speaker was given Wi-Fi through its own setup page; STR stops replaying or re-pushing the Wi-Fi it had stored",
					"strTag", ssidTag(strSSID), "inSetup", inSetup)
			}
		}
		// Either way this episode has spoken: drop the snapshot so the same
		// change is not acted on twice.
		_ = os.Remove(snapPath)
		return retired
	}
	if !inSetup && now.Sub(time.Unix(snap.At, 0)) > wlanSetupSnapshotSettle {
		_ = os.Remove(snapPath)
	}
	return false
}

// NoteSetupWifiPass is called by the leave-setup watcher on every pass that
// read the speaker's source. Cheap when there is nothing to do: one stat of
// the snapshot outside setup.
//
// TryLock, as in reconcileWlanIntentLive: a Wi-Fi switch holding wlanMu is STR
// provisioning, and it rewrites the very files this compares. The snapshot is
// left in place and the next pass decides.
func (s *Server) NoteSetupWifiPass(inSetup bool) {
	if !inSetup {
		if _, err := os.Stat(wlanSetupSnapshotPath); err != nil {
			return
		}
	}
	if !s.wlanMu.TryLock() {
		return
	}
	defer s.wlanMu.Unlock()
	ownerSetupPassAt(defaultWlanIntentPaths(), wlanSetupSnapshotPath, readBoseStoreDocs(), inSetup, time.Now(), s.logger)
}
