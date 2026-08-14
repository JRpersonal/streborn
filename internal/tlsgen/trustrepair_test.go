package tlsgen

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

const testSTRRoot = "-----BEGIN CERTIFICATE-----\nSTRROOTCA\n-----END CERTIFICATE-----\n"

// wantPublic is how many public roots testPublic carries. It has to clear
// minPlausiblePublicRoots, because the repair judges a store by whether it
// holds a believable NUMBER of public roots rather than merely one.
const wantPublic = minPlausiblePublicRoots + 2

// testPublic stands in for a real firmware bundle.
var testPublic = func() string {
	out := ""
	for i := 0; i < wantPublic; i++ {
		out += fmt.Sprintf("-----BEGIN CERTIFICATE-----\nPUBLICROOT%02d\n-----END CERTIFICATE-----\n", i)
	}
	return out
}()

// fakeMounts models a bind mount over a single file: the "mounted" content
// shadows the pristine file, and unmounting reveals it again. That is the
// whole mechanism the repair depends on, so the test drives it directly
// instead of needing a real kernel mount.
type fakeMounts struct {
	t         *testing.T
	path      string
	pristine  []byte
	mounted   bool
	unmounts  int
	binds     int
	failUmnt  error
	failBind  error
	lastBound string
}

func newFakeMounts(t *testing.T, pristine, overlay string) *fakeMounts {
	t.Helper()
	// /tmp is the box's tmpfs and does not exist on a dev host.
	prev := overlayDir
	overlayDir = t.TempDir()
	t.Cleanup(func() { overlayDir = prev })

	f := &fakeMounts{t: t, path: filepath.Join(t.TempDir(), "ca-bundle.crt"), pristine: []byte(pristine)}
	if overlay != "" {
		f.mounted = true
		writeFile(t, f.path, overlay)
	} else {
		writeFile(t, f.path, pristine)
	}
	return f
}

func (f *fakeMounts) ops() mountOps {
	return mountOps{
		isMountPoint: func(string) bool { return f.mounted },
		unmount: func(p string) error {
			if f.failUmnt != nil {
				return f.failUmnt
			}
			f.unmounts++
			f.mounted = false
			writeFile(f.t, p, string(f.pristine))
			return nil
		},
		bindMount: func(src, dst string) error {
			if f.failBind != nil {
				return f.failBind
			}
			f.binds++
			f.lastBound = src
			b, err := os.ReadFile(src)
			if err != nil {
				return err
			}
			f.mounted = true
			writeFile(f.t, dst, string(b))
			return nil
		},
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestRepairRebuildsAStoreThatHoldsOnlyOurOwnRoot is the field case: the
// ST20 whose overlay carried STR's root and nothing else, so Deutschlandfunk
// and the Spotify engine both died on "certificate signed by unknown
// authority" (support mail 2026-08-13).
func TestRepairRebuildsAStoreThatHoldsOnlyOurOwnRoot(t *testing.T) {
	f := newFakeMounts(t, testPublic, testSTRRoot)

	res := repairTrustStorePaths([]string{f.path}, []byte(testSTRRoot), f.ops(), quietLogger())

	if len(res) != 1 {
		t.Fatalf("want one result, got %d", len(res))
	}
	if res[0].Outcome != TrustRepairRepaired {
		t.Fatalf("outcome = %q (err %q), want %q", res[0].Outcome, res[0].Err, TrustRepairRepaired)
	}
	if res[0].RootsBefore != 0 {
		t.Errorf("RootsBefore = %d, want 0 public roots before the repair", res[0].RootsBefore)
	}
	if res[0].RootsAfter != wantPublic {
		t.Errorf("RootsAfter = %d, want the %d public roots back", res[0].RootsAfter, wantPublic)
	}
	// The box has to trust the internet AND us: dropping our root would
	// break the Bose-domain server cert instead.
	live := readFile(t, f.path)
	if !strings.Contains(live, "PUBLICROOT00") || !strings.Contains(live, "PUBLICROOT05") {
		t.Error("repaired store lost the firmware's public roots")
	}
	if !strings.Contains(live, "STRROOTCA") {
		t.Error("repaired store lost STR's own root")
	}
	if f.unmounts != 1 || f.binds != 1 {
		t.Errorf("unmounts=%d binds=%d, want exactly one of each", f.unmounts, f.binds)
	}
}

func TestRepairLeavesAHealthyStoreUntouched(t *testing.T) {
	f := newFakeMounts(t, testPublic, testPublic+testSTRRoot)

	res := repairTrustStorePaths([]string{f.path}, []byte(testSTRRoot), f.ops(), quietLogger())

	if res[0].Outcome != TrustRepairHealthy {
		t.Fatalf("outcome = %q, want %q", res[0].Outcome, TrustRepairHealthy)
	}
	if f.unmounts != 0 || f.binds != 0 {
		t.Errorf("unmounts=%d binds=%d, a healthy store must not be touched", f.unmounts, f.binds)
	}
}

// TestRepairRestoresTheOverlayWhenTheFirmwareBundleIsEmptyToo guards against
// making things worse: if the pristine bundle has no public roots either, the
// box should end up with at least what it had, not less.
func TestRepairRestoresTheOverlayWhenTheFirmwareBundleIsEmptyToo(t *testing.T) {
	f := newFakeMounts(t, "", testSTRRoot)

	res := repairTrustStorePaths([]string{f.path}, []byte(testSTRRoot), f.ops(), quietLogger())

	if res[0].Outcome != TrustRepairFirmwareEmpty {
		t.Fatalf("outcome = %q, want %q", res[0].Outcome, TrustRepairFirmwareEmpty)
	}
	if !strings.Contains(readFile(t, f.path), "STRROOTCA") {
		t.Error("the rollback must put STR's root back, the box had it before")
	}
	if f.binds != 1 {
		t.Errorf("binds=%d, want the overlay remounted once", f.binds)
	}
}

// TestRepairLeavesTheFirmwareBundleLiveWhenRemountFails encodes the safe
// direction: a box that trusts the internet but not STR still plays radio and
// Spotify, while the reverse leaves it unable to reach anything.
func TestRepairLeavesTheFirmwareBundleLiveWhenRemountFails(t *testing.T) {
	f := newFakeMounts(t, testPublic, testSTRRoot)
	f.failBind = errors.New("mount: operation not permitted")

	res := repairTrustStorePaths([]string{f.path}, []byte(testSTRRoot), f.ops(), quietLogger())

	if res[0].Outcome != TrustRepairFailed {
		t.Fatalf("outcome = %q, want %q", res[0].Outcome, TrustRepairFailed)
	}
	if res[0].RootsAfter != wantPublic {
		t.Errorf("RootsAfter = %d, want the firmware's %d public roots live", res[0].RootsAfter, wantPublic)
	}
	if strings.Contains(readFile(t, f.path), "STRROOTCA") {
		t.Error("expected the pristine firmware bundle, not the broken overlay")
	}
}

// TestRepairKeepsTheBrokenOverlayWhenUnmountFails: if we cannot take the
// overlay off we have not changed anything, and the result has to say so
// rather than claim a repair.
func TestRepairKeepsTheBrokenOverlayWhenUnmountFails(t *testing.T) {
	f := newFakeMounts(t, testPublic, testSTRRoot)
	f.failUmnt = errors.New("umount: device or resource busy")

	res := repairTrustStorePaths([]string{f.path}, []byte(testSTRRoot), f.ops(), quietLogger())

	if res[0].Outcome != TrustRepairFailed {
		t.Fatalf("outcome = %q, want %q", res[0].Outcome, TrustRepairFailed)
	}
	if !strings.Contains(res[0].Err, "unmount") {
		t.Errorf("Err = %q, want it to name the unmount", res[0].Err)
	}
	if f.binds != 0 {
		t.Error("nothing may be mounted when the unmount failed")
	}
}

// TestRepairDoesNotTouchAnEmptyStoreThatIsNotOverlaid: an empty bundle with
// no mount over it is the firmware's own file on a read-only rootfs. Not ours
// to have caused and not ours to fix.
func TestRepairDoesNotTouchAnEmptyStoreThatIsNotOverlaid(t *testing.T) {
	f := newFakeMounts(t, "", "")
	f.mounted = false

	res := repairTrustStorePaths([]string{f.path}, []byte(testSTRRoot), f.ops(), quietLogger())

	if res[0].Outcome != TrustRepairNotOverlaid {
		t.Fatalf("outcome = %q, want %q", res[0].Outcome, TrustRepairNotOverlaid)
	}
	if f.unmounts != 0 || f.binds != 0 {
		t.Error("a store STR does not overlay must not be touched")
	}
}

func TestRepairReportsAnAbsentBundle(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-bundle.crt")

	res := repairTrustStorePaths([]string{missing}, []byte(testSTRRoot), platformMountOps(), quietLogger())

	if res[0].Outcome != TrustRepairAbsent {
		t.Fatalf("outcome = %q, want %q", res[0].Outcome, TrustRepairAbsent)
	}
}

// TestRepairGivesEachStoreItsOwnOverlayFile guards the collision that put
// both trust stores on one file: a shared overlay is how the empty-copy bug
// hit both at once.
func TestRepairGivesEachStoreItsOwnOverlayFile(t *testing.T) {
	if a, b := overlayPath(1), overlayPath(2); a == b {
		t.Fatalf("slot 1 and 2 share the overlay file %q", a)
	}
}

// TestRepairWithoutOurRootStillRestoresThePublicRoots: on a box whose CA is
// not generated yet, radio and Spotify still matter.
func TestRepairWithoutOurRootStillRestoresThePublicRoots(t *testing.T) {
	f := newFakeMounts(t, testPublic, "")
	f.mounted = true
	writeFile(t, f.path, "")

	res := repairTrustStorePaths([]string{f.path}, nil, f.ops(), quietLogger())

	if res[0].Outcome != TrustRepairRepaired {
		t.Fatalf("outcome = %q (err %q), want %q", res[0].Outcome, res[0].Err, TrustRepairRepaired)
	}
	if res[0].RootsAfter != wantPublic {
		t.Errorf("RootsAfter = %d, want %d", res[0].RootsAfter, wantPublic)
	}
}

func TestLastTrustRepairReportsTheMostRecentPass(t *testing.T) {
	f := newFakeMounts(t, testPublic, testSTRRoot)

	repairTrustStorePaths([]string{f.path}, []byte(testSTRRoot), f.ops(), quietLogger())

	last := LastTrustRepair()
	if len(last) != 1 || last[0].Outcome != TrustRepairRepaired {
		t.Fatalf("LastTrustRepair() = %+v, want one repaired entry", last)
	}
	// A copy, so a caller rendering it into a diagnostic bundle cannot
	// mutate what the next pass compares against.
	last[0].Outcome = TrustRepairFailed
	if LastTrustRepair()[0].Outcome != TrustRepairRepaired {
		t.Error("LastTrustRepair must hand out a copy")
	}
}

func TestAppendRootBlockSeparatesTheMarkerFromABundleWithoutATrailingNewline(t *testing.T) {
	out := string(appendRootBlock([]byte("-----END CERTIFICATE-----"), []byte(testSTRRoot)))
	if strings.Contains(out, "-----END CERTIFICATE----- ") || !strings.Contains(out, "-----END CERTIFICATE-----\n") {
		t.Errorf("marker glued to the last line:\n%s", out)
	}
	if !strings.Contains(out, "# >>> STR Root CA >>>") || !strings.Contains(out, "# <<< STR Root CA <<<") {
		t.Errorf("missing the markers the boot script writes:\n%s", out)
	}
}

// The field case the first version of this repair walked straight past: an
// ST20 on the fixed build reported ca-bundle.crt holding TWO certificates,
// STR's root plus one survivor. "More than zero public roots" called that
// healthy, the file was left alone, and the speaker went on failing every
// handshake while the diagnostic said it was fine.
func TestRepairRebuildsAStoreLeftWithASingleSurvivingRoot(t *testing.T) {
	const oneSurvivor = "-----BEGIN CERTIFICATE-----\nLONESURVIVOR\n-----END CERTIFICATE-----\n"
	f := newFakeMounts(t, testPublic, oneSurvivor+testSTRRoot)

	res := repairTrustStorePaths([]string{f.path}, []byte(testSTRRoot), f.ops(), quietLogger())

	if res[0].Outcome != TrustRepairRepaired {
		t.Fatalf("outcome = %q (err %q), want %q: one public root is the same corruption, not a healthy store",
			res[0].Outcome, res[0].Err, TrustRepairRepaired)
	}
	if res[0].RootsBefore != 1 {
		t.Errorf("RootsBefore = %d, want the single survivor counted", res[0].RootsBefore)
	}
	if res[0].RootsAfter != wantPublic {
		t.Errorf("RootsAfter = %d, want the firmware's %d roots back", res[0].RootsAfter, wantPublic)
	}
}

// A store that is merely SMALL but believable must still be left alone, so the
// threshold cannot be read as "rebuild anything that is not the biggest".
func TestRepairLeavesAPlausibleStoreAlone(t *testing.T) {
	f := newFakeMounts(t, testPublic, testPublic+testSTRRoot)

	res := repairTrustStorePaths([]string{f.path}, []byte(testSTRRoot), f.ops(), quietLogger())

	if res[0].Outcome != TrustRepairHealthy {
		t.Fatalf("outcome = %q, want %q", res[0].Outcome, TrustRepairHealthy)
	}
	if f.unmounts != 0 || f.binds != 0 {
		t.Error("a store with a believable number of roots must not be touched")
	}
}
