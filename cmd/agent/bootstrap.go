// Boot-time self-heal: stick and NAND bootstrap sync, reboot policy,
// version stamping, region and box-name plumbing, and sshd.

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	usbstick "github.com/JRpersonal/streborn/usb-stick"
)

// loadRegion reads the country code from region.txt on the stick. Empty
// if the file does not exist or is empty; in that case the app later falls
// back to the browser/user default.
func loadRegion(path string, logger *slog.Logger) string {
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		logger.Debug("region file not readable", "path", path, "err", err)
		return ""
	}
	cc := ""
	for _, r := range string(b) {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			cc += string(r)
		}
	}
	if len(cc) < 2 {
		return ""
	}
	cc = cc[:2]
	// Uppercase
	out := ""
	for _, r := range cc {
		if r >= 'a' && r <= 'z' {
			r = r - 32
		}
		out += string(r)
	}
	logger.Info("region loaded", "country", out)
	return out
}

// syncRunOverrideFromStick keeps the NAND run-override.sh in sync with
// the run.sh on the stick. Important: rc.local prioritises NAND over the
// stick, so a stale NAND script would ignore the new setup wizard features
// (name.conf, region.conf, etc.).
//
// If the files are identical: no-op (no flash writes).
func syncRunOverrideFromStick(logger *slog.Logger) {
	const stickPath = "/media/sda1/run.sh"
	const nandPath = "/mnt/nv/streborn/run-override.sh"

	time.Sleep(5 * time.Second) // give the stick time to mount

	stickData, err := os.ReadFile(stickPath)
	if err != nil {
		logger.Debug("run.sh on stick not readable, skipping sync", "err", err)
		return
	}
	nandData, _ := os.ReadFile(nandPath)
	if len(nandData) > 0 && bytesEqual(stickData, nandData) {
		return // already identical
	}
	tmp := nandPath + ".new"
	if err := os.WriteFile(tmp, stickData, 0o755); err != nil {
		logger.Warn("run-override.sh sync write failed", "err", err)
		return
	}
	if err := os.Rename(tmp, nandPath); err != nil {
		logger.Warn("run-override.sh sync rename failed", "err", err)
		os.Remove(tmp)
		return
	}
	logger.Info("run-override.sh updated on NAND from stick", "bytes", len(stickData))
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// applyPendingBoxName applies a box name left by the setup wizard once to the
// Bose box, verbatim. The name is one the user deliberately typed for this
// speaker during setup, so it is used exactly as chosen: appending the DeviceID
// as a UID suffix here made the user's own name look untidy on every install and
// update (#133, #292). Duplicate-name disambiguation is the caller's concern (the
// user simply gives two speakers two different names); STR does not second-guess
// a name the user picked. On success the file is deleted.
func applyPendingBoxName(ctx context.Context, boxHost, path string, logger *slog.Logger) {
	if boxHost == "" || path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		// no file, nothing to apply
		return
	}
	raw := strings.TrimSpace(string(b))
	if raw == "" {
		_ = os.Remove(path)
		return
	}
	wanted := raw
	// The box must be reachable. Wait until the BoseApp web server is up.
	time.Sleep(10 * time.Second)
	c := boxapi.New(boxHost)
	for attempt := 0; attempt < 12; attempt++ {
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := c.SetName(callCtx, wanted)
		cancel()
		if err == nil {
			logger.Info("setup wizard box name applied", "name", wanted)
			_ = os.Remove(path)
			return
		}
		logger.Debug("box name set failed, will retry", "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
	logger.Warn("could not set box name from setup, giving up", "path", path)
}

// ensureSshdRunning keeps the box reachable via SSH whether the agent
// boot came in via a fresh stick run.sh (which has its own
// ensure_sshd_running shell function) or via OTA-only update (which
// replaces only the binary and leaves the on-NAND run-override.sh
// untouched). Without this the OTA path loses the diagnostic channel
// the first time the agent crashes, and `SaveDiagnosticBundle`'s
// SSH-fallback layer comes back empty.
//
// Pre-1.0 we explicitly prefer diagnostic access over the residual
// risk of a known-default Bose root password; tracked under the
// existing box-security-hardening roadmap.
//
// bootstrapTargets lists the on-NAND files the agent will replace
// when their disk content differs from what is embedded in the
// agent binary. /mnt/nv/rc.local is read once by /etc/init.d/
// shelby_local at boot; /mnt/nv/streborn/run-override.sh is what
// that rc.local exec's. Both must stay in sync with the agent
// version so the boot path uses the same shim / WLAN / gate logic
// the running agent expects.
var bootstrapTargets = []struct {
	embedded string
	target   string
	desc     string
}{
	{"run.sh", "/mnt/nv/streborn/run-override.sh", "boot bootstrap script"},
	{"rc.local", "/mnt/nv/rc.local", "shelby_local entry point"},
}

// syncBootstrapFromEmbedded compares the bootstrap files embedded
// in this agent binary against the on-NAND copies and replaces any
// that differ. Runs once on agent startup. Atomic via tmp-file +
// rename; on any failure we leave the existing file in place and
// log so the next diagnostic bundle captures the reason. Skipped
// silently in dev environments where /mnt/nv does not exist.
func syncBootstrapFromEmbedded(logger *slog.Logger) (changed bool) {
	if _, err := os.Stat("/mnt/nv"); err != nil {
		// Not on the box (developer machine, CI). No-op.
		return false
	}
	stickFS := usbstick.Files()
	for _, t := range bootstrapTargets {
		embedded, err := fs.ReadFile(stickFS, t.embedded)
		if err != nil {
			logger.Warn("bootstrap sync: embedded file not readable",
				"name", t.embedded, "err", err)
			continue
		}
		current, _ := os.ReadFile(t.target)
		if bytes.Equal(embedded, current) {
			// Already current. Quiet path.
			continue
		}
		// Ensure parent directory exists. /mnt/nv/streborn may be
		// missing on a freshly-flashed-and-reset box that still has
		// the old rc.local but no streborn dir tree yet.
		if i := strings.LastIndex(t.target, "/"); i > 0 {
			_ = os.MkdirAll(t.target[:i], 0o755)
		}
		tmp := t.target + ".str-bootstrap-sync"
		_ = os.Remove(tmp) // tolerate stale tmp from a previous crashed run
		if err := os.WriteFile(tmp, embedded, 0o755); err != nil {
			logger.Warn("bootstrap sync: write failed",
				"tmp", tmp, "err", err)
			continue
		}
		if err := os.Rename(tmp, t.target); err != nil {
			logger.Warn("bootstrap sync: atomic rename failed, leaving old in place",
				"tmp", tmp, "target", t.target, "err", err)
			_ = os.Remove(tmp)
			continue
		}
		// WARN so a diagnostic bundle pinpoints the boot where the
		// bootstrap layer caught up. The replacement only takes effect
		// on the NEXT boot: this boot's already-running shelby_local
		// and run-override.sh are whatever they were before the sync.
		logger.Warn("bootstrap sync: replaced on-NAND file with embedded copy",
			"target", t.target,
			"desc", t.desc,
			"oldBytes", len(current),
			"newBytes", len(embedded),
			"effective", "next boot")
		changed = true
	}
	return changed
}

// bootstrapRebootStampPath records the fingerprint of the embedded
// bootstrap set we last rebooted for. It is the loop breaker: if a
// NAND write silently fails to persist, syncBootstrapFromEmbedded
// would report "changed" on every boot, and an unconditional
// post-sync reboot would turn that into a boot loop that bricks the
// box. We reboot at most once per embedded fingerprint.
const bootstrapRebootStampPath = "/mnt/nv/streborn/.str-bootstrap-reboot-stamp"

// embeddedBootstrapStamp is a stable fingerprint of the bootstrap
// files embedded in THIS binary. It changes only when the embedded
// run.sh / rc.local change, i.e. across agent releases that touch the
// boot path. Returns "" if the embedded files cannot be read, in
// which case the caller must not reboot (it cannot guard the loop).
func embeddedBootstrapStamp() string {
	h := sha256.New()
	stickFS := usbstick.Files()
	for _, t := range bootstrapTargets {
		b, err := fs.ReadFile(stickFS, t.embedded)
		if err != nil {
			return ""
		}
		_, _ = h.Write([]byte(t.embedded))
		_, _ = h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// maybeRebootAfterBootstrapSync reboots the box once so a freshly
// written run-override.sh / rc.local take effect immediately instead
// of on the user's next manual power-cycle. This is the "STR reboots
// itself into a clean state" path: after a stick install or agent OTA
// the running boot used the OLD scripts; one clean reboot lands the
// box on the boot path that matches the running binary.
//
// Guarded so it can never loop:
//   - If the embedded fingerprint cannot be computed, do not reboot.
//   - If we already rebooted for exactly this fingerprint (marker
//     matches) yet the sync still reported a change, the NAND write is
//     not persisting; rebooting again would loop, so we stay up in a
//     degraded state and log loudly instead.
func maybeRebootAfterBootstrapSync(logger *slog.Logger) {
	stamp := embeddedBootstrapStamp()
	if stamp == "" {
		logger.Warn("bootstrap reboot: skipped, cannot fingerprint embedded boot files (no loop guard possible)")
		return
	}
	if prev, err := os.ReadFile(bootstrapRebootStampPath); err == nil &&
		strings.TrimSpace(string(prev)) == stamp {
		logger.Error("bootstrap reboot: on-NAND boot files STILL differ after a prior reboot for this exact version - the NAND write is not persisting; refusing to reboot again to avoid a boot loop, continuing on the stale boot path",
			"stamp", stamp)
		return
	}
	if err := os.WriteFile(bootstrapRebootStampPath, []byte(stamp+"\n"), 0o644); err != nil {
		logger.Warn("bootstrap reboot: could not persist loop-guard stamp, refusing to reboot",
			"path", bootstrapRebootStampPath, "err", err)
		return
	}
	logger.Warn("bootstrap reboot: boot path was refreshed, rebooting once so the new run-override.sh/rc.local run this cycle instead of waiting for a manual power-cycle",
		"stamp", stamp)
	// A reboot fired seconds after boot — stacked on the install/bootstrap/OTA
	// reboot — trips Bose's shepherdd watchdog into --recovery mode, where the
	// Bose services never start and radio cannot play until a manual power-cycle
	// (the box shows the alternating amber LED pattern). First seen on the lisa
	// chassis (Wave, SA-4, #372) and now confirmed on ginger (SoundTouch 300,
	// reported 2026-07-11 after a v0.9.4 OTA). Wait for the Bose stack to come up
	// first so shepherd marks this boot successful and resets its crash-loop
	// counter; the reboot that follows is then a clean single reboot.
	settleBeforeFragileReboot(logger)
	// Flush pending writes (the stick log, the bootstrap files and the
	// guard stamp on NAND) before we pull the rug out. busybox `sync`
	// keeps this portable at compile time, matching the reboot exec.
	_ = exec.Command("sync").Run()
	time.Sleep(2 * time.Second)
	if err := exec.Command("reboot").Run(); err != nil {
		logger.Error("bootstrap reboot: reboot command failed, continuing on stale boot path", "err", err)
	}
}

// boxVariant returns the Bose chassis codename from /proc/variant (e.g. "lisa",
// "taigan", "rhino", "mojo"), lowercased, or "" when it cannot be read.
func boxVariant() string {
	b, err := os.ReadFile("/proc/variant")
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(string(b)))
}

// verifiedFastRebootChassis are the Bose chassis codenames Jens has
// hardware-verified to survive a stacked early-boot reboot without shepherdd
// entering --recovery, so they skip the settle wait and reboot immediately.
// Every OTHER chassis (lisa/Wave/SA-4 #372, ginger/ST300, and any unknown or
// unreadable variant) waits for the Bose stack first: the amber-recovery trip
// turned out NOT to be lisa-only, so the safe default is to settle unless a
// chassis is on this proven-fast allowlist.
var verifiedFastRebootChassis = map[string]bool{
	// rhino (ST10) was removed 2026-07-14: two field ST10s went network-unstable
	// on an OTA reboot until a manual power-cycle (#403), the same shepherdd
	// --recovery trip as lisa (#372) and ginger/ST300 (a56b0ae). Three of this
	// allowlist's assumptions have now been disproven, so only chassis with
	// repeated first-hand confirmation stay: mojo and taigan have each survived
	// many stacked reboots on Jens' own boxes. The settle wait is a cheap
	// best-effort with no downside on a genuinely fast box.
	"mojo":   true, // SoundTouch 30 (scm/sm2)
	"taigan": true, // SoundTouch Portable
}

// settleBeforeFragileReboot delays a STR-initiated early-boot reboot until the
// Bose service stack has come up on this boot, so a reboot stacked on the
// install/bootstrap/OTA reboot does not trip shepherdd into --recovery mode
// (the alternating amber LED lockup that needs a manual power-cycle, #372 lisa +
// ginger/ST300). Waiting for :8090 to answer means shepherd has marked this boot
// successful and reset its crash-loop counter. Skipped on the hardware-verified
// fast chassis; best-effort everywhere else — after the settle window it reboots
// anyway.
func settleBeforeFragileReboot(logger *slog.Logger) {
	variant := boxVariant()
	if verifiedFastRebootChassis[variant] {
		return
	}
	const (
		maxWait = 100 * time.Second
		grace   = 5 * time.Second
	)
	logger.Info("bootstrap reboot: waiting for the Bose stack (:8090) before rebooting so shepherdd does not enter recovery (#372)", "variant", variant)
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		if dialable("127.0.0.1", 8090) {
			logger.Info("bootstrap reboot: Bose stack is up (:8090); shepherd boot marked healthy, proceeding with a single clean reboot")
			time.Sleep(grace) // let shepherd finish marking the boot successful
			return
		}
		time.Sleep(2 * time.Second)
	}
	logger.Warn("bootstrap reboot: Bose stack did not come up within the settle window; rebooting anyway (best-effort)", "variant", variant)
}

// stampVersionFiles writes this binary's version (semver + build stamp)
// to the on-box version.txt files so the desktop always sees the
// version that is actually running, not whatever the last stick-prep
// wrote. Without it, an OTA (which replaces only the binary) left
// version.txt at the old build and the box kept reporting the
// pre-update version (#94). NAND is the reliable target; the FAT32
// stick copy is best-effort (one small write, not in the boot-critical
// path). Atomic via tmp + rename; skipped where the parent dir is
// absent (dev host, no stick).
func stampVersionFiles(logger *slog.Logger) {
	stamp := version
	if buildStamp != "" && buildStamp != "dev" {
		stamp = version + "+" + buildStamp
	}
	for _, p := range []string{"/mnt/nv/streborn/version.txt", "/media/sda1/version.txt"} {
		dir := p[:strings.LastIndex(p, "/")]
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		tmp := p + ".str-new"
		if err := os.WriteFile(tmp, []byte(stamp+"\n"), 0o644); err != nil {
			logger.Debug("version stamp: write failed", "path", p, "err", err)
			continue
		}
		if err := os.Rename(tmp, p); err != nil {
			logger.Debug("version stamp: rename failed", "path", p, "err", err)
			_ = os.Remove(tmp)
		}
	}
}

// Best-effort: if sshd is already running, the init script
// no-ops; if no sshd init script exists (unexpected on Bose
// firmware), we just log and continue.
func ensureSshdRunning(logger *slog.Logger) {
	// Cheap pre-check: avoid spawning the init script if sshd is
	// already up — saves a fork on every agent restart.
	if out, err := exec.Command("pidof", "sshd").Output(); err == nil && len(strings.TrimSpace(string(out))) > 0 {
		return
	}
	for _, attempt := range [][]string{
		{"/etc/init.d/sshd", "start"},
		{"/usr/sbin/sshd"},
	} {
		cmd := exec.Command(attempt[0], attempt[1:]...)
		if err := cmd.Run(); err == nil {
			logger.Info("sshd started", "via", attempt[0])
			return
		}
	}
	logger.Warn("sshd start: no usable init script found, SSH will not come up from agent")
}
