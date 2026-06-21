// winformat is a small helper program that quick-formats Windows volumes
// natively as FAT32 — without FmIfs.dll, without the 32 GB limit, without
// Microsoft getting in our way.
//
// Procedure:
//   1. Open, lock, dismount the volume \\.\X: (NTFS driver off)
//   2. Write the FAT32 boot sector + FSInfo + FAT tables + root dir directly
//      to the locked handle (see fat32.go)
//   3. Close the handle — Windows mounts it automatically as FAT32
//
// Started elevated by the desktop app setup wizard via Start-Process -Verb
// RunAs (single UAC prompt). Exit codes:
//   0  success
//   1  bad arguments
//   2  format failed (details in the status file)
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

// writeStatus writes the status to a file. This lets us return the real
// success/error message across the UAC elevation boundary (Start-Process
// -Verb RunAs does NOT allow -RedirectStandardError, so the app reads this
// file instead).
func writeStatus(path, status string) {
	if path == "" {
		return
	}
	_ = os.WriteFile(path, []byte(status), 0o644)
}

// volumeTotalBytes returns the volume size in bytes via GetDiskFreeSpaceEx.
// 0 on error (happens when the volume has no recognised filesystem — then
// use volumeLengthFromHandle).
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

// volumeLengthFromHandle returns the size via IOCTL_DISK_GET_LENGTH_INFO.
// Works on raw / dismounted volume handles too, unlike GetDiskFreeSpaceEx.
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
		return 0, fmt.Errorf("IOCTL_DISK_GET_LENGTH_INFO returned %d", length)
	}
	return uint64(length), nil
}

// openLockDismount opens \\.\X: exclusively, locks the volume, dismounts
// the FS driver. Returns the open handle so the caller can write to it
// directly (FAT32 structures). The caller must call CloseHandle.
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
		return 0, fmt.Errorf("lock failed (volume is held by another process)")
	}
	if r, _, e := deviceIoControl.Call(h, fsctlDismountVol, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&bytesReturned)), 0); r == 0 {
		closeHandle.Call(h)
		return 0, fmt.Errorf("dismount: %v", e)
	}
	return h, nil
}

// chooseClusterSize returns the Windows default FAT32 cluster size for the
// given volume size. 0 = FmIfs default (works for small volumes, but for
// > 32 GB results in too many clusters).
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
		return 0 // FmIfs default
	}
}

func main() {
	// Arguments: <drive_letter> <label> <status_file>
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
	// First estimate via the drive letter — only works when the FS is
	// recognised. For a "raw" volume this returns 0, then we get the real
	// size from the handle.
	totalBytes := volumeTotalBytes(rootPtr)

	// Open, lock, dismount the volume — the handle stays open.
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

	// Fallback: get the size from the handle (works on raw too).
	if totalBytes == 0 {
		if sz, lerr := volumeLengthFromHandle(h); lerr == nil {
			totalBytes = sz
		} else {
			writeStatus(statusFile, fmt.Sprintf("ERR volume size: %v", lerr))
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
