// The standing record of the Wi-Fi network the user chose for this speaker.

package webui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"time"
)

// wlanTargetPath holds the STANDING intent: the network the user last moved
// this speaker to. It survives every boot and is never consumed on read.
//
// Deliberately NOT the one-shot .wlan-apply-pending marker (see
// touchWLANApplyMarker): run.sh deletes that marker as it reads it, so it only
// ever protected the FIRST boot after a move. That is exactly why a reporter's
// speakers came back on the old network again on the second power cycle
// (#479, five diagnostic bundles, 2026-08-21). A standing record costs one
// wpa_cli status read on a healthy boot and is the only thing that can notice
// a speaker drifting back weeks later.
//
// The file carries the passphrase in clear, like the wlan-creds file next to
// it that the boot path already replays; both are 0600 on NAND that only root
// can reach. It is a var only so the tests can point it at a temp dir, never
// reassigned in production.
var wlanTargetPath = "/mnt/nv/streborn/wlan-target"

// maxFailedBoots is the failure budget. After this many boots where a
// correction was attempted and did not take, the guard stands down for good
// and says so once, loudly. Without a budget an ambiguous SSID (a repeater, a
// neighbour with the same name, band steering) would make the speaker chase a
// network it can never win, forever.
const maxFailedBoots = 5

// wlanTarget is the intent record itself.
type wlanTarget struct {
	SSID   string `json:"ssid"`
	PSK    string `json:"psk"`
	Hidden bool   `json:"hidden"`
	// Verify records how strong the evidence behind this record is:
	//
	//	"live" the box associated to this SSID and wpa_supplicant confirmed it
	//	"weak" no runtime channel existed (BCO/scm chassis, or a conf that could
	//	       only be applied by rebooting), so the move was requested but never
	//	       observed. A "weak" record must never be reported as a green tick.
	Verify string `json:"verify,omitempty"`
	// Gen is bumped by every user-initiated move. A correction that finds a
	// different Gen than it started with is chasing a network the user has
	// since replaced and stands down.
	Gen   int   `json:"gen"`
	SetAt int64 `json:"setAt"`
	// BootsFailed counts boots on which a correction was attempted and failed.
	// Reset by any boot that ends on the target; NOT incremented by a boot
	// where the guard stood down without touching anything.
	BootsFailed int    `json:"bootsFailed"`
	LastVerdict string `json:"lastVerdict,omitempty"`
}

// wlanBudget says what a verdict does to the failure budget.
type wlanBudget int

const (
	// budgetReset: the boot ended on the target, so the budget is spent-free
	// again.
	budgetReset wlanBudget = iota
	// budgetHold: nothing was attempted (stand-down, or a chassis with no
	// runtime channel), so no attempt is burned. This is what keeps "the old
	// network is switched off" from eating the budget of a speaker that is
	// simply out of range of the network it should be on.
	budgetHold
	// budgetBurn: a correction ran and did not take.
	budgetBurn
)

// writeWlanTarget commits the intent record: temp file, fsync, rename, so a
// power loss mid-write can never leave a half-parsed record behind (the box is
// power-cycled by the wall socket in exactly the test this feature exists for).
func writeWlanTarget(t wlanTarget) error {
	return writeWlanTargetAt(wlanTargetPath, t)
}

// writeWlanTargetAt is the path-injectable core, kept separate so the record
// format stays unit-testable off-box.
func writeWlanTargetAt(path string, t wlanTarget) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := path + ".new"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, werr := f.Write(b); werr != nil {
		f.Close()
		_ = os.Remove(tmp)
		return werr
	}
	// Best-effort flush to the NAND before the rename: a rename of unflushed
	// data is atomic in the directory but not in the file's contents.
	_ = f.Sync()
	if cerr := f.Close(); cerr != nil {
		_ = os.Remove(tmp)
		return cerr
	}
	return os.Rename(tmp, path)
}

// readWlanTarget returns the intent record. A missing OR unparsable file is
// "no intent", never an error: the guard must stay silent rather than act on
// a record it does not understand.
func readWlanTarget() (wlanTarget, bool) {
	return readWlanTargetAt(wlanTargetPath)
}

func readWlanTargetAt(path string) (wlanTarget, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return wlanTarget{}, false
	}
	var t wlanTarget
	if uerr := json.Unmarshal(b, &t); uerr != nil {
		return wlanTarget{}, false
	}
	// A record with no SSID cannot be compared against anything, so it is the
	// same as having none.
	if t.SSID == "" {
		return wlanTarget{}, false
	}
	return t, true
}

// noteWlanTargetVerdict records what this boot concluded and moves the failure
// budget accordingly. Silent when there is no record to update.
func noteWlanTargetVerdict(verdict string, b wlanBudget) {
	t, ok := readWlanTarget()
	if !ok {
		return
	}
	before := t
	t.LastVerdict = verdict
	switch b {
	case budgetReset:
		t.BootsFailed = 0
	case budgetBurn:
		t.BootsFailed++
	case budgetHold:
		// unchanged on purpose: nothing was attempted
	}
	// A healthy boot reaches the same verdict as the last one, so writing it
	// again would put one pointless NAND write into every single power cycle.
	if t == before {
		return
	}
	_ = writeWlanTarget(t)
}

// clearWlanTarget forgets the intent entirely, so the guard stops caring about
// this speaker. Used by the rollback path (a switch that never associated must
// never arm a correction loop) and by the user saying "this speaker is where I
// want it".
func clearWlanTarget() error {
	err := os.Remove(wlanTargetPath)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// armWlanTarget records the network this speaker should be on from now on,
// bumping the generation so a correction already in flight for the previous
// network stands down instead of fighting the user.
func (s *Server) armWlanTarget(ssid, psk string, hidden bool, verify string) {
	prev, _ := readWlanTarget()
	t := wlanTarget{
		SSID:   ssid,
		PSK:    psk,
		Hidden: hidden,
		Verify: verify,
		Gen:    prev.Gen + 1,
		SetAt:  time.Now().Unix(),
	}
	if err := writeWlanTarget(t); err != nil {
		// Non-fatal: without the record the speaker behaves exactly as it did
		// before this feature existed, which is the old bug, not a new one.
		s.logger.Warn("WLAN: could not record which network this speaker should be on; a cold boot will not be corrected",
			"err", err, "wantTag", ssidTag(ssid), "verify", verify)
		return
	}
	s.logger.Info("WLAN: recorded the intended network for this speaker",
		"wantTag", ssidTag(ssid), "verify", verify, "gen", t.Gen)
}

// wlanTargetDebug is the bundle view of the intent record: everything that
// decides a support question, and never the credentials. The passphrase and
// the network name are left out at the source rather than trusted to a
// downstream scrubber, because this section is served to an unauthenticated
// GET on the LAN as well as into a bundle.
func wlanTargetDebug() map[string]any {
	t, ok := readWlanTarget()
	if !ok {
		return map[string]any{"set": false}
	}
	return map[string]any{
		"set":         true,
		"ssidTag":     ssidTag(t.SSID),
		"hidden":      t.Hidden,
		"verify":      t.Verify,
		"gen":         t.Gen,
		"setAt":       t.SetAt,
		"bootsFailed": t.BootsFailed,
		"lastVerdict": t.LastVerdict,
	}
}

// ssidTag turns an SSID into a short, stable, non-reversible identity so two
// networks can be told apart in a diagnostic bundle without the bundle
// carrying anyone's network name.
//
// This is the piece that makes the next bundle decidable. The desktop app
// replaces the value of any field named exactly "ssid"/"psk"/"password" with
// <REDACTED> (desktop-app/logexport.go), which is correct and must stay — but
// it is also why every bundle so far showed four indistinguishable networks
// and nobody could say whether the speaker had come up on the right one. A tag
// under a differently named field survives that scrub, is stable across
// bundles and across speakers, and is not a network name.
//
// The length is the SSID's byte length (802.11 SSIDs are byte strings, up to
// 32 bytes) and is there to separate two SSIDs whose hashes happen to share a
// 24-bit prefix.
//
// Log field names must NOT start with "ssid": the bundle's text scrubber
// rewrites anything matching ssid<non-space>* and would eat the tag with it.
// Hence wantTag / gotTag / storeTags in the log lines.
func ssidTag(s string) string {
	if s == "" {
		return "none"
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:6] + ":" + strconv.Itoa(len(s))
}
