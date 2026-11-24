package usbmedia

import (
	"encoding/binary"
	"io"
	"os"
	"strings"
)

// The volume label of a FAT stick, so the Library can say which stick it is
// ("USB stick MUSIC (Kitchen)").
//
// Windows keeps the label the user sees in a volume-label entry of the root
// directory (attribute 0x08) and leaves the boot sector field alone when the
// stick is renamed, often at "NO NAME". So the root directory entry wins and
// the boot sector field is only the fallback. The kernel's vfat driver hides
// that entry from a directory listing, and the 3.14 kernel has no ioctl for
// it, so it is read from the device: the boot sector and the first cluster of
// the root directory, at most labelRootRead bytes, once when a stick starts
// being served. Read-only, like everything here.

// labelRootRead bounds the root directory read. The label entry is normally
// the first entry, and a 32 KB cluster is the largest the speaker reads.
const labelRootRead = 32 << 10

// ReadVolumeLabel returns the label of the FAT volume on r, "" when it has
// none or r is not FAT.
func ReadVolumeLabel(r io.ReaderAt) string {
	b := make([]byte, 512)
	if _, err := r.ReadAt(b, 0); err != nil {
		return ""
	}
	v := sniffSector(b)
	if v.FS != FSFAT {
		return ""
	}
	if l, found := rootDirLabel(r, b, v.FATType); found {
		return l
	}
	off := 43 // FAT12/16 BS_VolLab
	if v.FATType == "FAT32" {
		off = 71
	}
	return cleanLabel(b[off : off+11])
}

// ReadVolumeLabelDevice opens the device node and reads its label, read-only.
func ReadVolumeLabelDevice(devPath string) string {
	f, err := os.Open(devPath)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	return ReadVolumeLabel(f)
}

// rootDirLabel reads the volume-label entry of the root directory. found is
// true when the root directory could be read, label or not: a root directory
// without a label entry is a stick without a label, whatever the boot sector
// still says.
func rootDirLabel(r io.ReaderAt, b []byte, fatType string) (string, bool) {
	bps := int64(binary.LittleEndian.Uint16(b[11:13]))
	spc := int64(b[13])
	reserved := int64(binary.LittleEndian.Uint16(b[14:16]))
	fats := int64(b[16])
	switch bps {
	case 512, 1024, 2048, 4096:
	default:
		return "", false
	}
	if spc == 0 || spc&(spc-1) != 0 || reserved == 0 || fats == 0 || fats > 4 {
		return "", false
	}
	var sector, n int64
	if fatType == "FAT32" {
		fatSz := int64(binary.LittleEndian.Uint32(b[36:40]))
		rootClus := int64(binary.LittleEndian.Uint32(b[44:48]))
		if fatSz == 0 || rootClus < 2 {
			return "", false
		}
		sector = reserved + fats*fatSz + (rootClus-2)*spc
		n = spc * bps
	} else {
		fatSz := int64(binary.LittleEndian.Uint16(b[22:24]))
		entries := int64(binary.LittleEndian.Uint16(b[17:19]))
		if fatSz == 0 || entries == 0 {
			return "", false
		}
		sector = reserved + fats*fatSz
		n = entries * 32
	}
	if n > labelRootRead {
		n = labelRootRead
	}
	dir := make([]byte, n)
	got, err := r.ReadAt(dir, sector*bps)
	if got < 32 && err != nil {
		return "", false
	}
	dir = dir[:got-got%32]
	for i := 0; i+32 <= len(dir); i += 32 {
		e := dir[i : i+32]
		switch e[0] {
		case 0x00:
			return "", true // end of the directory
		case 0xE5:
			continue // deleted
		}
		attr := e[11]
		if attr == 0x0F || attr&0x08 == 0 || attr&0x10 != 0 {
			continue // a long-name part, a file or a folder
		}
		return cleanLabel(e[:11]), true
	}
	return "", true
}

// cleanLabel turns the 11 label bytes (OEM code page 437, padded with spaces)
// into text. "NO NAME" is what a stick without a label carries in its boot
// sector.
func cleanLabel(raw []byte) string {
	var s strings.Builder
	for _, c := range raw {
		switch {
		case c >= 0x80:
			s.WriteRune(cp437High[c-0x80])
		case c >= 0x20 && c != 0x7F:
			s.WriteByte(c)
		}
	}
	l := strings.TrimSpace(s.String())
	if strings.EqualFold(l, "NO NAME") {
		return ""
	}
	return l
}

// cp437High is code page 437 from 0x80 up, the OEM code page FAT labels are
// written in (Windows in Western Europe uses 850, which agrees with it on the
// letters a label is usually made of: the umlauts, é, ç, ñ and ß).
var cp437High = []rune("ÇüéâäàåçêëèïîìÄÅÉæÆôöòûùÿÖÜ¢£¥₧ƒáíóúñÑªº¿⌐¬½¼¡«»░▒▓│┤╡╢╖╕╣║╗╝╜╛┐└┴┬├─┼╞╟╚╔╩╦╠═╬╧╨╤╥╙╘╒╓╫╪┘┌█▄▌▐▀αßΓπΣσµτΦΘΩδ∞φε∩≡±≥≤⌠⌡÷≈°∙·√ⁿ²■ ")
