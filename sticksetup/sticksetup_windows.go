//go:build windows

package sticksetup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
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
		return nil, fmt.Errorf("GetLogicalDrives lieferte 0")
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

		// Box liest nur FAT32. Auf NTFS / exFAT / sonstigem Filesystem
		// gilt der Stick NICHT als Bose Stick auch wenn run.sh & Co
		// drauf liegen — sonst zeigt die App faelschlich "Stick
		// erkannt, Version 1.0.0" und schreibt sinnlose Files.
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

// listDrivesMac und listDrivesLinux sind auf Windows nicht implementiert
// aber wir brauchen die Symbole damit das gemeinsame sticksetup.go Switch
// statement kompiliert.
func listDrivesMac() ([]Drive, error)   { return nil, fmt.Errorf("nicht auf Mac") }
func listDrivesLinux() ([]Drive, error) { return nil, fmt.Errorf("nicht auf Linux") }

// formatFAT32Impl formatiert das Volume neu als FAT32 via embeddedem
// winformat.exe Helper, der FmIfs.dll FormatEx direkt aufruft. Das hat
// kein 32 GB Limit (anders als Format-Volume Cmdlet) und liefert einen
// sauberen Exit Code zurueck.
//
// Ablauf:
//  1. winformat.exe aus dem Embed in TEMP extrahieren
//  2. Mit Start-Process -Verb RunAs starten — User sieht EINEN UAC Prompt
//  3. Helper laeuft elevated, ruft FmIfs.FormatEx, schreibt Exit Code
//  4. Wir lesen stderr fuer eine sinnvolle Fehlermeldung
func formatFAT32Impl(path, label string) error {
	letter := strings.TrimSuffix(path, ":\\")
	letter = strings.TrimSuffix(letter, ":/")
	if len(letter) == 0 {
		return fmt.Errorf("kein Drive Letter in %q", path)
	}
	if label == "" {
		label = "REBORN"
	}

	if len(winformatBinary) == 0 {
		return fmt.Errorf("winformat Helper fehlt im Build (sticksetup/embedded/winformat.exe nicht eingebettet)")
	}

	// Helper in TEMP extrahieren. Eindeutiger Name pro Aufruf damit
	// alte Instanzen das File nicht sperren.
	tmpDir := os.TempDir()
	stamp := time.Now().UnixNano()
	helperPath := filepath.Join(tmpDir, fmt.Sprintf("st-winformat-%d.exe", stamp))
	statusPath := filepath.Join(tmpDir, fmt.Sprintf("st-winformat-%d.status", stamp))
	if err := os.WriteFile(helperPath, winformatBinary, 0o755); err != nil {
		return fmt.Errorf("helper schreiben: %w", err)
	}
	defer os.Remove(helperPath)
	defer os.Remove(statusPath)

	// Start-Process -Verb RunAs zeigt den UAC Prompt und wartet auf
	// Exit. WICHTIG: -RedirectStandardError ist mit -Verb NICHT
	// erlaubt (PowerShell wirft ParameterBindingException), daher
	// schreibt der Helper das Ergebnis selbst in die Status Datei.
	// $LASTEXITCODE liefern wir an die App zurueck.
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

	// Status Datei lesen — das ist die Quelle der Wahrheit.
	statusData, _ := os.ReadFile(statusPath)
	status := strings.TrimSpace(string(statusData))

	if strings.HasPrefix(status, "OK") {
		return nil
	}

	// Kein OK Status → entweder UAC abgelehnt oder Helper hat Fehler gemeldet.
	if runErr != nil {
		msg := strings.TrimSpace(string(out))
		// Exit Code 1223 = ERROR_CANCELLED (UAC vom User abgelehnt).
		if strings.Contains(msg, "1223") || strings.Contains(strings.ToLower(msg), "cancel") {
			return fmt.Errorf("Admin Berechtigung wurde abgelehnt — bitte beim UAC Dialog auf Ja klicken")
		}
		if status != "" {
			return fmt.Errorf("Format fehlgeschlagen: %s", strings.TrimPrefix(status, "ERR "))
		}
		if msg != "" {
			return fmt.Errorf("Format fehlgeschlagen (PowerShell): %s", msg)
		}
		return fmt.Errorf("Format fehlgeschlagen: %v", runErr)
	}

	if status != "" {
		return fmt.Errorf("Format fehlgeschlagen: %s", strings.TrimPrefix(status, "ERR "))
	}
	// Kein Status File geschrieben + kein Error → Helper ist nie gelaufen
	// (z.B. weil User UAC vor dem Prompt schon geklickt hatte oder
	// AntiVirus den Helper geblockt hat).
	return fmt.Errorf("Format fehlgeschlagen: Helper wurde nicht ausgefuehrt (UAC abgelehnt oder AV Block?)")
}

// ejectImpl wirft das Volume sauber aus via Win32 API direkt.
// Vorgehen ist der Microsoft Standard fuer "Safely Remove Hardware":
//   1. CreateFile(\\.\X:) — Handle aufs Volume
//   2. FSCTL_LOCK_VOLUME — exklusiver Lock damit niemand mehr schreibt
//   3. FSCTL_DISMOUNT_VOLUME — Filesystem-Mount entfernen
//   4. IOCTL_STORAGE_MEDIA_REMOVAL — Hardware Removal-Schutz aus
//   5. IOCTL_STORAGE_EJECT_MEDIA — den USB Driver anweisen das Geraet
//      freizugeben (kein Disconnect Sound beim Ziehen mehr)
//
// Das ist deutlich robuster als Shell.Application Eject Verb, das oft
// nur den Drive Letter entfernt aber USB Driver aktiv laesst.
func ejectImpl(path string) error {
	letter := strings.TrimSuffix(path, ":\\")
	letter = strings.TrimSuffix(letter, ":/")
	if len(letter) == 0 {
		return fmt.Errorf("kein Drive Letter in %q", path)
	}

	// Schreibe Caches flushen lassen
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
	// Lock — retry weil andere Prozesse (Indexer) ggf. noch zugreifen
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

	// Dismount Filesystem
	if r, _, e := deviceIoControl.Call(h, fsctlDismountVolume, 0, 0, 0, 0, uintptr(unsafe.Pointer(&bytesReturned)), 0); r == 0 {
		return fmt.Errorf("dismount: %v", e)
	}

	// Media Removal aktivieren (PREVENT_MEDIA_REMOVAL = 0 = false)
	var preventRemoval byte = 0
	deviceIoControl.Call(h, ioctlStorageMediaRemov, uintptr(unsafe.Pointer(&preventRemoval)), 1, 0, 0, uintptr(unsafe.Pointer(&bytesReturned)), 0)

	// Eject — der eigentliche "Safe to Remove" Hardware Trigger
	if r, _, e := deviceIoControl.Call(h, ioctlStorageEjectMedia, 0, 0, 0, 0, uintptr(unsafe.Pointer(&bytesReturned)), 0); r == 0 {
		return fmt.Errorf("eject media: %v", e)
	}
	return nil
}

var _ = filepath.Join
