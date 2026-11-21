package usbmedia

import (
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Detection, read-only. Nothing here mounts, writes or runs a command: it reads
// sysfs (is a USB disk there, which partitions does it have), /proc/mounts (is
// one mounted, where, as what) and the first sector of a partition (what
// filesystem it carries when nothing mounted it). All of it is a few small
// reads, so the agent can afford it whenever a user opens the Library, and it
// never needs a timer.

// Partitions lists the block devices of the USB disks under sysRoot/block, by
// device name ("sda1"), in order. A disk without a partition table (a
// "superfloppy", which some sticks ship as) is listed by its own name ("sda").
func Partitions(sysRoot string) []string {
	disks, _ := filepath.Glob(filepath.Join(sysRoot, "block", "sd*"))
	sort.Strings(disks)
	var out []string
	for _, d := range disks {
		disk := filepath.Base(d)
		parts, _ := filepath.Glob(filepath.Join(d, disk+"*"))
		var names []string
		for _, p := range parts {
			name := filepath.Base(p)
			if name != disk && digitRun(strings.TrimPrefix(name, disk)) > 0 {
				names = append(names, name)
			}
		}
		if len(names) == 0 {
			out = append(out, disk)
			continue
		}
		sort.Strings(names)
		out = append(out, names...)
	}
	return out
}

// Mount is one /proc/mounts line.
type Mount struct {
	Device  string // "/dev/sda1"
	Dir     string // "/media/sda1"
	FSType  string // "vfat"
	Options string // "rw,relatime,..."
}

// ReadOnly reports whether the mount is read-only.
func (m Mount) ReadOnly() bool {
	for _, o := range strings.Split(m.Options, ",") {
		if o == "ro" {
			return true
		}
	}
	return false
}

// ParseMounts reads /proc/mounts content. The kernel escapes a space in a
// path as \040; that is undone so a mount dir compares as a path.
func ParseMounts(content string) []Mount {
	var out []Mount
	for _, line := range strings.Split(content, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		out = append(out, Mount{Device: unescapeMount(f[0]), Dir: unescapeMount(f[1]), FSType: f[2], Options: f[3]})
	}
	return out
}

func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return r.Replace(s)
}

// MountOf returns the mount of device name ("sda1"), the first one listed.
func MountOf(mounts []Mount, name string) (Mount, bool) {
	for _, m := range mounts {
		if m.Device == "/dev/"+name {
			return m, true
		}
	}
	return Mount{}, false
}

// Filesystem kinds as Sniff names them.
const (
	FSFAT     = "vfat"
	FSExFAT   = "exfat"
	FSNTFS    = "ntfs"
	FSUnknown = "unknown"
)

// FATMountTypes are the /proc/mounts types a FAT stick can be mounted as.
var FATMountTypes = map[string]bool{"vfat": true, "msdos": true, "fat": true}

// Volume is what the first sector of a partition says about it.
type Volume struct {
	// FS is FSFAT, FSExFAT, FSNTFS or FSUnknown.
	FS string
	// FATType is "FAT12", "FAT16" or "FAT32" for a FAT volume.
	FATType string
	// ClusterBytes is the FAT cluster size, 0 when not FAT.
	ClusterBytes int
}

// MaxClusterBytes is the largest FAT cluster the speaker's kernel reads data
// from. A FAT32 stick with 64 KB clusters lists its folders and then fails
// every file read with an I/O error (#119); STR's own formatter caps clusters
// at 32 KB for that reason.
const MaxClusterBytes = 32 << 10

// Sniff reads the boot sector at the start of r and names the filesystem. It
// reads 512 bytes and nothing else.
func Sniff(r io.ReaderAt) (Volume, error) {
	b := make([]byte, 512)
	if _, err := r.ReadAt(b, 0); err != nil {
		return Volume{FS: FSUnknown}, err
	}
	return sniffSector(b), nil
}

func sniffSector(b []byte) Volume {
	if len(b) < 512 {
		return Volume{FS: FSUnknown}
	}
	switch string(b[3:11]) {
	case "EXFAT   ":
		return Volume{FS: FSExFAT}
	case "NTFS    ":
		return Volume{FS: FSNTFS}
	}
	if b[510] != 0x55 || b[511] != 0xAA {
		return Volume{FS: FSUnknown}
	}
	fatType := ""
	switch {
	case strings.HasPrefix(string(b[82:90]), "FAT32"):
		fatType = "FAT32"
	case strings.HasPrefix(string(b[54:62]), "FAT16"):
		fatType = "FAT16"
	case strings.HasPrefix(string(b[54:62]), "FAT12"):
		fatType = "FAT12"
	default:
		return Volume{FS: FSUnknown}
	}
	bytesPerSector := int(binary.LittleEndian.Uint16(b[11:13]))
	sectorsPerCluster := int(b[13])
	return Volume{FS: FSFAT, FATType: fatType, ClusterBytes: bytesPerSector * sectorsPerCluster}
}

// SniffDevice opens the device node and sniffs it, read-only.
func SniffDevice(devPath string) (Volume, error) {
	f, err := os.Open(devPath)
	if err != nil {
		return Volume{FS: FSUnknown}, err
	}
	defer func() { _ = f.Close() }()
	return Sniff(f)
}
