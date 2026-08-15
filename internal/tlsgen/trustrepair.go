package tlsgen

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// The trust store overlay and how it breaks.
//
// The boot script copies the firmware's own CA bundle to /tmp, appends STR's
// root and bind-mounts the result over the original. When it copied while an
// overlay from an earlier run was still mounted, source and destination were
// the same inode: the copy read and wrote one file and could leave it empty.
// What then got mounted was STR's root and nothing else, and the box stopped
// trusting every public certificate. Every https station and the Spotify
// engine die seconds apart on the identical "x509: certificate signed by
// unknown authority" (ST20 field bundle, 2026-08-13).
//
// setup/run.sh and usb-stick/run.sh no longer produce that state. That fix
// alone does not reach a speaker that is already installed: an over-the-air
// update replaces the agent binary and nothing else, and the NAND copy of the
// boot script is only ever refreshed from a physical USB stick. A box that
// entered the broken state would keep re-entering it at every boot, forever,
// no matter how many updates it received.
//
// So the agent repairs it. Reading the pristine firmware bundle means taking
// the broken overlay off first, which is also the rollback: if anything below
// fails, the firmware's own bundle is what stays live. That is the safe
// direction. A box that trusts the internet but not STR still plays radio and
// Spotify, and RefreshTrustStore re-appends our root; the reverse leaves the
// speaker unable to reach anything.

// overlayDir is where the overlay files that get bind-mounted over the trust
// store live. tmpfs on the box, so this costs no NAND and starts empty after
// every boot. A variable only so the tests can put it somewhere that exists
// on a dev host.
var overlayDir = "/tmp"

// TrustRepairOutcome is the verdict for one trust store path.
type TrustRepairOutcome string

const (
	// TrustRepairHealthy means the store still carries public roots. The
	// overwhelmingly common case and the only one that touches nothing.
	TrustRepairHealthy TrustRepairOutcome = "healthy"
	// TrustRepairAbsent means the firmware has no bundle at that path.
	TrustRepairAbsent TrustRepairOutcome = "absent"
	// TrustRepairRepaired means the store had lost its public roots and now
	// carries them again.
	TrustRepairRepaired TrustRepairOutcome = "repaired"
	// TrustRepairNotOverlaid means the store is broken but nothing is
	// mounted over it, so the emptiness is the firmware's own file. Taking
	// a mount off cannot fix that and there is nothing to roll back to.
	TrustRepairNotOverlaid TrustRepairOutcome = "not-overlaid"
	// TrustRepairFirmwareEmpty means the pristine bundle underneath the
	// overlay carries no public roots either. The overlay was put back.
	TrustRepairFirmwareEmpty TrustRepairOutcome = "firmware-bundle-empty"
	// TrustRepairFailed means the repair could not be completed. Which step
	// gave up is in Err; the firmware bundle is live unless Err says the
	// unmount itself failed, in which case nothing changed at all.
	TrustRepairFailed TrustRepairOutcome = "failed"
)

// TrustRepairResult records what happened to one trust store path, so a
// diagnostic bundle answers "did the speaker notice, and did it help?"
// without anyone having to reproduce the boot.
type TrustRepairResult struct {
	Path    string             `json:"path"`
	Outcome TrustRepairOutcome `json:"outcome"`
	// RootsBefore / RootsAfter are PEM certificate counts as read THROUGH
	// any mount, i.e. what the box's TLS clients actually see. A healthy
	// box shows a three-digit count; 1 is the broken state.
	RootsBefore int `json:"roots_before"`
	RootsAfter  int `json:"roots_after"`
	// FirmwareRoots is what the pristine bundle underneath held, counted
	// while the overlay was off. Zero unless the overlay came off.
	FirmwareRoots int `json:"firmware_roots,omitempty"`
	// Overlay is the file that ended up mounted, when one did.
	Overlay string `json:"overlay,omitempty"`
	Err     string `json:"err,omitempty"`
}

// mountOps is the platform boundary. The repair logic is identical
// everywhere; only these three calls are Linux-only, and injecting them keeps
// the logic testable on a dev host that cannot mount anything.
type mountOps struct {
	isMountPoint func(path string) bool
	unmount      func(path string) error
	bindMount    func(src, dst string) error
}

var (
	lastRepairMu sync.Mutex
	lastRepair   []TrustRepairResult
)

// LastTrustRepair returns the most recent repair pass, or nil if none has run
// yet. Reported through /api/debug/state next to the trust store snapshot.
func LastTrustRepair() []TrustRepairResult {
	lastRepairMu.Lock()
	defer lastRepairMu.Unlock()
	return append([]TrustRepairResult(nil), lastRepair...)
}

// RepairTrustStore inspects the overlaid trust stores and rebuilds any that
// have lost the vendor's public roots. rootCAPEM is STR's own root, appended
// to the rebuilt store so the box keeps accepting our Bose-domain server
// cert; pass nil when it is not available yet and the rebuild restores the
// public roots alone.
//
// Returns one result per configured path. Never returns an error: no single
// store's fate should stop the other from being looked at, and a speaker that
// cannot repair itself must still start.
func RepairTrustStore(rootCAPEM []byte, logger *slog.Logger) []TrustRepairResult {
	return repairTrustStorePaths(DefaultTrustStorePaths, rootCAPEM, platformMountOps(), logger)
}

func repairTrustStorePaths(paths []string, rootCAPEM []byte, ops mountOps, logger *slog.Logger) []TrustRepairResult {
	if logger == nil {
		logger = slog.Default()
	}
	results := make([]TrustRepairResult, 0, len(paths))
	for i, path := range paths {
		// The slot number keeps the two overlays in separate files. They
		// shared one once, because the previous name carried a single hex
		// digit of an md5 and one collision in sixteen is not rare enough.
		results = append(results, repairOne(path, i+1, rootCAPEM, ops, logger))
	}
	lastRepairMu.Lock()
	lastRepair = append([]TrustRepairResult(nil), results...)
	lastRepairMu.Unlock()
	return results
}

func repairOne(path string, slot int, rootCAPEM []byte, ops mountOps, logger *slog.Logger) TrustRepairResult {
	res := TrustRepairResult{Path: path}

	before, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			res.Outcome = TrustRepairAbsent
			return res
		}
		res.Outcome = TrustRepairFailed
		res.Err = fmt.Sprintf("read: %v", err)
		logger.Warn("trust store repair: cannot read the store", "path", path, "err", err)
		return res
	}
	res.RootsBefore = publicRootCount(before, rootCAPEM)
	res.RootsAfter = res.RootsBefore
	if res.RootsBefore >= minPlausiblePublicRoots {
		res.Outcome = TrustRepairHealthy
		return res
	}

	// Broken. Everything past here changes the box's trust set, so say so
	// before touching anything: if the repair wedges the speaker, the log
	// still shows what it was about to do.
	logger.Warn("trust store lost its public roots, repairing",
		"path", path, "bytes", len(before), "certificateHeaders", strings.Count(string(before), pemCertHeader), "usableCertificates", usableRoots(before))

	if !ops.isMountPoint(path) {
		// Nothing mounted: this emptiness is the firmware's own file, on a
		// read-only rootfs. Not ours to fix, and not ours to have caused.
		res.Outcome = TrustRepairNotOverlaid
		logger.Error("trust store is empty but not overlaid by STR, leaving it alone", "path", path)
		return res
	}

	if err := ops.unmount(path); err != nil {
		res.Outcome = TrustRepairFailed
		res.Err = fmt.Sprintf("unmount: %v", err)
		logger.Error("trust store repair: unmount failed, the broken overlay stays live", "path", path, "err", err)
		return res
	}

	// The overlay is off, so this reads the pristine firmware bundle.
	firmware, err := os.ReadFile(path)
	if err != nil {
		res.Outcome = TrustRepairFailed
		res.Err = fmt.Sprintf("read firmware bundle: %v", err)
		logger.Error("trust store repair: firmware bundle unreadable after unmount", "path", path, "err", err)
		return res
	}
	res.FirmwareRoots = usableRoots(firmware)
	if publicRootCount(firmware, rootCAPEM) < minPlausiblePublicRoots {
		// Nothing to rebuild from. The broken overlay at least carried our
		// root, so put it back rather than leave the speaker with less than
		// it started with.
		res.Outcome = TrustRepairFirmwareEmpty
		if err := restoreOverlay(path, before, slot, ops); err != nil {
			res.Err = fmt.Sprintf("restore: %v", err)
		}
		res.RootsAfter = publicRootCountAt(path, rootCAPEM)
		logger.Error("trust store repair: the firmware bundle holds no public roots either",
			"path", path, "restoreErr", res.Err)
		return res
	}

	overlay := overlayPath(slot)
	rebuilt := appendRootBlock(firmware, rootCAPEM)
	if err := os.WriteFile(overlay, rebuilt, 0o644); err != nil {
		res.Outcome = TrustRepairFailed
		res.Err = fmt.Sprintf("write overlay: %v", err)
		res.RootsAfter = publicRootCountAt(path, rootCAPEM)
		logger.Error("trust store repair: cannot write the overlay, the firmware bundle stays live",
			"path", path, "overlay", overlay, "err", err)
		return res
	}
	res.Overlay = overlay
	if err := ops.bindMount(overlay, path); err != nil {
		res.Outcome = TrustRepairFailed
		res.Err = fmt.Sprintf("bind mount: %v", err)
		res.RootsAfter = publicRootCountAt(path, rootCAPEM)
		logger.Error("trust store repair: bind mount failed, the firmware bundle stays live",
			"path", path, "overlay", overlay, "err", err)
		return res
	}

	// Confirm through the mount, the same way the box's TLS clients will.
	res.RootsAfter = publicRootCountAt(path, rootCAPEM)
	if res.RootsAfter < minPlausiblePublicRoots {
		res.Outcome = TrustRepairFailed
		res.Err = "the rebuilt overlay still shows no public roots"
		logger.Error("trust store repair: rebuilt overlay reads back empty", "path", path, "overlay", overlay)
		return res
	}
	res.Outcome = TrustRepairRepaired
	logger.Warn("trust store repaired", "path", path, "overlay", overlay,
		"publicRoots", res.RootsAfter, "firmwareRoots", res.FirmwareRoots)
	return res
}

// restoreOverlay puts the original overlay content back over path, used when
// the pristine bundle turns out to be no better than what we took off.
func restoreOverlay(path string, original []byte, slot int, ops mountOps) error {
	overlay := overlayPath(slot)
	if err := os.WriteFile(overlay, original, 0o644); err != nil {
		return err
	}
	return ops.bindMount(overlay, path)
}

func overlayPath(slot int) string {
	return fmt.Sprintf("%s/streborn-trust%d.crt", overlayDir, slot)
}

// appendRootBlock returns bundle with rootCAPEM appended in the same marked
// form the boot script writes, so `cat` on the box reads the same either way.
// A nil or blank root yields the bundle unchanged.
func appendRootBlock(bundle, rootCAPEM []byte) []byte {
	var b bytes.Buffer
	b.Write(bundle)
	if len(bundle) > 0 && bundle[len(bundle)-1] != '\n' {
		b.WriteByte('\n')
	}
	if len(bytes.TrimSpace(rootCAPEM)) == 0 {
		return b.Bytes()
	}
	b.WriteString("\n# >>> STR Root CA >>>\n")
	b.Write(rootCAPEM)
	if rootCAPEM[len(rootCAPEM)-1] != '\n' {
		b.WriteByte('\n')
	}
	b.WriteString("# <<< STR Root CA <<<\n")
	return b.Bytes()
}

// minPlausiblePublicRoots is the smallest number of public roots a real
// firmware bundle can be believed to hold.
//
// "More than zero" is not the right test, and a field bundle proved it: an
// ST20 on the fixed build reported ca-bundle.crt with TWO certificates, STR's
// root plus a single survivor. One public root passed the old check as
// healthy, the repair skipped the file, and the speaker went on failing every
// handshake. Both stores on a working box of the same firmware carry 158 and
// 165. Anything in single digits is the same corruption caught one step later,
// not a legitimately small trust store, and treating it as healthy is how a
// broken box gets told it is fine.
const minPlausiblePublicRoots = 10

// publicRootCount counts the USABLE certificates in bundle that are not STR's
// own root. That is the figure that decides whether the box can reach the
// internet: a store holding only our root passes a naive "is it empty" check
// and still fails every public handshake, and so does a store full of text
// that merely looks like certificates. Counting headers here would also let
// the repair rebuild a store from a firmware bundle Go cannot read and then
// report that as repaired.
func publicRootCount(bundle, rootCAPEM []byte) int {
	n := usableRoots(bundle)
	if n > 0 && len(bytes.TrimSpace(rootCAPEM)) > 0 && bytes.Contains(bundle, bytes.TrimSpace(rootCAPEM)) {
		n--
	}
	return n
}

func publicRootCountAt(path string, rootCAPEM []byte) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return publicRootCount(b, rootCAPEM)
}
