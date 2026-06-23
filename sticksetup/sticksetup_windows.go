//go:build windows

package sticksetup

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

const (
	driveRemovable = 2
)

func listDrivesWindows() ([]Drive, error) {
	kernel := syscall.NewLazyDLL("kernel32.dll")
	getLogicalDrives := kernel.NewProc("GetLogicalDrives")
	getDriveType := kernel.NewProc("GetDriveTypeW")
	getDiskFreeSpaceEx := kernel.NewProc("GetDiskFreeSpaceExW")
	getVolumeInformation := kernel.NewProc("GetVolumeInformationW")

	mask, _, _ := getLogicalDrives.Call()
	if mask == 0 {
		return nil, fmt.Errorf("GetLogicalDrives returned 0")
	}

	var out []Drive
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		letter := string(rune('A' + i))
		root := letter + ":\\"
		rootPtr, _ := syscall.UTF16PtrFromString(root)

		typeVal, _, _ := getDriveType.Call(uintptr(unsafe.Pointer(rootPtr)))
		removable := typeVal == driveRemovable
		if !removable {
			continue
		}

		// Time the per-drive volume queries: on a just-inserted stick
		// these GetDiskFreeSpaceEx / GetVolumeInformation calls can block
		// for seconds while Windows finishes mounting, which is the
		// user-reported "search hangs 10-20s then the stick appears".
		dstart := time.Now()
		var freeBytesAvail, totalBytes, totalFreeBytes uint64
		_, _, _ = getDiskFreeSpaceEx.Call(
			uintptr(unsafe.Pointer(rootPtr)),
			uintptr(unsafe.Pointer(&freeBytesAvail)),
			uintptr(unsafe.Pointer(&totalBytes)),
			uintptr(unsafe.Pointer(&totalFreeBytes)),
		)

		volumeName := make([]uint16, 261)
		fsName := make([]uint16, 261)
		var volSerial, maxCompLen, fsFlags uint32
		_, _, _ = getVolumeInformation.Call(
			uintptr(unsafe.Pointer(rootPtr)),
			uintptr(unsafe.Pointer(&volumeName[0])),
			uintptr(len(volumeName)),
			uintptr(unsafe.Pointer(&volSerial)),
			uintptr(unsafe.Pointer(&maxCompLen)),
			uintptr(unsafe.Pointer(&fsFlags)),
			uintptr(unsafe.Pointer(&fsName[0])),
			uintptr(len(fsName)),
		)

		label := syscall.UTF16ToString(volumeName)
		fs := syscall.UTF16ToString(fsName)

		// The box only reads FAT32. On NTFS / exFAT / any other filesystem
		// the stick does NOT count as a Bose stick even if run.sh & co are
		// on it — otherwise the app would wrongly show "stick detected,
		// version 1.0.0" and write pointless files.
		hasStick := false
		if strings.EqualFold(fs, "FAT32") {
			hasStick = IsBoseStick(root)
		}

		out = append(out, Drive{
			Path:        root,
			Label:       label,
			TotalBytes:  int64(totalBytes),
			FreeBytes:   int64(totalFreeBytes),
			Filesystem:  fs,
			Removable:   true,
			HasStick:    hasStick,
			Description: descForDrive(root, label, fs, int64(totalBytes)),
		})
		Logger.Info("drive probed", "drive", root, "fs", fs,
			"ms", time.Since(dstart).Milliseconds(), "hasStick", hasStick)
	}
	return out, nil
}

func descForDrive(path, label, fs string, total int64) string {
	gb := float64(total) / (1024 * 1024 * 1024)
	if label == "" {
		return fmt.Sprintf("%s (%.1f GB, %s)", path, gb, fs)
	}
	return fmt.Sprintf("%s %s (%.1f GB, %s)", path, label, gb, fs)
}

// listDrivesMac and listDrivesLinux are not implemented on Windows, but
// we need the symbols so the shared sticksetup.go switch statement
// compiles.
func listDrivesMac() ([]Drive, error)   { return nil, fmt.Errorf("not on Mac") }
func listDrivesLinux() ([]Drive, error) { return nil, fmt.Errorf("not on Linux") }

// looksAppControlBlocked reports whether a stick-format failure is a Windows
// application-control / Smart App Control / WDAC / antivirus block of the
// unsigned winformat helper rather than a genuine format error. In that case
// the helper never executed (a Win 11 reporter saw every attempt end with "Eine
// Anwendungssteuerungsrichtlinie hat diese Datei blockiert"), so STR can retry
// with the Microsoft-signed Format-Volume cmdlet, which passes SAC/WDAC.
func looksAppControlBlocked(combined string) bool {
	l := strings.ToLower(combined)
	for _, m := range []string{
		"anwendungssteuerungsrichtlinie", // DE: application control policy
		"application control",
		"blocked this file",
		"blockiert", // DE: blocked
		"smart app control",
		"securitysoftware",
		"this program is blocked by group policy",
		"operation did not complete successfully because the file contains a virus",
	} {
		if strings.Contains(l, m) {
			return true
		}
	}
	return false
}

// psEncodeCommand encodes s as a PowerShell -EncodedCommand argument (UTF-16LE
// then base64). Building the elevated command this way in Go avoids every
// quoting hazard of passing a nested script across the Start-Process -Verb
// RunAs boundary.
func psEncodeCommand(s string) string {
	u := utf16.Encode([]rune(s))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		b[i*2] = byte(c)
		b[i*2+1] = byte(c >> 8)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// formatViaFormatVolume reformats the volume as FAT32 using the Microsoft-signed
// PowerShell Format-Volume cmdlet, run elevated. Unlike the embedded winformat
// helper this passes Smart App Control / WDAC because the binary is
// Microsoft-signed. Format-Volume caps FAT32 at ~32 GB, which is fine here: STR
// steers >34 GB sticks through a separate reformat path, and for <=32 GB sticks
// the default cluster size is one the speaker kernel can read. Used as the
// fallback when the unsigned helper is blocked.
func formatViaFormatVolume(letter, label string) error {
	// One UAC prompt; the elevated child runs Format-Volume and reports success
	// via its exit code. The inner script is base64-encoded (psEncodeCommand) and
	// passed as -EncodedCommand, so no quoting survives across the elevation.
	inner := fmt.Sprintf(
		`try { Format-Volume -DriveLetter %s -FileSystem FAT32 -NewFileSystemLabel '%s' -Force -Confirm:$false -ErrorAction Stop | Out-Null; exit 0 } catch { exit 70 }`,
		letter, strings.ReplaceAll(label, "'", "''"),
	)
	ps := fmt.Sprintf(
		`$ErrorActionPreference='Stop'; try { $p = Start-Process -FilePath 'powershell.exe' -ArgumentList '-NoProfile','-EncodedCommand','%s' -Verb RunAs -Wait -PassThru -WindowStyle Hidden; exit $p.ExitCode } catch { Write-Error $_.Exception.Message; exit 99 }`,
		psEncodeCommand(inner),
	)
	cmd := exec.Command("powershell.exe", "-NoProfile", "-Command", ps)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(out))
	if strings.Contains(msg, "1223") || strings.Contains(strings.ToLower(msg), "cancel") {
		return fmt.Errorf("admin permission was declined, please click Yes on the UAC dialog")
	}
	if looksAppControlBlocked(msg) {
		// Both the unsigned helper AND the signed cmdlet were blocked: surface the
		// distinct manual-format guidance code the frontend maps to a localized
		// message.
		return fmt.Errorf("format-blocked-manual: %s", msg)
	}
	return fmt.Errorf("Format-Volume fallback failed: %s", msg)
}

// formatFAT32Impl reformats the volume as FAT32 via the embedded winformat.exe
// helper, which calls FmIfs.dll FormatEx directly. That has no 32 GB limit
// (unlike the Format-Volume cmdlet) and returns a clean exit code. When the
// unsigned helper is blocked by Smart App Control / WDAC / antivirus it never
// runs, so the failure paths fall back to the Microsoft-signed Format-Volume
// cmdlet (formatViaFormatVolume).
//
// Flow:
//  1. Extract winformat.exe from the embed into TEMP
//  2. Launch it with Start-Process -Verb RunAs (the user sees ONE UAC prompt)
//  3. The helper runs elevated, calls FmIfs.FormatEx, writes the exit code
//  4. We read the status file for a meaningful error message
func formatFAT32Impl(path, label string) error {
	letter := strings.TrimSuffix(path, ":\\")
	letter = strings.TrimSuffix(letter, ":/")
	if len(letter) == 0 {
		return fmt.Errorf("no drive letter found in %q", path)
	}
	if label == "" {
		label = "REBORN"
	}

	if len(winformatBinary) == 0 {
		return fmt.Errorf("winformat helper missing from this build " +
			"(sticksetup/embedded/winformat.exe was not embedded; " +
			"please re-download the installer from the GitHub release page)")
	}

	// Extract the helper into TEMP. Unique name per call so old
	// instances do not lock the file.
	tmpDir := os.TempDir()
	stamp := time.Now().UnixNano()
	helperPath := filepath.Join(tmpDir, fmt.Sprintf("st-winformat-%d.exe", stamp))
	statusPath := filepath.Join(tmpDir, fmt.Sprintf("st-winformat-%d.status", stamp))
	if err := os.WriteFile(helperPath, winformatBinary, 0o755); err != nil {
		return fmt.Errorf("write format helper to temp: %w", err)
	}
	defer os.Remove(helperPath)
	defer os.Remove(statusPath)

	// Start-Process -Verb RunAs shows the UAC prompt and waits for exit.
	// IMPORTANT: -RedirectStandardError is NOT allowed with -Verb
	// (PowerShell throws ParameterBindingException), so the helper writes
	// the result into the status file itself. We return $LASTEXITCODE to
	// the app.
	ps := fmt.Sprintf(
		`$ErrorActionPreference='Stop'; try { $p = Start-Process -FilePath '%s' -ArgumentList '%s','%s','%s' -Verb RunAs -Wait -PassThru -WindowStyle Hidden; exit $p.ExitCode } catch { Write-Error $_.Exception.Message; exit 99 }`,
		strings.ReplaceAll(helperPath, `'`, `''`),
		letter,
		strings.ReplaceAll(label, "'", "''"),
		strings.ReplaceAll(statusPath, `'`, `''`),
	)
	cmd := exec.Command("powershell.exe", "-NoProfile", "-Command", ps)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, runErr := cmd.CombinedOutput()

	// Read the status file — this is the source of truth.
	statusData, _ := os.ReadFile(statusPath)
	status := strings.TrimSpace(string(statusData))

	if strings.HasPrefix(status, "OK") {
		return nil
	}

	// No OK status: UAC declined, the helper reported an error, or the unsigned
	// helper was blocked by Smart App Control / WDAC / antivirus before it could
	// run. In the block case the helper produced no status, so the signed
	// Format-Volume cmdlet is retried (it passes SAC/WDAC).
	combined := strings.TrimSpace(string(out)) + " " + status
	helperBlocked := looksAppControlBlocked(combined) ||
		(runErr != nil && status == "") // helper never executed at all

	if runErr != nil {
		msg := strings.TrimSpace(string(out))
		// Exit Code 1223 = ERROR_CANCELLED (UAC declined by user).
		if strings.Contains(msg, "1223") || strings.Contains(strings.ToLower(msg), "cancel") {
			return fmt.Errorf("admin permission was declined, please click Yes on the UAC dialog")
		}
		if helperBlocked {
			Logger.Warn("winformat helper blocked, falling back to signed Format-Volume",
				"drive", path, "detail", msg)
			return formatViaFormatVolume(letter, label)
		}
		if status != "" {
			return fmt.Errorf("format failed: %s", strings.TrimPrefix(status, "ERR "))
		}
		if msg != "" {
			return fmt.Errorf("format failed (PowerShell): %s", msg)
		}
		return fmt.Errorf("format failed: %v", runErr)
	}

	if status != "" {
		return fmt.Errorf("format failed: %s", strings.TrimPrefix(status, "ERR "))
	}
	// No status file and no runErr: helper never ran (UAC dismissed, or AV /
	// app-control silently blocked the helper binary). Try the signed cmdlet.
	Logger.Warn("winformat helper produced no status, falling back to signed Format-Volume", "drive", path)
	return formatViaFormatVolume(letter, label)
}

// ejectImpl ejects the volume cleanly via the Win32 API directly.
// The procedure is the Microsoft standard for "Safely Remove Hardware":
//  1. CreateFile(\\.\X:) — handle to the volume
//  2. FSCTL_LOCK_VOLUME — exclusive lock so nobody writes anymore
//  3. FSCTL_DISMOUNT_VOLUME — remove the filesystem mount
//  4. IOCTL_STORAGE_MEDIA_REMOVAL — turn off the hardware removal guard
//  5. IOCTL_STORAGE_EJECT_MEDIA — tell the USB driver to release the
//     device (no more disconnect sound when pulling it)
//
// This is considerably more robust than the Shell.Application Eject verb,
// which often only removes the drive letter but leaves the USB driver
// active.
func ejectImpl(path string) error {
	letter := strings.TrimSuffix(path, ":\\")
	letter = strings.TrimSuffix(letter, ":/")
	if len(letter) == 0 {
		return fmt.Errorf("no drive letter in %q", path)
	}

	// Let write caches flush
	time.Sleep(1 * time.Second)

	drivePath := `\\.\` + letter + ":"

	const (
		genericRead  = 0x80000000
		genericWrite = 0x40000000
		fileShareRW  = 0x00000003
		openExisting = 3

		fsctlLockVolume        = 0x00090018
		fsctlDismountVolume    = 0x00090020
		ioctlStorageMediaRemov = 0x002D4804
		ioctlStorageEjectMedia = 0x002D4808
	)

	k32 := syscall.NewLazyDLL("kernel32.dll")
	createFileW := k32.NewProc("CreateFileW")
	deviceIoControl := k32.NewProc("DeviceIoControl")
	closeHandle := k32.NewProc("CloseHandle")

	utf16, err := syscall.UTF16PtrFromString(drivePath)
	if err != nil {
		return err
	}
	h, _, lastErr := createFileW.Call(
		uintptr(unsafe.Pointer(utf16)),
		genericRead|genericWrite,
		fileShareRW,
		0,
		openExisting,
		0,
		0,
	)
	if h == 0 || h == ^uintptr(0) {
		return fmt.Errorf("CreateFile %s: %v", drivePath, lastErr)
	}
	defer closeHandle.Call(h)

	var bytesReturned uint32
	// Lock — retry because other processes (the indexer) may still be accessing it
	var lockErr error
	for i := 0; i < 10; i++ {
		r, _, e := deviceIoControl.Call(h, fsctlLockVolume, 0, 0, 0, 0, uintptr(unsafe.Pointer(&bytesReturned)), 0)
		if r != 0 {
			lockErr = nil
			break
		}
		lockErr = e
		time.Sleep(200 * time.Millisecond)
	}
	if lockErr != nil {
		return fmt.Errorf("lock volume: %v", lockErr)
	}

	// Dismount the filesystem
	if r, _, e := deviceIoControl.Call(h, fsctlDismountVolume, 0, 0, 0, 0, uintptr(unsafe.Pointer(&bytesReturned)), 0); r == 0 {
		return fmt.Errorf("dismount: %v", e)
	}

	// Enable media removal (PREVENT_MEDIA_REMOVAL = 0 = false)
	var preventRemoval byte = 0
	deviceIoControl.Call(h, ioctlStorageMediaRemov, uintptr(unsafe.Pointer(&preventRemoval)), 1, 0, 0, uintptr(unsafe.Pointer(&bytesReturned)), 0)

	// Eject — the actual "Safe to Remove" hardware trigger
	if r, _, e := deviceIoControl.Call(h, ioctlStorageEjectMedia, 0, 0, 0, 0, uintptr(unsafe.Pointer(&bytesReturned)), 0); r == 0 {
		return fmt.Errorf("eject media: %v", e)
	}
	return nil
}

var _ = filepath.Join
