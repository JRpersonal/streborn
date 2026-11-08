//go:build !windows

package sticksetup

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// partitionStyle asks the operating system how the disk behind the mount at
// path is partitioned: diskutil on macOS, findmnt and lsblk on Linux. Best
// effort, read-only.
func partitionStyle(path string) (string, int, error) {
	if runtime.GOOS == "darwin" {
		return macPartitionStyle(path)
	}
	return linuxPartitionStyle(path)
}

// macPartitionStyle reads the volume's device and its whole disk from
// diskutil. A volume that is its own whole disk has no partition table.
func macPartitionStyle(volumePath string) (string, int, error) {
	out, err := exec.Command("diskutil", "info", "-plist", volumePath).CombinedOutput()
	if err != nil {
		return "", 0, fmt.Errorf("diskutil info %s: %w", volumePath, err)
	}
	dev := plistString(string(out), "DeviceIdentifier")
	whole := plistString(string(out), "ParentWholeDisk")
	if dev == "" || whole == "" {
		return "", 0, fmt.Errorf("diskutil info %s names no device", volumePath)
	}
	if dev == whole {
		return "none", 0, nil
	}
	wout, err := exec.Command("diskutil", "info", "-plist", whole).CombinedOutput()
	if err != nil {
		return "", 0, fmt.Errorf("diskutil info %s: %w", whole, err)
	}
	style := partitionSchemeStyle(plistString(string(wout), "Content"))
	parts := -1
	if lout, lerr := exec.Command("diskutil", "list", whole).CombinedOutput(); lerr == nil {
		parts = strings.Count(string(lout), whole+"s")
	}
	return style, parts, nil
}

// linuxPartitionStyle reads the partition table type of the device behind the
// mount. A file system on the whole disk (sdb, not sdb1) has no table.
func linuxPartitionStyle(path string) (string, int, error) {
	dev, err := linuxBackingDevice(path)
	if err != nil {
		return "", 0, err
	}
	base := filepath.Base(dev)
	parent := diskParent(base)
	if parent == base {
		return "none", 0, nil
	}
	out, err := exec.Command("lsblk", "-dno", "PTTYPE", "/dev/"+parent).CombinedOutput()
	if err != nil {
		return "", 0, fmt.Errorf("lsblk %s: %w", parent, err)
	}
	style := partitionSchemeStyle(strings.TrimSpace(string(out)))
	parts := -1
	if lout, lerr := exec.Command("lsblk", "-nro", "TYPE", "/dev/"+parent).CombinedOutput(); lerr == nil {
		parts = strings.Count(string(lout), "part")
	}
	return style, parts, nil
}
