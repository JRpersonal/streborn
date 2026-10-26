//go:build !windows

package sticksetup

import "syscall"

// clusterBytes returns the volume's block size, which for a FAT32 mount is its
// cluster size, or 0 when statfs fails.
func clusterBytes(path string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0
	}
	return int64(st.Bsize)
}
