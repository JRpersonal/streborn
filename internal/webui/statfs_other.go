//go:build !linux

package webui

import "syscall"

// diskFree has no statfs on non-Linux hosts. The agent only ever runs on the
// speaker's ARM Linux; this stub exists so the package builds and its tests
// run on dev hosts. ok=false makes every caller treat the free figure as
// unknown and fail OPEN, exactly like a failed statfs on the box.
func diskFree(string) (total, avail int64, ok bool) { return 0, 0, false }

// sysProcAttrSetsid: no session detach off-Linux (the helpers it guards are
// Linux-only paths that never run on a dev host).
func sysProcAttrSetsid() *syscall.SysProcAttr { return nil }
