//go:build windows

package sticksetup

import (
	"syscall"
	"unsafe"
)

// clusterBytes returns the volume's allocation unit (cluster) size, or 0 when
// Windows does not say. GetDiskFreeSpaceW reports sectors per cluster and
// bytes per sector for the volume root.
func clusterBytes(path string) int64 {
	root := path
	if len(root) >= 2 && root[1] == ':' {
		root = root[:2] + "\\"
	}
	p, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return 0
	}
	var spc, bps, free, total uint32
	r, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceW").Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&spc)),
		uintptr(unsafe.Pointer(&bps)),
		uintptr(unsafe.Pointer(&free)),
		uintptr(unsafe.Pointer(&total)),
	)
	if r == 0 {
		return 0
	}
	return int64(spc) * int64(bps)
}
