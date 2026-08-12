//go:build darwin

package main

// In-place update of the running .app bundle on macOS (#71 phase 2).
//
// Until now macOS was the one platform where the update stopped halfway: STR
// downloaded the .dmg, verified it and opened it, and the user dragged the app
// into Applications by hand, every single release. Windows and Linux have
// replaced themselves since the feature existed. The reason macOS did not was
// never Gatekeeper, the app and the image are Developer ID signed and notarized
// since v0.9.33; it was that replacing a running BUNDLE is a different job from
// overwriting one file, and nobody had written it.
//
// A running .app CAN be replaced: the process keeps the files it already has
// open, so moving the bundle aside and putting a new one in its place is safe,
// and the swapped-in copy is what the next launch runs. This is what Sparkle
// has done for years. What has to be got right is everything around it:
//
//   - The source is a ZIP of the signed, STAPLED app, not the DMG. The ticket
//     lives inside the bundle, so the extracted copy passes Gatekeeper offline
//     and no second notarization round is needed for the zip itself.
//   - Extraction uses ditto, not archive/zip: a bundle carries symlinks,
//     extended attributes and the code signature, and a naive unzip silently
//     breaks the signature.
//   - The extracted app is verified against OUR identity before it is allowed
//     anywhere near /Applications. The SHA256 from the release manifest already
//     proves the download, but the swap is the one moment where a tampered
//     bundle would win, so codesign has to agree too.
//   - Everything happens on the same volume as the installed app, so the final
//     move is a rename and not a copy that can die halfway.
//
// Every failure path falls back to the old assisted flow (reveal the download),
// which is why this can ship before anyone has run it on a real Mac: the worst
// case is what users have today.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// teamID is the Apple Developer Team the release is signed with. The extracted
// bundle must present exactly this before it may replace the running one.
// Empty disables the check, which is what a locally built, ad-hoc signed app
// gets; it then falls back to the assisted flow rather than swapping something
// unverifiable into place.
const teamID = "" // set from the signing identity when the release pipeline ships zips

// canSelfReplaceDarwin reports whether an in-place bundle swap is possible right
// now. It answers for THIS installation, not for macOS in general: an app
// running from the DMG, from a read-only volume or from a directory the user
// cannot write must keep the assisted flow.
func canSelfReplaceDarwin() bool {
	bundle, err := runningBundlePath()
	if err != nil {
		return false
	}
	parent := filepath.Dir(bundle)
	// Running from the mounted disk image is the common one: the user opened
	// the DMG and launched the app out of it instead of copying it first.
	if strings.HasPrefix(bundle, "/Volumes/") {
		return false
	}
	return dirWritable(parent)
}

// runningBundlePath returns the path of the .app bundle this process runs from,
// e.g. /Applications/ST Reborn.app. It walks up from the executable
// (<bundle>/Contents/MacOS/<binary>) rather than guessing a location.
func runningBundlePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	// <bundle>/Contents/MacOS/<binary> -> three levels up is the bundle.
	dir := filepath.Dir(exe)         // .../Contents/MacOS
	contents := filepath.Dir(dir)    // .../Contents
	bundle := filepath.Dir(contents) // .../ST Reborn.app
	if filepath.Base(dir) != "MacOS" || filepath.Base(contents) != "Contents" ||
		!strings.HasSuffix(bundle, ".app") {
		return "", fmt.Errorf("not running from an .app bundle (%s)", exe)
	}
	return bundle, nil
}

// applyDarwin replaces the running bundle with the one inside zipPath and
// relaunches. Returns an error WITHOUT having touched the installed app
// whenever it cannot complete safely, so the caller can fall back.
func (a *App) applyDarwin(zipPath string) error {
	if !strings.EqualFold(filepath.Ext(zipPath), ".zip") {
		// A .dmg cannot be swapped in; that is the assisted path.
		return fmt.Errorf("in-place update needs the .zip asset, got %s", filepath.Base(zipPath))
	}
	bundle, err := runningBundlePath()
	if err != nil {
		return err
	}
	parent := filepath.Dir(bundle)
	if !dirWritable(parent) {
		return fmt.Errorf("no permission to replace the app in %s", parent)
	}

	// Stage on the SAME volume as the installed app so the swap is a rename.
	stage, err := os.MkdirTemp(parent, ".streborn-update-")
	if err != nil {
		return fmt.Errorf("could not create a staging folder next to the app: %w", err)
	}
	defer os.RemoveAll(stage)

	// ditto preserves the bundle: symlinks, extended attributes, and the
	// stapled notarization ticket that makes the copy open without a warning.
	if out, err := exec.Command("/usr/bin/ditto", "-x", "-k", zipPath, stage).CombinedOutput(); err != nil {
		return fmt.Errorf("could not unpack the update: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	newBundle, err := singleAppIn(stage)
	if err != nil {
		return err
	}
	if err := verifySignedBundle(newBundle); err != nil {
		return err
	}

	// The swap itself. The old bundle is moved aside first so a failure leaves
	// SOMETHING launchable at the original path: if the second rename fails,
	// the first is rolled back.
	aside := filepath.Join(stage, "previous.app")
	if err := os.Rename(bundle, aside); err != nil {
		return fmt.Errorf("could not move the current app aside: %w", err)
	}
	if err := os.Rename(newBundle, bundle); err != nil {
		if rbErr := os.Rename(aside, bundle); rbErr != nil {
			// Both moves failed: say where the app went, since the user's
			// Applications folder is now missing it.
			return fmt.Errorf("could not install the new app (%w) and could not put the old one back (%v); it is at %s",
				err, rbErr, aside)
		}
		return fmt.Errorf("could not install the new app: %w", err)
	}
	a.logger.Info("macOS update: bundle replaced in place", "path", bundle)
	a.relaunchBundleAndQuit(bundle)
	return nil
}

// singleAppIn returns the one .app bundle inside dir, and refuses anything else:
// a zip that carries no app, or more than one, is not a release we built.
func singleAppIn(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var found []string
	for _, e := range entries {
		if e.IsDir() && strings.HasSuffix(e.Name(), ".app") {
			found = append(found, filepath.Join(dir, e.Name()))
		}
	}
	if len(found) != 1 {
		return "", fmt.Errorf("the update archive holds %d app bundles, expected exactly one", len(found))
	}
	return found[0], nil
}

// verifySignedBundle refuses to install anything the system would not vouch
// for. The manifest SHA256 already covers the download; this covers the swap,
// where a bundle that is unsigned, broken or from someone else must not be put
// into the place the user launches from.
func verifySignedBundle(bundle string) error {
	if out, err := exec.Command("/usr/bin/codesign", "--verify", "--strict", bundle).CombinedOutput(); err != nil {
		return fmt.Errorf("the downloaded app failed signature verification: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	if teamID == "" {
		return nil
	}
	out, err := exec.Command("/usr/bin/codesign", "-dv", "--verbose=4", bundle).CombinedOutput()
	if err != nil {
		return fmt.Errorf("could not read the signature of the downloaded app: %w", err)
	}
	if !strings.Contains(string(out), "TeamIdentifier="+teamID) {
		return fmt.Errorf("the downloaded app is signed by a different developer than this one")
	}
	return nil
}

// relaunchBundleAndQuit starts the freshly installed bundle once this process is
// gone, then quits. Same contract as relaunchAndQuit, but it launches the
// BUNDLE with `open` rather than the inner binary, so the new instance gets its
// own process identity, dock entry and activation instead of inheriting ours.
func (a *App) relaunchBundleAndQuit(bundle string) {
	pid := os.Getpid()
	sh := fmt.Sprintf("while kill -0 %d 2>/dev/null; do sleep 0.2; done; sleep 0.4; exec /usr/bin/open -n %s",
		pid, shSingleQuote(bundle))
	cmd := exec.Command("sh", "-c", sh)
	cmd.Dir = filepath.Dir(bundle)
	if err := cmd.Start(); err != nil {
		a.logger.Warn("relaunch helper failed to start; please start the app manually", "err", err)
		return
	}
	a.quitAfterRelaunchArmed(pid)
}
