//go:build linux

package spotify

import "syscall"

// freeBytes returns the bytes available to non-root on the filesystem backing
// dir, ok=false if statfs fails. Mirrors webui.diskFree; kept local so the
// spotify package takes no dependency on webui.
func freeBytes(dir string) (int64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), true
}
