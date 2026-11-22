package usbmedia

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// fatImage builds the start of a FAT volume: the boot sector with bootLabel in
// its label field and, at the root directory, the given 32-byte entries.
func fatImage(kind, bootLabel string, entries ...[]byte) []byte {
	b := bootSector(kind, 512, 1)
	label := []byte(bootLabel + "           ")[:11]
	var root int64
	if kind == "fat32" {
		binary.LittleEndian.PutUint16(b[14:], 32) // reserved sectors
		b[16] = 2                                 // FATs
		binary.LittleEndian.PutUint32(b[36:], 4)  // sectors per FAT
		binary.LittleEndian.PutUint32(b[44:], 2)  // root cluster
		copy(b[71:], label)
		root = (32 + 2*4) * 512
	} else {
		binary.LittleEndian.PutUint16(b[14:], 1)   // reserved sectors
		b[16] = 2                                  // FATs
		binary.LittleEndian.PutUint16(b[17:], 512) // root entries
		binary.LittleEndian.PutUint16(b[22:], 4)   // sectors per FAT
		copy(b[43:], label)
		root = (1 + 2*4) * 512
	}
	img := make([]byte, root+512)
	copy(img, b)
	for i, e := range entries {
		copy(img[root+int64(i)*32:], e)
	}
	return img
}

// dirEntry is a 32-byte directory entry with an 11-byte name and attributes.
func dirEntry(name string, attr byte) []byte {
	e := make([]byte, 32)
	copy(e, []byte(name + "           ")[:11])
	e[11] = attr
	return e
}

func TestReadVolumeLabelTakesTheRootDirectoryEntry(t *testing.T) {
	deleted := dirEntry("XOLD", 0x08)
	deleted[0] = 0xE5
	cases := []struct {
		name string
		img  []byte
		want string
	}{
		{"FAT32 renamed on Windows: the boot sector still says NO NAME",
			fatImage("fat32", "NO NAME", dirEntry("MUSIK", 0x08)), "MUSIK"},
		{"FAT32 as STR formats it", fatImage("fat32", "REBORN", dirEntry("REBORN", 0x08)), "REBORN"},
		{"a deleted label, a long name and a folder come first",
			fatImage("fat32", "NO NAME", deleted, dirEntry("Aalbum", 0x0F), dirEntry("ALBUM", 0x10), dirEntry("PARTY 2024", 0x08|0x20)),
			"PARTY 2024"},
		{"an umlaut in code page 437", fatImage("fat32", "NO NAME", dirEntry("\x8eRGER \xe1", 0x08)), "ÄRGER ß"},
		{"no label entry: no label, whatever the boot sector says",
			fatImage("fat32", "OLDNAME", dirEntry("SONG    MP3", 0x20)), ""},
		{"FAT16", fatImage("fat16", "NO NAME", dirEntry("KLEIN", 0x08)), "KLEIN"},
		{"a root directory past the end: the boot sector label",
			fatImage("fat32", "BOOTLBL")[:512], "BOOTLBL"},
		{"a boot sector that says NO NAME and nothing else", fatImage("fat32", "NO NAME")[:512], ""},
		{"exFAT", bootSector("exfat", 512, 0), ""},
		{"no boot sector", make([]byte, 4096), ""},
	}
	for _, c := range cases {
		if got := ReadVolumeLabel(bytes.NewReader(c.img)); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestCodePage437TableIsComplete(t *testing.T) {
	if len(cp437High) != 128 {
		t.Fatalf("cp437High has %d runes, want 128", len(cp437High))
	}
	for b, want := range map[byte]rune{0x80: 'Ç', 0x81: 'ü', 0x84: 'ä', 0x8E: 'Ä', 0x94: 'ö', 0x99: 'Ö', 0x9A: 'Ü', 0xA5: 'Ñ', 0xE1: 'ß', 0xFF: ' '} {
		if got := cp437High[b-0x80]; got != want {
			t.Errorf("0x%X: got %q, want %q", b, got, want)
		}
	}
}
