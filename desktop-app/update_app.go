package main

// In-app self-update (#71), phase 1: download the matching release asset for the
// host OS, verify its SHA256 against the release manifest, then install it.
//
// Install capability differs by OS, driven by how the app is shipped (a raw,
// UNSIGNED binary on every platform, signing is cost-deferred):
//   - Linux  : the asset is a .tar.gz with the binary inside. A running binary
//              can be replaced on Linux, so STR swaps itself and relaunches.
//   - Windows: the asset is the portable .exe. A running .exe cannot be
//              overwritten but CAN be renamed, so STR renames itself to .old,
//              drops the new .exe in place and relaunches; the .old is removed on
//              the next start.
//   - macOS  : the asset is a .dmg. An unsigned, un-notarized .app downloaded
//              this way is blocked by Gatekeeper, so STR only downloads+verifies
//              and opens the .dmg for the user to drag into Applications. Full
//              auto-replace waits for notarization.
//
// The check itself (CheckAppUpdate) is unchanged; this adds the download/verify/
// apply half the banner previously delegated to "open the website".

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	wailsrt "github.com/wailsapp/wails/v2/pkg/runtime"
)

// versionFromFilenameRE pulls a vX.Y.Z out of a release asset filename, e.g.
// "STR-Windows-v0.7.42.exe" -> "v0.7.42".
var versionFromFilenameRE = regexp.MustCompile(`v\d+\.\d+\.\d+`)

// resolveSecondInstanceExe turns the SingleInstanceLock second-instance args +
// working dir into the absolute path of the binary the user just launched.
func resolveSecondInstanceExe(args []string, wd string) string {
	if len(args) == 0 || args[0] == "" {
		return ""
	}
	p := args[0]
	if !filepath.IsAbs(p) && wd != "" {
		p = filepath.Join(wd, p)
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return p
}

// pathsEqual compares two file paths, case-insensitively on Windows.
func pathsEqual(a, b string) bool {
	ca, _ := filepath.Abs(filepath.Clean(a))
	cb, _ := filepath.Abs(filepath.Clean(b))
	if runtime.GOOS == "windows" {
		return strings.EqualFold(ca, cb)
	}
	return ca == cb
}

// tryHandOffTo handles the case where the user double-clicks a freshly downloaded
// NEWER build while this (older) one is running: the SingleInstanceLock would
// otherwise just raise this old window and the new binary would exit, leaving the
// user stuck on the old version. If the second instance is a different file whose
// filename version is strictly newer than ours, quit this one and start that one
// (via the same wait-for-our-pid helper), so the new version actually comes up.
// Returns true when it took over (caller then skips the raise-to-front).
//
// Guard rails: the same binary path just raises the window (no-op handoff); an
// unparseable or not-newer filename is NOT handed off, so a re-launch of the same
// or an older copy never downgrades silently.
func (a *App) tryHandOffTo(other string) bool {
	if other == "" {
		return false
	}
	self, err := os.Executable()
	if err != nil {
		return false
	}
	if r, e := filepath.EvalSymlinks(self); e == nil {
		self = r
	}
	if pathsEqual(self, other) {
		return false
	}
	ov := versionFromFilenameRE.FindString(filepath.Base(other))
	if ov == "" || !versionLess(appVersion, ov) {
		return false
	}
	a.logger.Info("second instance is a newer build; handing off instead of just focusing",
		"self", appVersion, "newVersion", ov, "path", other)
	a.relaunchAndQuit(other)
	return true
}

// newGETRequest builds a GET with STR's identifiable update user-agent, used for
// the manifest and asset downloads through updateHTTPClient (the pure-Go TLS
// client, see update_tls.go).
func newGETRequest(ctx context.Context, url string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "STReborn-Desktop ("+runtime.GOOS+"; "+runtime.GOARCH+")")
	return req, nil
}

// updateAssetKey maps the host OS to the manifest.json artifact key.
func updateAssetKey() string {
	switch runtime.GOOS {
	case "windows":
		return "desktop_windows"
	case "darwin":
		return "desktop_macos"
	case "linux":
		return "desktop_linux"
	}
	return ""
}

// canSelfReplace reports whether STR can install an update in place on this OS.
// macOS cannot until the .app is notarized (Gatekeeper blocks a downloaded,
// unsigned bundle), so there it stays assisted.
func canSelfReplace() bool { return runtime.GOOS == "linux" || runtime.GOOS == "windows" }

// UpdateAsset is the resolved download for the host OS, returned to the frontend
// so it can show the size/version and decide between "Install now" (self-replace)
// and "Download & open" (assisted, macOS).
type UpdateAsset struct {
	Version     string `json:"version"`
	SHA256      string `json:"sha256"`
	URL         string `json:"url"`
	Filename    string `json:"filename"`
	AutoInstall bool   `json:"autoInstall"`
}

// releaseManifestURL is the GitHub release manifest.json for a version tag. The
// manifest carries the per-OS asset url + sha256; reading it after the update
// check (which only tells us the version) keeps the download self-contained and
// independent of the website endpoint.
func releaseManifestURL(version string) string {
	return "https://github.com/JRpersonal/streborn/releases/download/" + version + "/manifest.json"
}

// releaseManifestLatestURL is the stable /releases/latest manifest. GitHub
// resolves /latest to the newest PUBLISHED release, so this never 404s on a tag
// that is still a draft or whose case differs from the manifest's version
// string. Used as the fallback when the version-pinned manifest is unreachable
// (the most likely cause of the "page not found" a user hit right after an
// update banner appeared).
func releaseManifestLatestURL() string {
	return "https://github.com/JRpersonal/streborn/releases/latest/download/manifest.json"
}

// ResolveUpdateAsset fetches the release manifest for version and returns the
// download for the host OS. Errors when the OS is unsupported or the manifest
// lacks the asset (a malformed/old release).
func (a *App) ResolveUpdateAsset(version string) (UpdateAsset, error) {
	key := updateAssetKey()
	if key == "" {
		return UpdateAsset{}, fmt.Errorf("unsupported OS %q", runtime.GOOS)
	}
	fetch := func(url string) (UpdateAsset, error) {
		ctx, cancel := context.WithTimeout(a.appCtx(), 15*time.Second)
		defer cancel()
		req, err := newGETRequest(ctx, url)
		if err != nil {
			return UpdateAsset{}, err
		}
		resp, err := updateHTTPClient().Do(req)
		if err != nil {
			return UpdateAsset{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return UpdateAsset{}, fmt.Errorf("manifest status %d", resp.StatusCode)
		}
		var m struct {
			Version   string `json:"version"`
			Artifacts map[string]struct {
				URL      string `json:"url"`
				SHA256   string `json:"sha256"`
				Filename string `json:"filename"`
			} `json:"artifacts"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&m); err != nil {
			return UpdateAsset{}, err
		}
		art, ok := m.Artifacts[key]
		if !ok || art.URL == "" || art.SHA256 == "" {
			return UpdateAsset{}, fmt.Errorf("release %s has no %s asset", m.Version, key)
		}
		return UpdateAsset{
			Version:     m.Version,
			SHA256:      strings.ToLower(strings.TrimSpace(art.SHA256)),
			URL:         art.URL,
			Filename:    art.Filename,
			AutoInstall: canSelfReplace(),
		}, nil
	}
	// Try the version-pinned manifest first; if it is unreachable (a draft tag, a
	// case mismatch, or a publish still propagating), fall back to /releases/latest
	// so the one-click update never dead-ends on "page not found".
	asset, err := fetch(releaseManifestURL(version))
	if err != nil {
		if asset2, err2 := fetch(releaseManifestLatestURL()); err2 == nil {
			return asset2, nil
		}
		return UpdateAsset{}, err
	}
	return asset, nil
}

// updateDir is the per-user cache dir STR downloads updates into.
func updateDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	d := filepath.Join(base, "STReborn", "updates")
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", err
	}
	return d, nil
}

// DownloadUpdate downloads the host-OS asset for version, verifies its SHA256
// against the release manifest, and returns the local file path. It streams the
// body to <updateDir>/<filename>.part, emitting "app:update:progress" (0-100)
// for a progress bar, then renames to the final name only after the hash checks
// out, so a partial/corrupt download never sits where Apply would pick it up.
func (a *App) DownloadUpdate(version string) (string, error) {
	asset, err := a.ResolveUpdateAsset(version)
	if err != nil {
		return "", err
	}
	dir, err := updateDir()
	if err != nil {
		return "", err
	}
	name := asset.Filename
	if name == "" {
		name = "STReborn-" + version + assetExt()
	}
	finalPath := filepath.Join(dir, name)
	partPath := finalPath + ".part"

	ctx, cancel := context.WithTimeout(a.appCtx(), 10*time.Minute)
	defer cancel()
	req, err := newGETRequest(ctx, asset.URL)
	if err != nil {
		return "", err
	}
	resp, err := updateHTTPClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download status %d", resp.StatusCode)
	}

	out, err := os.Create(partPath)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	total := resp.ContentLength
	var done int64
	lastPct := -1
	buf := make([]byte, 64*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				os.Remove(partPath)
				return "", werr
			}
			h.Write(buf[:n])
			done += int64(n)
			if total > 0 {
				if pct := int(done * 100 / total); pct != lastPct {
					lastPct = pct
					wailsrt.EventsEmit(a.appCtx(), "app:update:progress", pct)
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			os.Remove(partPath)
			return "", rerr
		}
	}
	if err := out.Close(); err != nil {
		os.Remove(partPath)
		return "", err
	}

	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, asset.SHA256) {
		os.Remove(partPath)
		return "", fmt.Errorf("checksum mismatch: got %s, expected %s", got, asset.SHA256)
	}
	if err := os.Rename(partPath, finalPath); err != nil {
		os.Remove(partPath)
		return "", err
	}
	a.logger.Info("update downloaded and verified", "version", version, "path", finalPath)
	return finalPath, nil
}

// assetExt is the fallback download extension per OS when the manifest omits a
// filename.
func assetExt() string {
	switch runtime.GOOS {
	case "windows":
		return ".exe"
	case "darwin":
		return ".dmg"
	default:
		return ".tar.gz"
	}
}

// ApplyUpdate installs a file produced by DownloadUpdate. On Linux and Windows it
// replaces the running binary and relaunches; on macOS it opens the .dmg for the
// user to drag into Applications (Gatekeeper blocks an unsigned auto-replace).
func (a *App) ApplyUpdate(downloadedPath string) error {
	if _, err := os.Stat(downloadedPath); err != nil {
		return fmt.Errorf("downloaded file missing: %w", err)
	}
	switch runtime.GOOS {
	case "darwin":
		// Assisted: just surface the verified .dmg; the user drags the new app in.
		return a.RevealUpdateFile(downloadedPath)
	case "windows":
		return a.applyWindows(downloadedPath)
	case "linux":
		return a.applyLinux(downloadedPath)
	}
	return fmt.Errorf("self-update not supported on %q", runtime.GOOS)
}

// applyWindows swaps the running .exe with the downloaded one using the
// rename-then-replace trick (a running .exe cannot be overwritten but can be
// renamed), then relaunches and quits. The .old is cleaned up on the next start.
func (a *App) applyWindows(newExe string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return fmt.Errorf("could not move the current app aside (is it in a write-protected folder?): %w", err)
	}
	if err := copyFile(newExe, exe); err != nil {
		// Roll the rename back so the app still launches next time.
		_ = os.Rename(old, exe)
		return fmt.Errorf("could not write the new app: %w", err)
	}
	a.relaunchAndQuit(exe)
	return nil
}

// applyLinux extracts the binary from the downloaded .tar.gz, swaps the running
// binary with it and relaunches.
func (a *App) applyLinux(tgz string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	dir, _ := updateDir()
	extracted := filepath.Join(dir, "STReborn.new")
	if err := extractLargestFile(tgz, extracted); err != nil {
		return fmt.Errorf("could not unpack the update: %w", err)
	}
	defer os.Remove(extracted)
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return fmt.Errorf("could not move the current app aside: %w", err)
	}
	if err := copyFile(extracted, exe); err != nil {
		_ = os.Rename(old, exe)
		return fmt.Errorf("could not write the new app: %w", err)
	}
	_ = os.Chmod(exe, 0o755)
	a.relaunchAndQuit(exe)
	return nil
}

// relaunchAndQuit launches the replaced binary AFTER this process has fully
// exited, then quits. The app holds a SingleInstanceLock (see main.go), so a new
// instance started while this one is still alive detects the old one and exits
// immediately, then the old one quits and nothing is left running (the "it closed
// but did not reopen" bug). The fix: spawn a small detached helper that waits for
// THIS pid to disappear (the lock is released on exit), then starts the new
// binary. The helper is orphaned by our quit but keeps running (Windows does not
// cascade-kill children; on Linux it reparents to init).
func (a *App) relaunchAndQuit(exe string) {
	pid := os.Getpid()
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// PowerShell ships on every supported Windows; Wait-Process blocks until
		// our pid is gone (lock freed), then Start-Process launches detached.
		ps := fmt.Sprintf("try { Wait-Process -Id %d -Timeout 30 } catch {}; Start-Sleep -Milliseconds 500; Start-Process -FilePath '%s'",
			pid, strings.ReplaceAll(exe, "'", "''"))
		cmd = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", ps)
	default:
		// Poll until our pid is gone, then exec the new binary (replaces the sh).
		sh := fmt.Sprintf("while kill -0 %d 2>/dev/null; do sleep 0.2; done; sleep 0.4; exec %s",
			pid, shSingleQuote(exe))
		cmd = exec.Command("sh", "-c", sh)
	}
	cmd.Dir = filepath.Dir(exe)
	if err := cmd.Start(); err != nil {
		a.logger.Warn("relaunch helper failed to start; please start the app manually", "err", err)
		return
	}
	a.logger.Info("update applied; relaunch helper armed, quitting so it can start the new version", "pid", pid)
	// Small grace so the helper is definitely running, then quit to release the
	// single-instance lock; the helper does the rest once we are gone.
	go func() {
		time.Sleep(400 * time.Millisecond)
		wailsrt.Quit(a.appCtx())
	}()
}

// shSingleQuote wraps s in POSIX single quotes for safe interpolation into the
// sh -c relaunch command, escaping any embedded single quote.
func shSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// RevealUpdateFile opens the OS file manager / mounts the .dmg at the downloaded
// file so the user can complete the install. Used on macOS and as the fallback
// when a self-replace is refused (e.g. a write-protected folder on Windows).
func (a *App) RevealUpdateFile(path string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", path).Start() // mounts the .dmg, Finder shows it
	case "windows":
		return exec.Command("explorer", "/select,", filepath.FromSlash(path)).Start()
	default:
		return exec.Command("xdg-open", filepath.Dir(path)).Start()
	}
}

// cleanupOldBinary removes a leftover "<exe>.old" from a previous Windows/Linux
// self-update. Called once on startup; best-effort (the file may still be locked
// for a moment right after the swap, in which case the next start clears it).
func (a *App) cleanupOldBinary() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	exe, _ = filepath.EvalSymlinks(exe)
	old := exe + ".old"
	if _, err := os.Stat(old); err == nil {
		if rmErr := os.Remove(old); rmErr != nil {
			a.logger.Info("update cleanup: previous binary still locked, will retry next start", "file", old)
		} else {
			a.logger.Info("update cleanup: removed previous binary", "file", old)
		}
	}
}

// copyFile copies src to dst (overwriting), preserving nothing but the bytes.
// Used instead of os.Rename for the swap because the download dir and the app
// dir can be on different volumes, where rename fails.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// extractLargestFile writes the largest regular file inside a .tar.gz to dst. The
// Linux desktop tarball holds a single binary (plus maybe a readme); the binary
// dominates by size, so "largest regular file" picks it without hardcoding a name
// that a future build rename would break.
func extractLargestFile(tgz, dst string) error {
	f, err := os.Open(tgz)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	// Two passes would need a re-open; instead buffer the best candidate to a temp
	// file and swap. Simpler: scan to find the largest header size, then re-open.
	var bestName string
	var bestSize int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeReg && h.Size > bestSize {
			bestSize = h.Size
			bestName = h.Name
		}
	}
	if bestName == "" {
		return fmt.Errorf("no file found in archive")
	}
	// Second pass: re-open and copy the chosen entry.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	gz2, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz2.Close()
	tr2 := tar.NewReader(gz2)
	for {
		h, err := tr2.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if h.Name != bestName {
			continue
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, io.LimitReader(tr2, bestSize)); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	}
	return fmt.Errorf("archive entry vanished between passes")
}
