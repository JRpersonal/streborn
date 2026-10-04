package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Leaving an updated app under the stable name.
//
// Since v1.0.2 the release publishes STR-Windows.exe with no version in the
// name. The Windows self-update swaps the running file in place, which kept
// whatever name that file had: somebody who started from STR-Windows-v0.9.95.exe
// and updated to v1.0.2 was left with a file whose name still said 0.9.95.
// Exactly the confusion the stable names were meant to end (Jens, 2026-10-04).
//
// So an update that runs from an old versioned file installs the new build as
// STR-Windows.exe next to it, moves the old file aside for the next start to
// remove, and points the user's shortcuts at the new file. A name the user chose
// themselves ("STR.exe") is theirs and stays untouched.

// stableWindowsExeName is the published Windows file name since v1.0.2.
const stableWindowsExeName = "STR-Windows.exe"

// staleOldBinary matches what a swap leaves behind in the app's folder: the
// previous file, published or stable name, with ".old" appended. Anchored on
// the published family so the cleanup can never reach anything that is not ours.
var staleOldBinary = regexp.MustCompile(`(?i)^STR-Windows[^/\\]*\.exe\.old$`)

// windowsInstallPlan says where an update writes the new build.
type windowsInstallPlan struct {
	// Target is the path the new build is written to. Equal to the running exe
	// for an in-place swap.
	Target string
	// Renamed is true when Target differs from the running exe, i.e. the update
	// moves an old versioned file over to the stable name.
	Renamed bool
	// ReplaceExisting is true when Target already exists (an earlier download)
	// and is older than or equal to the update, so it is moved aside to
	// Target+".old" before the new build is written.
	ReplaceExisting bool
	// HandOff names an existing STR-Windows.exe that is NEWER than the update.
	// Nothing is written then; the app hands over to that copy.
	HandOff string
	// Why is a short reason for the log.
	Why string
}

// planWindowsInstall decides where the update goes. exe is the running file,
// update the version being installed. exists and versionOf are injected so the
// decision can be tested without touching a disk or a version resource.
func planWindowsInstall(exe, update string, exists func(string) bool, versionOf func(string) (int, int, int, bool)) windowsInstallPlan {
	inPlace := windowsInstallPlan{Target: exe}
	base := filepath.Base(exe)
	if !strExeName.MatchString(base) {
		inPlace.Why = "the running file is not an old versioned name, swapping in place"
		return inPlace
	}
	stable := filepath.Join(filepath.Dir(exe), stableWindowsExeName)
	if strings.EqualFold(stable, exe) {
		inPlace.Why = "already the stable name"
		return inPlace
	}
	if !exists(stable) {
		return windowsInstallPlan{Target: stable, Renamed: true, Why: "old versioned name, installing under the stable name"}
	}
	uMaj, uMin, uPatch, uok := parseAppVersion(update)
	sMaj, sMin, sPatch, sok := versionOf(stable)
	if !uok || !sok {
		// A file called STR-Windows.exe whose version cannot be read could be
		// anything. Leave it alone and keep the swap where it always was.
		inPlace.Why = "an existing " + stableWindowsExeName + " has no readable version, swapping in place"
		return inPlace
	}
	if newer(sMaj, sMin, sPatch, uMaj, uMin, uPatch) {
		return windowsInstallPlan{Target: exe, HandOff: stable, Why: "an existing " + stableWindowsExeName + " is newer than this update"}
	}
	return windowsInstallPlan{Target: stable, Renamed: true, ReplaceExisting: true, Why: "replacing an older or equal " + stableWindowsExeName}
}

// removeStaleOldBinaries deletes the ".old" files a swap left in dir: the
// running exe's own and, after a rename to the stable name, the old versioned
// file's (STR-Windows-v0.9.95.exe.old). Returns the removed and the still
// locked names. Only names of the published family are considered.
func removeStaleOldBinaries(dir string) (removed, locked []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	for _, e := range entries {
		if e.IsDir() || !staleOldBinary.MatchString(e.Name()) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if err := os.Remove(p); err != nil {
			locked = append(locked, e.Name())
			continue
		}
		removed = append(removed, e.Name())
	}
	return removed, locked
}

// installUnderStableName writes the new build to plan.Target (the stable name)
// and returns the path to launch. Order matters, because at every step the user
// must still have a working copy:
//
//  1. an older STR-Windows.exe that is in the way is moved aside, not deleted;
//  2. the new build is written; if that fails, step 1 is undone and the caller
//     falls back to the in-place swap, which leaves the running file untouched;
//  3. only then the running, old-named file is renamed to ".old" so the next
//     start removes it (a running .exe can be renamed, not deleted). If that
//     rename fails the old file simply stays next to the new one;
//  4. shortcuts that opened the old file are pointed at the new one.
func installUnderStableName(exe, newExe string, plan windowsInstallPlan, rt shortcutRetargeter, logf func(msg string, args ...any)) (string, error) {
	target := plan.Target
	moved := false
	if plan.ReplaceExisting {
		_ = os.Remove(target + ".old")
		if err := os.Rename(target, target+".old"); err != nil {
			return "", err
		}
		moved = true
	}
	if err := copyFile(newExe, target); err != nil {
		_ = os.Remove(target)
		if moved {
			_ = os.Rename(target+".old", target)
		}
		return "", err
	}
	_ = os.Remove(exe + ".old")
	if err := os.Rename(exe, exe+".old"); err != nil {
		logf("update: the old file could not be moved aside and stays next to the new one", "file", exe, "err", err)
	}
	if rt != nil {
		changed, err := rt.Retarget(exe, target)
		if err != nil {
			logf("update: shortcuts could not all be updated", "err", err, "changed", len(changed))
		} else if len(changed) > 0 {
			logf("update: shortcuts now open the stable name", "changed", changed)
		}
	}
	return target, nil
}

// updateVersionOf tells the version of a downloaded build: from its version
// resource (Windows), else from a versioned file name in the update cache.
// Empty when neither says, which makes the plan swap in place.
func updateVersionOf(path string) string {
	if maj, min, patch, ok := exeResourceVersion(path); ok {
		return fmt.Sprintf("v%d.%d.%d", maj, min, patch)
	}
	if maj, min, patch, ok := parseExeVersion(filepath.Base(path)); ok {
		return fmt.Sprintf("v%d.%d.%d", maj, min, patch)
	}
	return ""
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// psQuote wraps s for a single-quoted PowerShell string literal, where the only
// special character is the single quote itself (doubled).
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// shortcutRetargetScript is the PowerShell the Windows retargeter runs. It
// prints the full path of every shortcut it changed, one per line. The Desktop
// is searched flat (a redirected OneDrive desktop can hold thousands of files),
// the Start Menu and the taskbar pins recursively.
func shortcutRetargetScript(oldPath, newPath string) string {
	return "$ErrorActionPreference='SilentlyContinue';" +
		"$old=" + psQuote(oldPath) + ";$new=" + psQuote(newPath) + ";" +
		"$sh=New-Object -ComObject WScript.Shell;" +
		"$places=@(@([Environment]::GetFolderPath('Desktop'),$false),@([Environment]::GetFolderPath('Programs'),$true)," +
		"@((Join-Path $env:APPDATA 'Microsoft\\Internet Explorer\\Quick Launch\\User Pinned\\TaskBar'),$true));" +
		"foreach($p in $places){$d=$p[0];if(-not $d -or -not (Test-Path -LiteralPath $d)){continue};" +
		"Get-ChildItem -LiteralPath $d -Filter *.lnk -File -Recurse:$p[1] | ForEach-Object {" +
		"$l=$sh.CreateShortcut($_.FullName);" +
		"if($l.TargetPath -and ($l.TargetPath -ieq $old)){$l.TargetPath=$new;$l.Save();$_.FullName}}}"
}

// shortcutRetargeter points shortcuts that open oldPath at newPath instead.
// Returns the shortcut files it changed. Best effort by contract: an error is
// logged, never allowed to fail an update.
type shortcutRetargeter interface {
	Retarget(oldPath, newPath string) ([]string, error)
}
