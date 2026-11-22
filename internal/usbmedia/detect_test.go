package usbmedia

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPartitionsListsEachUSBDiskOnce(t *testing.T) {
	sys := t.TempDir()
	for _, d := range []string{"block/sda/sda1", "block/sda/sda2", "block/sdb", "block/mtdblock0"} {
		if err := os.MkdirAll(filepath.Join(sys, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// sysfs also has non-partition entries inside a disk directory.
	if err := os.MkdirAll(filepath.Join(sys, "block", "sda", "queue"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := Partitions(sys)
	want := []string{"sda1", "sda2", "sdb"} // sdb: a stick without a partition table
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := Partitions(t.TempDir()); len(got) != 0 {
		t.Fatalf("no stick: got %v", got)
	}
}

func TestParseMountsFindsTheStickAndUnescapes(t *testing.T) {
	mounts := ParseMounts("rootfs / rootfs rw 0 0\n" +
		"/dev/sda1 /media/sda1 vfat rw,relatime,fmask=0022,codepage=cp437,iocharset=utf8,errors=remount-ro 0 0\n" +
		"/dev/sdb1 /tmp/str\\040music vfat ro,nosuid,nodev,noexec 0 0\n")
	m, ok := MountOf(mounts, "sda1")
	if !ok || m.Dir != "/media/sda1" || m.FSType != "vfat" || m.ReadOnly() {
		t.Fatalf("sda1: %+v %v", m, ok)
	}
	m, ok = MountOf(mounts, "sdb1")
	if !ok || m.Dir != "/tmp/str music" || !m.ReadOnly() {
		t.Fatalf("sdb1: %+v %v", m, ok)
	}
	if _, ok := MountOf(mounts, "sdc1"); ok {
		t.Fatal("an unmounted device must not be found")
	}
}

// bootSector builds a minimal boot sector of the given kind.
func bootSector(kind string, bytesPerSector uint16, sectorsPerCluster byte) []byte {
	b := make([]byte, 512)
	switch kind {
	case "exfat":
		copy(b[3:], "EXFAT   ")
	case "ntfs":
		copy(b[3:], "NTFS    ")
	case "fat32":
		copy(b[3:], "MSWIN4.1")
		copy(b[82:], "FAT32   ")
	case "fat16":
		copy(b[3:], "MSDOS5.0")
		copy(b[54:], "FAT16   ")
	}
	binary.LittleEndian.PutUint16(b[11:], bytesPerSector)
	b[13] = sectorsPerCluster
	b[510], b[511] = 0x55, 0xAA
	return b
}

func TestSniffNamesTheFilesystemAndTheClusterSize(t *testing.T) {
	cases := []struct {
		name    string
		sector  []byte
		fs      string
		fatType string
		cluster int
	}{
		{"FAT32 as STR formats it", bootSector("fat32", 512, 64), FSFAT, "FAT32", 32 << 10},
		{"FAT32 over 32 GB from another tool", bootSector("fat32", 512, 128), FSFAT, "FAT32", 64 << 10},
		{"a small FAT16 stick", bootSector("fat16", 512, 8), FSFAT, "FAT16", 4 << 10},
		{"exFAT, what Windows makes of a 64 GB stick", bootSector("exfat", 512, 0), FSExFAT, "", 0},
		{"NTFS", bootSector("ntfs", 512, 8), FSNTFS, "", 0},
		{"no boot sector at all", make([]byte, 512), FSUnknown, "", 0},
	}
	for _, c := range cases {
		v, err := Sniff(bytes.NewReader(c.sector))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if v.FS != c.fs || v.FATType != c.fatType || v.ClusterBytes != c.cluster {
			t.Errorf("%s: got %+v", c.name, v)
		}
	}
	if v, err := Sniff(bytes.NewReader([]byte{1, 2, 3})); err == nil || v.FS != FSUnknown {
		t.Errorf("a short device must fail: %+v %v", v, err)
	}
}
