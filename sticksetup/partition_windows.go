//go:build windows

package sticksetup

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"
)

// IOCTL codes from winioctl.h. Both are FILE_ANY_ACCESS, so the handles are
// opened with no access rights at all: no administrator rights are needed and
// nothing on the stick can change.
const (
	ioctlStorageGetDeviceNumber = 0x002D1080
	ioctlDiskGetDriveLayoutEx   = 0x00070050
)

// partitionStyle asks Windows for the partition table of the disk that holds
// the volume at path (a drive root such as "E:\").
func partitionStyle(path string) (string, int, error) {
	if len(path) < 2 || path[1] != ':' {
		return "", 0, fmt.Errorf("no drive letter in %q", path)
	}
	k32 := syscall.NewLazyDLL("kernel32.dll")
	createFileW := k32.NewProc("CreateFileW")
	deviceIoControl := k32.NewProc("DeviceIoControl")
	closeHandle := k32.NewProc("CloseHandle")

	open := func(name string) (uintptr, error) {
		p, err := syscall.UTF16PtrFromString(name)
		if err != nil {
			return 0, err
		}
		const fileShareRW, openExisting = 0x00000003, 3
		h, _, e := createFileW.Call(uintptr(unsafe.Pointer(p)), 0, fileShareRW, 0, openExisting, 0, 0)
		if h == 0 || h == ^uintptr(0) {
			return 0, fmt.Errorf("open %s: %w", name, e)
		}
		return h, nil
	}

	vol, err := open(`\\.\` + strings.ToUpper(path[:1]) + ":")
	if err != nil {
		return "", 0, err
	}
	// STORAGE_DEVICE_NUMBER: DeviceType, DeviceNumber, PartitionNumber.
	var devNum [3]uint32
	var n uint32
	r, _, e := deviceIoControl.Call(vol, ioctlStorageGetDeviceNumber, 0, 0,
		uintptr(unsafe.Pointer(&devNum[0])), unsafe.Sizeof(devNum), uintptr(unsafe.Pointer(&n)), 0)
	closeHandle.Call(vol)
	if r == 0 {
		return "", 0, fmt.Errorf("device number: %w", e)
	}

	disk, err := open(fmt.Sprintf(`\\.\PhysicalDrive%d`, devNum[1]))
	if err != nil {
		return "", 0, err
	}
	defer closeHandle.Call(disk)
	buf := make([]byte, layoutHeaderBytes+128*layoutEntryBytes)
	r, _, e = deviceIoControl.Call(disk, ioctlDiskGetDriveLayoutEx, 0, 0,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&n)), 0)
	if r == 0 {
		return "", 0, fmt.Errorf("drive layout: %w", e)
	}
	style, parts := parseDriveLayout(buf[:n])
	return style, parts, nil
}
