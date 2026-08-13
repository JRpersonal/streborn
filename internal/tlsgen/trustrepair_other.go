//go:build !linux

package tlsgen

import "errors"

// platformMountOps has no mounts off-Linux. The agent only ever runs on the
// speaker's ARM Linux; these stubs exist so the package builds and its tests
// run on a dev host. isMountPoint answering false makes the repair report
// "not overlaid" and change nothing, which is the same conservative answer
// the Linux build gives when /proc/mounts cannot be read.
func platformMountOps() mountOps {
	return mountOps{
		isMountPoint: func(string) bool { return false },
		unmount:      func(string) error { return errors.New("unmount: not supported on this platform") },
		bindMount:    func(string, string) error { return errors.New("bind mount: not supported on this platform") },
	}
}
