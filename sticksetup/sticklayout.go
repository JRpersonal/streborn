package sticksetup

import (
	"encoding/binary"
	"strings"
)

// PartitionStyle reads how the stick mounted at path is partitioned, for the
// firmware ladder's log: the Bose USB updater is picky about its stick, and a
// support case (2026-11) could not tell from the log whether the stick's
// layout was the reason a speaker never started the update.
//
// style is "MBR", "GPT", "none" (the volume starts at the first byte of the
// stick, no partition table: a "superfloppy"), "raw" or "unknown"; partitions
// is the number of used entries in the table, -1 when unknown. Best effort: on
// any failure the style is "unknown" and err says why.
func PartitionStyle(path string) (style string, partitions int, err error) {
	style, partitions, err = partitionStyle(path)
	if err != nil {
		return "unknown", -1, err
	}
	return style, partitions, nil
}

// The Windows DRIVE_LAYOUT_INFORMATION_EX, from winioctl.h. Kept here, not in
// the Windows file, so the parser is tested on every platform.
const (
	partitionStyleMBR = 0
	partitionStyleGPT = 1
	partitionStyleRAW = 2

	// DRIVE_LAYOUT_INFORMATION_EX: PartitionStyle (4), PartitionCount (4),
	// the MBR/GPT union (40), then PARTITION_INFORMATION_EX entries of 144
	// bytes each: PartitionStyle (4, padded to 8), StartingOffset (8),
	// PartitionLength (8), PartitionNumber (4), two BOOLEANs, padding, then
	// the MBR/GPT union at offset 32 whose first byte is the MBR type.
	layoutHeaderBytes  = 48
	layoutEntryBytes   = 144
	entryStartOffset   = 8
	entryLengthOffset  = 16
	entryMBRTypeOffset = 32
)

// parseDriveLayout reads a DRIVE_LAYOUT_INFORMATION_EX. An MBR layout lists
// four entries even when fewer are used; an entry with type 0 is empty. A
// stick whose volume starts at byte 0 (Windows lists such a "superfloppy" as
// one MBR entry at offset 0) has no table at all: "none".
func parseDriveLayout(b []byte) (string, int) {
	if len(b) < 8 {
		return "unknown", -1
	}
	style := binary.LittleEndian.Uint32(b[0:4])
	count := int(binary.LittleEndian.Uint32(b[4:8]))
	type entry struct {
		start   int64
		length  int64
		mbrType byte
	}
	var entries []entry
	for i := 0; i < count; i++ {
		off := layoutHeaderBytes + i*layoutEntryBytes
		if off+layoutEntryBytes > len(b) {
			break
		}
		entries = append(entries, entry{
			start:   int64(binary.LittleEndian.Uint64(b[off+entryStartOffset:])),
			length:  int64(binary.LittleEndian.Uint64(b[off+entryLengthOffset:])),
			mbrType: b[off+entryMBRTypeOffset],
		})
	}
	switch style {
	case partitionStyleGPT:
		return "GPT", len(entries)
	case partitionStyleRAW:
		return "raw", 0
	case partitionStyleMBR:
		used := 0
		superfloppy := false
		for _, e := range entries {
			if e.start == 0 && e.length > 0 {
				superfloppy = true
			}
			if e.mbrType != 0 {
				used++
			}
		}
		if superfloppy {
			return "none", 0
		}
		return "MBR", used
	}
	return "unknown", -1
}

// partitionSchemeStyle maps the partition scheme names of diskutil
// ("FDisk_partition_scheme", "GUID_partition_scheme") and lsblk's PTTYPE
// ("dos", "gpt") to the log's words. Anything else is passed through.
func partitionSchemeStyle(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "fdisk_partition_scheme", "dos":
		return "MBR"
	case "guid_partition_scheme", "gpt":
		return "GPT"
	case "":
		return "unknown"
	}
	return strings.TrimSpace(s)
}

// plistString returns the <string> value right after <key>key</key> in a
// diskutil plist, "" when that key is missing or holds no string.
func plistString(plist, key string) string {
	k := "<key>" + key + "</key>"
	idx := strings.Index(plist, k)
	if idx < 0 {
		return ""
	}
	tail := strings.TrimLeft(plist[idx+len(k):], " \t\r\n")
	if !strings.HasPrefix(tail, "<string>") {
		return ""
	}
	tail = tail[len("<string>"):]
	end := strings.Index(tail, "</string>")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(tail[:end])
}
