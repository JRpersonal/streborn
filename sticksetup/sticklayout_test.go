package sticksetup

import (
	"encoding/binary"
	"testing"
)

func layoutBuf(style uint32, entries ...[3]int64) []byte {
	b := make([]byte, layoutHeaderBytes+len(entries)*layoutEntryBytes)
	binary.LittleEndian.PutUint32(b[0:], style)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(entries)))
	for i, e := range entries {
		off := layoutHeaderBytes + i*layoutEntryBytes
		binary.LittleEndian.PutUint64(b[off+entryStartOffset:], uint64(e[0]))
		binary.LittleEndian.PutUint64(b[off+entryLengthOffset:], uint64(e[1]))
		b[off+entryMBRTypeOffset] = byte(e[2])
	}
	return b
}

func TestParseDriveLayout(t *testing.T) {
	cases := []struct {
		name  string
		buf   []byte
		style string
		parts int
	}{
		{"mbr one fat32", layoutBuf(partitionStyleMBR, [3]int64{1 << 20, 8 << 30, 0x0C}, [3]int64{}, [3]int64{}, [3]int64{}), "MBR", 1},
		{"superfloppy", layoutBuf(partitionStyleMBR, [3]int64{0, 8 << 30, 0x0C}), "none", 0},
		{"gpt", layoutBuf(partitionStyleGPT, [3]int64{1 << 20, 8 << 30, 0}), "GPT", 1},
		{"raw", layoutBuf(partitionStyleRAW), "raw", 0},
		{"short", []byte{1, 2}, "unknown", -1},
	}
	for _, c := range cases {
		style, parts := parseDriveLayout(c.buf)
		if style != c.style || parts != c.parts {
			t.Errorf("%s: got %s/%d, want %s/%d", c.name, style, parts, c.style, c.parts)
		}
	}
}

func TestPartitionSchemeAndPlist(t *testing.T) {
	for in, want := range map[string]string{"FDisk_partition_scheme": "MBR", "GUID_partition_scheme": "GPT", "dos": "MBR", "gpt": "GPT", "": "unknown", "atari": "atari"} {
		if got := partitionSchemeStyle(in); got != want {
			t.Errorf("partitionSchemeStyle(%q) = %q, want %q", in, got, want)
		}
	}
	plist := "<dict>\n\t<key>Content</key>\n\t<string>FDisk_partition_scheme</string>\n\t<key>Ejectable</key>\n\t<true/>\n\t<key>ParentWholeDisk</key>\n\t<string>disk4</string>\n</dict>"
	if got := plistString(plist, "Content"); got != "FDisk_partition_scheme" {
		t.Errorf("Content = %q", got)
	}
	if got := plistString(plist, "Ejectable"); got != "" {
		t.Errorf("a key without a string must not borrow the next one: %q", got)
	}
	if got := plistString(plist, "Missing"); got != "" {
		t.Errorf("Missing = %q", got)
	}
}
