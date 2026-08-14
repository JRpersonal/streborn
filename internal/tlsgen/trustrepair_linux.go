//go:build linux

package tlsgen

import (
	"os"
	"strings"
	"syscall"
)

// platformMountOps are the real mount operations on the speaker.
func platformMountOps() mountOps {
	return mountOps{
		isMountPoint: isMountPoint,
		unmount:      unmountOverlay,
		bindMount:    bindMountFile,
	}
}

// isMountPoint reports whether something is mounted exactly at path. The
// mount point is the SECOND field of /proc/mounts and has to match exactly:
// a prefix match would confuse the trust store with a neighbouring file.
// An unreadable /proc/mounts answers false, which routes the repair into
// "not overlaid" and makes it change nothing.
func isMountPoint(path string) bool {
	b, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && unescapeMountField(fields[1]) == path {
			return true
		}
	}
	return false
}

// unescapeMountField undoes the octal escaping /proc/mounts applies to
// spaces, tabs, newlines and backslashes. The trust store paths contain none
// of those, but a field left escaped would silently never match.
func unescapeMountField(f string) string {
	if !strings.Contains(f, `\`) {
		return f
	}
	var b strings.Builder
	for i := 0; i < len(f); i++ {
		if f[i] == '\\' && i+3 < len(f) {
			var v int
			ok := true
			for _, c := range []byte(f[i+1 : i+4]) {
				if c < '0' || c > '7' {
					ok = false
					break
				}
				v = v*8 + int(c-'0')
			}
			if ok {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(f[i])
	}
	return b.String()
}

// unmountOverlay takes the overlay off path. Bose's libcurl and openssl open
// the bundle per handshake rather than holding it, so a plain unmount is
// normally enough; a lazy detach covers the moment one of them happens to be
// mid-read. MNT_DETACH leaves already-open descriptors on the old file and
// points every new open at what is underneath, which is exactly what the
// rebuild needs.
func unmountOverlay(path string) error {
	err := syscall.Unmount(path, 0)
	if err == nil {
		return nil
	}
	if lazyErr := syscall.Unmount(path, syscall.MNT_DETACH); lazyErr == nil {
		return nil
	}
	return err
}

func bindMountFile(src, dst string) error {
	return syscall.Mount(src, dst, "", syscall.MS_BIND, "")
}
