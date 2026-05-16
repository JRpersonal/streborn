// winformat ist ein kleines Helper Programm das Windows Volumes
// nativ als FAT32 quick formatiert — ohne FmIfs.dll, ohne 32 GB Limit,
// ohne dass Microsoft uns reinredet.
//
// Vorgehen:
//   1. Volume \\.\X: oeffnen, locken, dismounten (NTFS Treiber aus)
//   2. FAT32 Boot Sector + FSInfo + FAT Tabellen + Root Dir direkt
//      auf das gelockte Handle schreiben (siehe fat32.go)
//   3. Handle schliessen — Windows mountet automatisch als FAT32
//
// Wird vom Desktop App Setup Wizard via Start-Process -Verb RunAs
// elevated gestartet (einmaliger UAC Prompt). Exit Codes:
//   0  Erfolg
//   1  Argumente fehlerhaft
//   2  Format fehlgeschlagen (Details im Status File)
//
//go:build windows

package main

import (
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"
)

// writeStatus schreibt Status in eine Datei. So koennen wir die echte
// Erfolgs/Fehlermeldung ueber die UAC Elevation Boundary hinweg
// zurueckliefern (Start-Process -Verb RunAs erlaubt KEIN
// -RedirectStandardError, die App liest stattdessen diese Datei).
func writeStatus(path, status string) {
	if path == "" {
		return
	}
	_ = os.WriteFile(path, []byte(status), 0o644)
}

// volumeTotalBytes liefert die Volume Groesse in Bytes via
// GetDiskFreeSpaceEx. 0 bei Fehler (passiert wenn Volume kein
// erkanntes Filesystem hat — dann nutze volumeLengthFromHandle).
func volumeTotalBytes(rootPtr *uint16) uint64 {
	kernel := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel.NewProc("GetDiskFreeSpaceExW")
	var free, total, totalFree uint64
	r, _, _ := proc.Call(
		uintptr(unsafe.Pointer(rootPtr)),
		uintptr(unsafe.Pointer(&free)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if r == 0 {
		return 0
	}
	return total
}

// volumeLengthFromHandle liefert die Groesse via
// IOCTL_DISK_GET_LENGTH_INFO. Funktioniert auch auf raw / dismounted
// Volume Handles, anders als GetDiskFreeSpaceEx.
func volumeLengthFromHandle(h uintptr) (uint64, error) {
	const ioctlDiskGetLengthInfo = 0x0007405C
	k32 := syscall.NewLazyDLL("kernel32.dll")
	deviceIoControl := k32.NewProc("DeviceIoControl")
	var length int64
	var bytesReturned uint32
	r, _, e := deviceIoControl.Call(
		h,
		uintptr(ioctlDiskGetLengthInfo),
		0, 0,
		uintptr(unsafe.Pointer(&length)),
		8,
		uintptr(unsafe.Pointer(&bytesReturned)),
		0,
	)
	if r == 0 {
		return 0, fmt.Errorf("IOCTL_DISK_GET_LENGTH_INFO: %v", e)
	}
	if length <= 0 {
		return 0, fmt.Errorf("IOCTL_DISK_GET_LENGTH_INFO lieferte %d", length)
	}
	return uint64(length), nil
}

// openLockDismount oeffnet \\.\X: exklusiv, sperrt das Volume,
// dismountet den FS Treiber. Returnt das offene Handle damit der
// Caller direkt weiter darauf schreiben kann (FAT32 Strukturen).
// Caller muss CloseHandle aufrufen.
func openLockDismount(letter string) (uintptr, error) {
	drivePath := `\\.\` + letter + ":"
	const (
		genericRead      = 0x80000000
		genericWrite     = 0x40000000
		fileShareRW      = 0x00000003
		openExisting     = 3
		fsctlLockVolume  = 0x00090018
		fsctlDismountVol = 0x00090020
	)

	k32 := syscall.NewLazyDLL("kernel32.dll")
	createFileW := k32.NewProc("CreateFileW")
	deviceIoControl := k32.NewProc("DeviceIoControl")
	closeHandle := k32.NewProc("CloseHandle")

	utf16, err := syscall.UTF16PtrFromString(drivePath)
	if err != nil {
		return 0, err
	}
	h, _, lastWinErr := createFileW.Call(
		uintptr(unsafe.Pointer(utf16)),
		genericRead|genericWrite,
		fileShareRW,
		0, openExisting, 0, 0,
	)
	if h == 0 || h == ^uintptr(0) {
		return 0, fmt.Errorf("CreateFile: %v", lastWinErr)
	}

	var bytesReturned uint32
	locked := false
	for i := 0; i < 20; i++ {
		r, _, _ := deviceIoControl.Call(h, fsctlLockVolume, 0, 0, 0, 0,
			uintptr(unsafe.Pointer(&bytesReturned)), 0)
		if r != 0 {
			locked = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !locked {
		closeHandle.Call(h)
		return 0, fmt.Errorf("lock fehlgeschlagen (Volume wird von anderem Prozess gehalten)")
	}
	if r, _, e := deviceIoControl.Call(h, fsctlDismountVol, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&bytesReturned)), 0); r == 0 {
		closeHandle.Call(h)
		return 0, fmt.Errorf("dismount: %v", e)
	}
	return h, nil
}

// chooseClusterSize gibt die Windows Standard FAT32 Cluster Size fuer
// die gegebene Volume Groesse zurueck. 0 = Standard von FmIfs (klappt
// fuer kleine Volumes, aber bei > 32 GB resultiert das in zu vielen
// Clustern).
func chooseClusterSize(totalBytes uint64) uint32 {
	const GB = uint64(1) << 30
	switch {
	case totalBytes > 32*GB:
		return 32 * 1024 // 32 KB
	case totalBytes > 16*GB:
		return 16 * 1024
	case totalBytes > 8*GB:
		return 8 * 1024
	default:
		return 0 // FmIfs Default
	}
}

func main() {
	// Argumente: <drive_letter> <label> <status_file>
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: winformat <drive_letter> [label] [status_file]")
		os.Exit(1)
	}
	letter := os.Args[1]
	if len(letter) >= 2 && letter[1] == ':' {
		letter = string(letter[0])
	}
	if len(letter) != 1 {
		fmt.Fprintln(os.Stderr, "drive letter must be A..Z")
		os.Exit(1)
	}
	label := "REBORN"
	if len(os.Args) >= 3 && os.Args[2] != "" {
		label = os.Args[2]
	}
	statusFile := ""
	if len(os.Args) >= 4 {
		statusFile = os.Args[3]
	}

	driveRoot := letter + ":\\"
	rootPtr, _ := syscall.UTF16PtrFromString(driveRoot)
	// Erste Schaetzung via Drive Letter — funktioniert nur wenn FS
	// erkannt ist. Bei "raw" Volume liefert das 0, dann holen wir die
	// echte Groesse vom Handle.
	totalBytes := volumeTotalBytes(rootPtr)

	// Volume oeffnen, sperren, dismounten — Handle bleibt offen.
	h, err := openLockDismount(letter)
	if err != nil {
		writeStatus(statusFile, fmt.Sprintf("ERR openLockDismount: %v [totalBytes=%d]",
			err, totalBytes))
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	k32 := syscall.NewLazyDLL("kernel32.dll")
	closeHandle := k32.NewProc("CloseHandle")
	defer closeHandle.Call(h)

	// Fallback: Groesse vom Handle holen (funktioniert auch auf raw).
	if totalBytes == 0 {
		if sz, lerr := volumeLengthFromHandle(h); lerr == nil {
			totalBytes = sz
		} else {
			writeStatus(statusFile, fmt.Sprintf("ERR Volume Groesse: %v", lerr))
			fmt.Fprintln(os.Stderr, lerr)
			os.Exit(2)
		}
	}
	clusterSize := chooseClusterSize(totalBytes)
	if clusterSize == 0 {
		clusterSize = 4096
	}

	if err := fat32QuickFormat(h, totalBytes, clusterSize, label); err != nil {
		writeStatus(statusFile, fmt.Sprintf("ERR fat32 write: %v [totalBytes=%d clusterSize=%d]",
			err, totalBytes, clusterSize))
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	writeStatus(statusFile, fmt.Sprintf("OK [totalBytes=%d clusterSize=%d]", totalBytes, clusterSize))
	fmt.Println("OK")
}
