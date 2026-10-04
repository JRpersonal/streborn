//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// exeResourceVersion reads the version Windows shows under Properties >
// Details, from the fixed part of the file's version resource (the block
// cmd/winresgen writes at release time, four numbers, the fourth always 0).
//
// Since 2026-10-04 the published file names carry no version, so this is the
// only place a copy of STR on disk still says which build it is. The fixed
// block is read rather than the localised string table: it needs no language
// lookup and cannot hold anything but numbers.
func exeResourceVersion(path string) (maj, min, patch int, ok bool) {
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil || size == 0 {
		return 0, 0, 0, false
	}
	buf := make([]byte, size)
	if err := windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&buf[0])); err != nil {
		return 0, 0, 0, false
	}
	var fixed *windows.VS_FIXEDFILEINFO
	var fixedLen uint32
	if err := windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\`, unsafe.Pointer(&fixed), &fixedLen); err != nil {
		return 0, 0, 0, false
	}
	if fixed == nil || fixedLen < uint32(unsafe.Sizeof(*fixed)) {
		return 0, 0, 0, false
	}
	maj = int(fixed.FileVersionMS >> 16)
	min = int(fixed.FileVersionMS & 0xffff)
	patch = int(fixed.FileVersionLS >> 16)
	// 0.0.0 is what an unstamped build carries (winresgen's default); it says
	// nothing about which build this is.
	if maj == 0 && min == 0 && patch == 0 {
		return 0, 0, 0, false
	}
	return maj, min, patch, true
}
