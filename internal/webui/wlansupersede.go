package webui

// Has the owner moved this speaker to another network without STR?
//
// STR keeps two records of the Wi-Fi it gave a speaker: wlan-creds (what the
// boot path replays and what the offline rescue re-pushes at the Wi-Fi chip)
// and wlan-target (what the boot guard steers back to). Both are only ever
// replaced by STR itself. An owner who later moves the speaker through Bose's
// own route (the setup access point after a Wi-Fi reset, the Bose app) leaves
// both pointing at the OLD network, and from then on STR is holding the wrong
// answer:
//
//   - the settings screen named the old network as the one the speaker is on
//     (support mail, scm ST20, 2026-10-21: "it claims the old network, which
//     I switched off; the new one is what works");
//   - worse, run.sh's offline rescue re-pushes wlan-creds through the chip's
//     goform handler whenever the speaker has no lease 90 s into a boot. That
//     handler is the very call that SWITCHES the chip's network, so a power cut
//     where the router boots slower than the speaker would have moved the chip
//     back to a network the owner had switched off, once a minute for twelve
//     minutes;
//   - on a wpa chassis the boot guard would treat the owner's new network as
//     drift and move the speaker back, until it had been overruled twice.
//
// The evidence that separates "the owner chose another network" from the drift
// the guard exists to fix (#461: the speaker wakes up on the network it was on
// BEFORE STR moved it) is whether the network is NEW. Drift returns to a
// network that was already in the speaker's store when STR set its record. An
// owner's own choice adds one. So when STR records credentials it also records
// which networks the speaker knew at that moment (as salted tags, never names),
// and a later network outside that set is the owner speaking. STR then retires
// its records instead of arguing: the credentials move aside to
// wlan-creds.superseded, the target is cleared, and every path that would have
// pushed or steered toward the old network has nothing to push.
//
// Never a write to the firmware's store or the Wi-Fi chip: retiring only
// removes STR's own files.

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

var (
	// wlanKnownPath is the baseline: the tag of STR's credentials and the tags
	// of every network the speaker knew when they were recorded. Vars only so
	// the tests can point them at a temp dir.
	wlanKnownPath = "/mnt/nv/streborn/wlan-known.json"
	// wlanCredsRetiredPath is where superseded credentials go. Kept rather than
	// deleted so a support case can still see that STR once set a network,
	// and so the agent can tell "retired" apart from "never had any".
	wlanCredsRetiredPath = "/mnt/nv/streborn/wlan-creds.superseded"
	// wlanCredsLivePath is wlanCredsPath as a var, for the same reason.
	wlanCredsLivePath = wlanCredsPath
)

// wlanKnown is the baseline record.
type wlanKnown struct {
	// CredsTag is ssidTag of the network in wlan-creds when the baseline was
	// taken. A different tag means STR has written new credentials since, and
	// the baseline is retaken rather than compared.
	CredsTag string   `json:"credsTag"`
	Known    []string `json:"known"`
}

// storedNet is one network in the speaker's own stores, as the supersede
// decision sees it.
type storedNet struct {
	SSID string
	Open bool
}

// isBoseSetupAPName reports the name the speaker's own setup access point
// broadcasts. The firmware files it as an open profile once its setup page
// has been used; it is the speaker's own network, never the owner's choice.
func isBoseSetupAPName(ssid string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(ssid)), "bose soundtouch")
}

// supersedingNetwork is the decision, pure. It returns the first network that
// proves the owner moved the speaker elsewhere: a network the speaker is on
// right now (live, "" when unknown) or has in its store, that is not STR's
// own, not open, not the setup access point, and was not known when STR took
// its baseline.
//
// The live association is checked first because it is the strongest evidence.
// Open networks never count: a public hotspot or the setup access point is
// not the owner picking a home network, and retiring on one would be a guess.
func supersedingNetwork(strSSID string, base wlanKnown, stored []storedNet, live string) string {
	if strSSID == "" || base.CredsTag != ssidTag(strSSID) {
		return ""
	}
	known := make(map[string]bool, len(base.Known))
	for _, k := range base.Known {
		known[k] = true
	}
	open := map[string]bool{}
	for _, n := range stored {
		if n.Open {
			open[n.SSID] = true
		}
	}
	isNew := func(ssid string) bool {
		ssid = strings.TrimSpace(ssid)
		return ssid != "" && ssid != strSSID && !open[ssid] && !isBoseSetupAPName(ssid) &&
			!isPublicHotspot(ssid) && !known[ssidTag(ssid)]
	}
	if isNew(live) {
		return live
	}
	for _, n := range stored {
		if !n.Open && isNew(n.SSID) {
			return n.SSID
		}
	}
	return ""
}

// baselineFor builds the record for the current credentials and store.
func baselineFor(strSSID string, stored []storedNet, live string) wlanKnown {
	b := wlanKnown{CredsTag: ssidTag(strSSID)}
	seen := map[string]bool{}
	add := func(ssid string) {
		ssid = strings.TrimSpace(ssid)
		if ssid == "" {
			return
		}
		t := ssidTag(ssid)
		if seen[t] {
			return
		}
		seen[t] = true
		b.Known = append(b.Known, t)
	}
	add(strSSID)
	add(live)
	for _, n := range stored {
		add(n.SSID)
	}
	return b
}

func readWlanKnownAt(path string) (wlanKnown, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return wlanKnown{}, false
	}
	var k wlanKnown
	if json.Unmarshal(b, &k) != nil || k.CredsTag == "" {
		return wlanKnown{}, false
	}
	return k, true
}

func writeWlanKnownAt(path string, k wlanKnown) error {
	b, err := json.Marshal(k)
	if err != nil {
		return err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// speakerStoredNets reads every network the speaker's own stores hold: the
// firmware's NetworkProfiles and the chip's AirplayConfiguration, with the
// open flag. File reads only.
func speakerStoredNets() []storedNet {
	var out []storedNet
	for _, spec := range []struct{ glob, element string }{
		{networkProfilesGlob, "profile"},
		{airplayConfigGlob, "PersistentWifiProfile"},
	} {
		files, _ := filepath.Glob(spec.glob)
		for _, f := range files {
			for _, e := range parseStoredProfiles(readFileString(f), spec.element) {
				out = append(out, storedNet(e))
			}
		}
	}
	return out
}

// liveSSIDFrom is the network the firmware itself says the speaker is
// associated to, "" when it does not name one (the scm ST20 never does).
func liveSSIDFrom(ifaces []boxapi.NetworkInterface) string {
	name, _ := liveAssociation(ifaces)
	return name
}

// wlanIntentPaths are the four files the reconcile reads and retires, path
// injectable so the whole flow runs off-box in tests.
type wlanIntentPaths struct {
	creds, retired, target, known string
}

func defaultWlanIntentPaths() wlanIntentPaths {
	return wlanIntentPaths{creds: wlanCredsLivePath, retired: wlanCredsRetiredPath, target: wlanTargetPath, known: wlanKnownPath}
}

// reconcileWlanIntentAt is the whole check against explicit paths. It returns
// the superseding network ("" when nothing changed). STR's network is the
// target record when there is one, else the credentials, the same order
// strWlanTarget uses.
func reconcileWlanIntentAt(p wlanIntentPaths, stored []storedNet, live string, logger *slog.Logger) string {
	strSSID := ""
	if t, ok := readWlanTargetAt(p.target); ok {
		strSSID = t.SSID
	}
	credsSSID := readWlanCredsSSIDAt(p.creds)
	if strSSID == "" {
		strSSID = credsSSID
	}
	if strSSID == "" {
		return ""
	}
	// The baseline is keyed on the credentials file, the thing the rescue
	// pushes. Without credentials, the target alone keys it.
	keySSID := credsSSID
	if keySSID == "" {
		keySSID = strSSID
	}
	base, ok := readWlanKnownAt(p.known)
	if !ok || base.CredsTag != ssidTag(keySSID) {
		// First look at these credentials: take the baseline. Everything the
		// speaker knows right now counts as known, including networks the owner
		// may already have added; only a network that appears LATER is evidence.
		if err := writeWlanKnownAt(p.known, baselineFor(keySSID, stored, live)); err != nil && logger != nil {
			logger.Warn("wlan intent: could not record which networks this speaker knows", "err", err)
		}
		return ""
	}
	newNet := supersedingNetwork(keySSID, base, stored, live)
	if newNet == "" && keySSID != strSSID {
		newNet = supersedingNetwork(strSSID, wlanKnown{CredsTag: ssidTag(strSSID), Known: base.Known}, stored, live)
	}
	if newNet == "" {
		return ""
	}
	retireWlanIntentAt(p, credsSSID != "", logger)
	if logger != nil {
		logger.Warn("wlan intent: the speaker was moved to another network outside STR; STR stops replaying, re-pushing or steering back to the network it set",
			"strTag", ssidTag(strSSID), "newTag", ssidTag(newNet), "liveKnown", live != "")
	}
	return newNet
}

// retireWlanIntentAt sets STR's Wi-Fi records aside: the credentials move to
// the retired path (kept, never deleted), the target and the baseline are
// removed. Shared by the supersede check and the owner-setup check
// (wlanownersetup.go).
func retireWlanIntentAt(p wlanIntentPaths, haveCreds bool, logger *slog.Logger) {
	if haveCreds {
		if err := os.Rename(p.creds, p.retired); err != nil && logger != nil {
			logger.Warn("wlan intent: could not set the old credentials aside", "err", err)
		}
	}
	if err := os.Remove(p.target); err != nil && !os.IsNotExist(err) && logger != nil {
		logger.Warn("wlan intent: could not clear the old target", "err", err)
	}
	_ = os.Remove(p.known)
}

// reconcileWlanIntent runs the check on the speaker. ifaces may be nil (the
// box's /networkInfo was not read); the stores alone still decide.
//
// TryLock, not Lock: a Wi-Fi switch holds wlanMu for minutes and rewrites the
// very files this reads, so a check that finds it busy has nothing to say and
// must not hold up the settings screen waiting for it.
func (s *Server) reconcileWlanIntent(ifaces []boxapi.NetworkInterface) {
	s.reconcileWlanIntentLive(liveSSIDFrom(ifaces))
}

// reconcileWlanIntentLive is the same with a live SSID already in hand (the
// wpa boot guard reads it from wpa_supplicant).
func (s *Server) reconcileWlanIntentLive(live string) {
	if !s.wlanMu.TryLock() {
		return
	}
	defer s.wlanMu.Unlock()
	reconcileWlanIntentAt(defaultWlanIntentPaths(), speakerStoredNets(), live, s.logger)
}

// rebaselineWlanKnown is called right after STR itself writes new
// credentials: the networks the speaker knows at that moment are, by
// definition, not the owner moving it elsewhere afterwards. It also drops a
// retired set, which the new credentials replace.
func rebaselineWlanKnown(ssid string) {
	_ = os.Remove(wlanCredsRetiredPath)
	_ = writeWlanKnownAt(wlanKnownPath, baselineFor(ssid, speakerStoredNets(), ""))
}

// wlanCredsRetired reports whether STR set its credentials aside because the
// owner moved the speaker elsewhere.
func wlanCredsRetired() bool {
	_, err := os.Stat(wlanCredsRetiredPath)
	return err == nil
}

// strConfiguredSSID is the network STR set for this speaker, for display
// only, "" when STR never set one or retired it.
func strConfiguredSSID() string {
	return strWlanTarget()
}
