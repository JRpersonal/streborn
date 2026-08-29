//go:build !darwin

package sticksetup

// mountReadOnly: the read-only-mount detection is macOS-specific (dirty FAT32
// volumes mount read-only there, see readonly_darwin.go). Windows exposes a
// write-protected stick through the write probe itself, so there is nothing
// extra to detect.
func mountReadOnly(string) bool { return false }
