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
func formatFAT32Impl(path, label string) error {
	switch runtime.GOOS {
	case "darwin":
		cmd := exec.Command("diskutil", "eraseDisk", "MS-DOS", label, "MBRFormat", path)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("diskutil format: %v: %s", err, string(out))
		}
		return nil
	default:
		return fmt.Errorf("Format auf dieser Plattform aktuell nicht implementiert. Bitte mit Systemwerkzeugen (mkfs.vfat) formatieren.")
	}
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
