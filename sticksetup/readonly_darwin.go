//go:build darwin

package sticksetup

import "syscall"

// mntRdonly is MNT_RDONLY from <sys/mount.h>; the syscall package on darwin
// does not export the constant, only the Statfs_t.Flags field it lives in.
const mntRdonly = 0x1

// mountReadOnly reports whether the volume at path is mounted read-only.
// macOS mounts a FAT32 volume read-only when its dirty bit is set, which is
// what an unplug without ejecting leaves behind: the stick then LOOKS fine in
// Finder but every write fails, and the wizard could only say "may be
// write-protected or faulty" (#775: two FAT32 sticks refused on a Mac while
// the same sticks as exFAT worked). Best-effort: any statfs error means
// "cannot tell", not "read-only".
func mountReadOnly(path string) bool {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return false
	}
	return st.Flags&mntRdonly != 0
}
