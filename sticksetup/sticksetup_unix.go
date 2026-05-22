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

func listDrivesWindows() ([]Drive, error) { return nil, fmt.Errorf("nicht auf Windows") }

func listDrivesMac() ([]Drive, error) {
	root := "/Volumes"
	return scanMounts(root)
}

func listDrivesLinux() ([]Drive, error) {
	// Versuche typische Mount Punkte
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
		// Sub Verzeichnis bei /media/<user>/<volume>
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

// detectFs ist Best Effort — Stat liefert filesystem type nur auf Linux.
func detectFs(path string) string {
	return "FAT32" // Assumption fuer Stick targets
}

// formatFAT32Impl stub fuer Mac/Linux — aktuell nicht implementiert.
// User auf diesen Plattformen formatiert vorerst selbst.
//
// macOS note (issue #58): `diskutil eraseDisk` requires a whole-disk
// node (`/dev/diskN`), not a mounted volume path (`/Volumes/BOSE`).
// Passing the volume produced
//   "A volume was specified instead of a whole disk: /Volumes/BOSE"
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
	default:
		return fmt.Errorf("format on this platform is not implemented yet; please format with system tools (e.g. mkfs.vfat)")
	}
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

// ejectImpl auf Mac/Linux via OS Commands.
func ejectImpl(path string) error {
	switch runtime.GOOS {
	case "darwin":
		cmd := exec.Command("diskutil", "eject", path)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("diskutil eject: %v: %s", err, string(out))
		}
		return nil
	default:
		// Linux: einfach umount, das ist meistens was der User will
		cmd := exec.Command("umount", path)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("umount: %v: %s", err, string(out))
		}
		return nil
	}
}

var _ = exec.Command
