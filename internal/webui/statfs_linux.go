//go:build linux

package webui

import "syscall"

// diskFree reports total and available-to-non-root bytes on the filesystem
// backing path. ok is false if the statfs failed.
func diskFree(path string) (total, avail int64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, false
	}
	bs := int64(st.Bsize)
	return int64(st.Blocks) * bs, int64(st.Bavail) * bs, true
}

// sysProcAttrSetsid detaches a helper process into its own session so it
// survives this agent's exit and any process-group teardown (used by the OTA
// self-restart fallback and the RAM-staged swap helper).
func sysProcAttrSetsid() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
