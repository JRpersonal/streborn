//go:build linux

package clocksync

import (
	"time"

	"golang.org/x/sys/unix"
)

// setSystemTime sets the wall clock via settimeofday(2). The agent runs as root
// on the speaker, which is required for this call.
func setSystemTime(t time.Time) error {
	tv := unix.NsecToTimeval(t.UnixNano())
	return unix.Settimeofday(&tv)
}
