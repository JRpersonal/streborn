//go:build !windows

package sticksetup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

func listDrivesWindows() ([]Drive, error) { return nil, fmt.Errorf("not on Windows") }

func listDrivesMac() ([]Drive, error) {
	root := "/Volumes"
	return scanMounts(root)
}

func listDrivesLinux() ([]Drive, error) {
	// Try typical mount points
	candidates := []string{"/media", "/mnt", "/run/media"}
	var all []Drive
	for _, c := range candidates {
		drives, _ := scanMounts(c)
		all = append(all, drives...)
	}
	return all, nil
}

func scanMounts(root string) ([]Drive, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []Drive
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(root, e.Name())
		// Subdirectory at /media/<user>/<volume>
		if runtime.GOOS == "linux" {
			subs, err := os.ReadDir(path)
			if err == nil {
				for _, s := range subs {
					if s.IsDir() {
						out = append(out, makeDrive(filepath.Join(path, s.Name())))
					}
				}
				continue
			}
		}
		out = append(out, makeDrive(path))
	}
	return out, nil
}

func makeDrive(path string) Drive {
	var stat syscall.Statfs_t
	total, free := int64(0), int64(0)
	if err := syscall.Statfs(path, &stat); err == nil {
		total = int64(stat.Blocks) * int64(stat.Bsize)
		free = int64(stat.Bfree) * int64(stat.Bsize)
	}
	label := filepath.Base(path)
	gb := float64(total) / (1024 * 1024 * 1024)
	return Drive{
		Path:        path,
		Label:       label,
		TotalBytes:  total,
		FreeBytes:   free,
		Filesystem:  detectFs(path),
		Removable:   strings.Contains(path, "Volumes") || strings.Contains(path, "media") || strings.Contains(path, "mnt"),
		HasStick:    IsBoseStick(path),
		Description: fmt.Sprintf("%s (%.1f GB)", label, gb),
	}
}

// detectFs reports the on-disk filesystem for a mounted stick path. On Linux it
// asks findmnt, so a stick that is NOT FAT32 (Fedora and other distros default
// larger USB sticks to exFAT) is correctly flagged as "needs format" instead of
// being silently accepted, written to, and then rejected by the speaker (Helmut,
// Fedora 44: a stick STR "prepared" was never read by the box). On platforms where
// we cannot cheaply detect it, keep the historical FAT32 assumption so nothing
// regresses.
func detectFs(path string) string {
	if runtime.GOOS != "linux" {
		return "FAT32"
	}
	out, err := exec.Command("findmnt", "-n", "-o", "FSTYPE", "--target", path).CombinedOutput()
	if err != nil {
		return "FAT32" // findmnt missing or path not a mount: assume FAT32, no regression
	}
	switch fs := strings.TrimSpace(string(out)); strings.ToLower(fs) {
	case "", "vfat", "msdos", "fat", "fat32":
		return "FAT32" // the Bose-readable FAT family (findmnt cannot split FAT16/32)
	default:
		return fs // exfat / ntfs / ext4 / ... -> caller flags it as not-FAT32
	}
}

// formatFAT32Impl stub for Mac/Linux — not implemented yet.
// Users on these platforms format the stick themselves for now.
//
// macOS note (issue #58): `diskutil eraseDisk` requires a whole-disk
// node (`/dev/diskN`), not a mounted volume path (`/Volumes/BOSE`).
// Passing the volume produced
//
//	"A volume was specified instead of a whole disk: /Volumes/BOSE"
//
// We therefore resolve the volume to its ParentWholeDisk via
// `diskutil info -plist <path>` before calling eraseDisk.
func formatFAT32Impl(path, label string) error {
	switch runtime.GOOS {
	case "darwin":
		whole, err := macParentWholeDisk(path)
		if err != nil {
			return fmt.Errorf("resolve whole disk for %q: %w", path, err)
		}
		// Sanity check: never call eraseDisk on disk0 (boot drive).
		// Even if diskutil would refuse, we'd rather error early than
		// rely on diskutil's safety net.
		if whole == "/dev/disk0" {
			return fmt.Errorf("refusing to format internal boot disk %s", whole)
		}
		cmd := exec.Command("diskutil", "eraseDisk", "MS-DOS", label, "MBRFormat", whole)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("diskutil eraseDisk %s: %v: %s", whole, err, string(out))
		}
		return nil
	case "linux":
		return linuxFormatFAT32(path, label)
	default:
		return fmt.Errorf("format on this platform is not implemented yet; please format with system tools (e.g. mkfs.vfat -F 32)")
	}
}

// linuxFormatFAT32 reformats the stick behind a mounted path as FAT32 via
// mkfs.vfat. It reformats the EXISTING backing partition (keeps the partition
// table, swaps the filesystem), which is the minimal change that turns an exFAT /
// ext4 Fedora stick into the FAT32 the speaker reads. Guarded hard so it can only
// ever touch a removable USB/SD stick, never the system disk.
//
// UNVERIFIED on the full range of speakers/sticks yet (implemented for Helmut's
// Fedora report; validate on hardware before relying on it). If a speaker still
// rejects a stick whose FS is now FAT32, the next step is repartitioning to a
// single MBR FAT32 partition, not just reformatting the filesystem.
func linuxFormatFAT32(path, label string) error {
	dev, err := linuxBackingDevice(path)
	if err != nil {
		return err
	}
	if err := linuxAssertRemovableStick(dev); err != nil {
		return err
	}
	// mkfs refuses a mounted target; unmount the path and the device (best-effort).
	_ = exec.Command("umount", path).Run()
	_ = exec.Command("umount", dev).Run()
	// mkfs.vfat -F 32 auto-selects a cluster size valid for the volume size,
	// including small sticks (unlike our Windows helper's fixed default). -n sets
	// the FAT label. Requires dosfstools.
	out, err := exec.Command("mkfs.vfat", "-F", "32", "-n", sanitizeFatLabel(label), dev).CombinedOutput()
	if err != nil {
		if _, lookErr := exec.LookPath("mkfs.vfat"); lookErr != nil {
			return fmt.Errorf("mkfs.vfat is not installed. Install dosfstools (Fedora: sudo dnf install dosfstools; Debian/Ubuntu: sudo apt install dosfstools), then try again")
		}
		return fmt.Errorf("mkfs.vfat %s: %v: %s", dev, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// linuxBackingDevice resolves a mounted path to the block device that backs it,
// e.g. /run/media/user/STICK -> /dev/sdb1.
func linuxBackingDevice(path string) (string, error) {
	out, err := exec.Command("findmnt", "-n", "-o", "SOURCE", "--target", path).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("could not resolve the stick's device via findmnt: %v", err)
	}
	dev := strings.TrimSpace(string(out))
	if !strings.HasPrefix(dev, "/dev/") {
		return "", fmt.Errorf("unexpected backing device %q for %q", dev, path)
	}
	return dev, nil
}

// linuxAssertRemovableStick refuses to format anything that is not clearly a
// removable USB/SD stick, and never the disk that holds the running system. This
// is the safety net for a tool that runs mkfs on a device path.
func linuxAssertRemovableStick(dev string) error {
	parent := diskParent(filepath.Base(dev))
	if parent == "" {
		return fmt.Errorf("could not determine the whole disk for %s", dev)
	}
	// Never the system disk.
	if rootDev, rerr := linuxBackingDevice("/"); rerr == nil {
		if diskParent(filepath.Base(rootDev)) == parent {
			return fmt.Errorf("refusing to format %s: that is the system disk", dev)
		}
	}
	rem, err := os.ReadFile("/sys/block/" + parent + "/removable")
	if err != nil || strings.TrimSpace(string(rem)) != "1" {
		return fmt.Errorf("refusing to format %s: it is not a removable USB stick. "+
			"If it really is your stick, format it yourself with: sudo mkfs.vfat -F 32 %s", dev, dev)
	}
	return nil
}

// diskParent strips a partition suffix to get the whole-disk name:
// sdb1 -> sdb, mmcblk0p1 -> mmcblk0, nvme0n1p3 -> nvme0n1.
func diskParent(base string) string {
	if strings.HasPrefix(base, "mmcblk") || strings.HasPrefix(base, "nvme") {
		if i := strings.LastIndexByte(base, 'p'); i > 0 && allDigits(base[i+1:]) {
			return base[:i]
		}
		return base
	}
	j := len(base)
	for j > 0 && base[j-1] >= '0' && base[j-1] <= '9' {
		j--
	}
	return base[:j]
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// sanitizeFatLabel makes a FAT volume label: uppercase ASCII, no reserved
// characters, at most 11 bytes. mkfs.vfat rejects a label that is too long.
func sanitizeFatLabel(label string) string {
	if label == "" {
		return "REBORN"
	}
	var b strings.Builder
	for _, r := range strings.ToUpper(label) {
		if r > 127 {
			continue
		}
		if strings.ContainsRune(`"*/:<>?\|.`, r) {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= 11 {
			break
		}
	}
	if b.Len() == 0 {
		return "REBORN"
	}
	return b.String()
}

// macParentWholeDisk takes a Volume path like "/Volumes/BOSE" and
// returns the whole-disk device node like "/dev/disk4". Implemented
// by parsing `diskutil info -plist <path>` for the ParentWholeDisk
// key. We use a regex against the plist text rather than pulling in
// plist parsing — the format is stable across macOS versions and the
// dependency surface stays small.
func macParentWholeDisk(volumePath string) (string, error) {
	cmd := exec.Command("diskutil", "info", "-plist", volumePath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("diskutil info %s: %v: %s", volumePath, err, strings.TrimSpace(string(out)))
	}
	// Look for: <key>ParentWholeDisk</key>\n\t<string>disk4</string>
	idx := strings.Index(string(out), "<key>ParentWholeDisk</key>")
	if idx < 0 {
		return "", fmt.Errorf("ParentWholeDisk not present in diskutil info plist for %s", volumePath)
	}
	tail := string(out)[idx:]
	openIdx := strings.Index(tail, "<string>")
	closeIdx := strings.Index(tail, "</string>")
	if openIdx < 0 || closeIdx < 0 || closeIdx <= openIdx {
		return "", fmt.Errorf("ParentWholeDisk value not parseable in diskutil info plist")
	}
	disk := strings.TrimSpace(tail[openIdx+len("<string>") : closeIdx])
	if disk == "" {
		return "", fmt.Errorf("ParentWholeDisk empty in diskutil info plist")
	}
	// disk is returned as "disk4" (no /dev prefix) — eraseDisk
	// accepts both forms but the canonical one in scripts is
	// /dev/disk4 so we normalize.
	if !strings.HasPrefix(disk, "/dev/") {
		disk = "/dev/" + disk
	}
	return disk, nil
}

// ejectImpl on Mac/Linux via OS commands.
func ejectImpl(path string) error {
	switch runtime.GOOS {
	case "darwin":
		cmd := exec.Command("diskutil", "eject", path)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("diskutil eject: %v: %s", err, string(out))
		}
		return nil
	default:
		// Linux: just umount, that is mostly what the user wants
		cmd := exec.Command("umount", path)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("umount: %v: %s", err, string(out))
		}
		return nil
	}
}

var _ = exec.Command
