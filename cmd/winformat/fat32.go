// fat32.go — nativer FAT32 Quick Formatter.
//
// FmIfs.dll FormatEx verweigert Quick Format auf einem Volume mit
// NTFS Resten (Packet 9 = FmIfsCantQuickFormat) und ist auch sonst
// schwer zu kontrollieren. Wir schreiben die FAT32 Strukturen daher
// selbst direkt auf das gelockte, dismounted Volume — das ist exakt
// was Rufus + fat32format intern tun.
//
// Was wir schreiben:
//   Sektor 0       Boot Sector mit BPB (BIOS Parameter Block)
//   Sektor 1       FSInfo
//   Sektor 6       Backup Boot Sector
//   Sektor 7       Backup FSInfo
//   Sektor 32..    FAT 1 (gross, mit 3 init Eintraegen)
//   nach FAT 1     FAT 2 (identisch zu FAT 1)
//   nach FAT 2     Root Directory Cluster (Cluster 2, mit Volume Label)
//
//go:build windows

package main

import (
	"encoding/binary"
	"fmt"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// fat32QuickFormat schreibt die FAT32 Strukturen direkt auf das
// gelockte + dismounted Volume Handle. Erwartet:
//   - h ist das CreateFile Handle auf \\.\X: aus lockAndDismount
//   - totalBytes ist die Volume Groesse
//   - clusterSize ist die gewuenschte Cluster Size in Bytes
//   - label ist das Volume Label (max 11 Zeichen, in ASCII konvertiert)
func fat32QuickFormat(h uintptr, totalBytes uint64, clusterSize uint32, label string) error {
	const sectorSize = uint32(512)
	if clusterSize == 0 {
		clusterSize = 32 * 1024
	}
	if clusterSize%sectorSize != 0 {
		return fmt.Errorf("clusterSize %d nicht Vielfaches von 512", clusterSize)
	}
	if totalBytes < 64*1024*1024 {
		return fmt.Errorf("volume too small (%d bytes), minimum size is 64 MB", totalBytes)
	}
	sectorsPerCluster := uint8(clusterSize / sectorSize)
	totalSectors := uint32(totalBytes / uint64(sectorSize))
	reservedSectors := uint16(32)
	numFATs := uint8(2)

	// Iterative FAT Groesse: erste Schaetzung, dann genauer.
	approxClusters := (totalSectors - uint32(reservedSectors)) / uint32(sectorsPerCluster)
	fatEntries := approxClusters + 2 // 2 reservierte Eintraege
	fatBytes := fatEntries * 4
	fatSectors := (fatBytes + sectorSize - 1) / sectorSize

	// Refinement: rechne mit FAT Belegung erneut
	dataSectors := totalSectors - uint32(reservedSectors) - uint32(numFATs)*fatSectors
	clusterCount := dataSectors / uint32(sectorsPerCluster)
	if clusterCount < 65525 {
		return fmt.Errorf("zu wenig Cluster fuer FAT32: %d (min 65525)", clusterCount)
	}
	if clusterCount > 0x0FFFFFF5 {
		return fmt.Errorf("zu viele Cluster fuer FAT32: %d (max 268435445)", clusterCount)
	}

	asciiLabel := makeASCIILabel(label)

	// Boot Sector + FSInfo aufbauen.
	bootSector := makeFAT32BootSector(totalSectors, sectorsPerCluster, fatSectors, reservedSectors, numFATs, asciiLabel)
	fsInfo := makeFAT32FSInfo()

	k32 := syscall.NewLazyDLL("kernel32.dll")
	writeFileProc := k32.NewProc("WriteFile")
	setFilePointer := k32.NewProc("SetFilePointer")
	setFilePointerEx := k32.NewProc("SetFilePointerEx")
	flushFileBuffers := k32.NewProc("FlushFileBuffers")

	setPos := func(offset int64) error {
		var newPos int64
		r, _, e := setFilePointerEx.Call(h, uintptr(offset),
			uintptr(unsafe.Pointer(&newPos)), 0)
		_ = setFilePointer // ggf. spaeter fuer 32 Bit Fallback
		if r == 0 {
			return fmt.Errorf("SetFilePointerEx auf %d: %v", offset, e)
		}
		return nil
	}
	writeAt := func(offset int64, data []byte) error {
		if err := setPos(offset); err != nil {
			return err
		}
		var written uint32
		r, _, e := writeFileProc.Call(h,
			uintptr(unsafe.Pointer(&data[0])),
			uintptr(len(data)),
			uintptr(unsafe.Pointer(&written)),
			0)
		if r == 0 {
			return fmt.Errorf("WriteFile bei offset %d: %v", offset, e)
		}
		if written != uint32(len(data)) {
			return fmt.Errorf("WriteFile bei offset %d: %d von %d geschrieben", offset, written, len(data))
		}
		return nil
	}

	// 1. Boot Sector + FSInfo + Backups.
	if err := writeAt(0, bootSector); err != nil {
		return err
	}
	if err := writeAt(int64(sectorSize), fsInfo); err != nil {
		return err
	}
	if err := writeAt(int64(6*sectorSize), bootSector); err != nil {
		return err
	}
	if err := writeAt(int64(7*sectorSize), fsInfo); err != nil {
		return err
	}

	// 2. FAT 1 + FAT 2.
	// Erste 12 Bytes haben spezielle Eintraege:
	//   Cluster 0: Media Descriptor (0x0FFFFFF8) + reserved
	//   Cluster 1: End of Chain Marker (0x0FFFFFFF)
	//   Cluster 2: End of Chain — markiert Root Dir als allokiert
	fatFirstSector := make([]byte, sectorSize)
	binary.LittleEndian.PutUint32(fatFirstSector[0:4], 0x0FFFFFF8)
	binary.LittleEndian.PutUint32(fatFirstSector[4:8], 0xFFFFFFFF)
	binary.LittleEndian.PutUint32(fatFirstSector[8:12], 0x0FFFFFFF)

	// Statt sektorweise zu schreiben (langsam): in 1 MB Bloecken zero
	// hochladen.
	zeroBlock := make([]byte, 1024*1024)
	writeFATRegion := func(startSector uint32) error {
		// Erster Sektor mit init Eintraegen
		if err := writeAt(int64(startSector)*int64(sectorSize), fatFirstSector); err != nil {
			return err
		}
		// Rest mit Nullen in MB Chunks
		remaining := int64(fatSectors-1) * int64(sectorSize)
		offset := int64(startSector+1) * int64(sectorSize)
		for remaining > 0 {
			n := int64(len(zeroBlock))
			if n > remaining {
				n = remaining
			}
			// n auf sectorSize Vielfaches
			n = (n / int64(sectorSize)) * int64(sectorSize)
			if n == 0 {
				n = int64(sectorSize)
			}
			if err := writeAt(offset, zeroBlock[:n]); err != nil {
				return err
			}
			offset += n
			remaining -= n
		}
		return nil
	}

	fat1Start := uint32(reservedSectors)
	if err := writeFATRegion(fat1Start); err != nil {
		return err
	}
	fat2Start := fat1Start + fatSectors
	if err := writeFATRegion(fat2Start); err != nil {
		return err
	}

	// 3. Root Directory Cluster (Cluster 2 = erstes Daten Cluster).
	rootDirSector := fat1Start + uint32(numFATs)*fatSectors
	rootDir := make([]byte, clusterSize)
	if len(asciiLabel) > 0 {
		copy(rootDir[0:11], asciiLabel)
		rootDir[11] = 0x08 // Volume Label Attribute
	}
	if err := writeAt(int64(rootDirSector)*int64(sectorSize), rootDir); err != nil {
		return err
	}

	// Flush damit das alles auf der Hardware landet bevor wir das
	// Handle freigeben und Windows automount startet.
	flushFileBuffers.Call(h)
	return nil
}

// makeASCIILabel konvertiert das Label in 11 Bytes ASCII upper case
// mit Space Padding wie FAT32 das erwartet.
func makeASCIILabel(label string) []byte {
	label = strings.ToUpper(label)
	out := make([]byte, 11)
	for i := range out {
		out[i] = ' '
	}
	for i, r := range label {
		if i >= 11 {
			break
		}
		if r > 0x7F {
			out[i] = '_'
		} else {
			out[i] = byte(r)
		}
	}
	return out
}

// makeFAT32BootSector erzeugt einen 512 Byte Boot Sector mit FAT32 BPB.
// Layout nach Microsoft FAT32 Spec.
func makeFAT32BootSector(totalSectors uint32, secPerCluster uint8, fatSectors uint32, reservedSectors uint16, numFATs uint8, asciiLabel []byte) []byte {
	bs := make([]byte, 512)

	// Jump Instruction (EB 58 90 = jmp +0x58)
	bs[0] = 0xEB
	bs[1] = 0x58
	bs[2] = 0x90

	// OEM Name (8 Bytes)
	copy(bs[3:11], []byte("MSWIN4.1"))

	// BPB
	binary.LittleEndian.PutUint16(bs[11:13], 512)             // Bytes per Sector
	bs[13] = secPerCluster                                    // Sectors per Cluster
	binary.LittleEndian.PutUint16(bs[14:16], reservedSectors) // Reserved Sectors
	bs[16] = numFATs                                          // Number of FATs
	binary.LittleEndian.PutUint16(bs[17:19], 0)               // Root Entries (0 bei FAT32)
	binary.LittleEndian.PutUint16(bs[19:21], 0)               // Total Sectors 16 (0 bei FAT32)
	bs[21] = 0xF8                                             // Media Descriptor (fixed disk)
	binary.LittleEndian.PutUint16(bs[22:24], 0)               // Sectors per FAT 16 (0 bei FAT32)
	binary.LittleEndian.PutUint16(bs[24:26], 63)              // Sectors per Track
	binary.LittleEndian.PutUint16(bs[26:28], 255)             // Heads
	binary.LittleEndian.PutUint32(bs[28:32], 0)               // Hidden Sectors
	binary.LittleEndian.PutUint32(bs[32:36], totalSectors)    // Total Sectors 32

	// FAT32 specific Extended BPB
	binary.LittleEndian.PutUint32(bs[36:40], fatSectors) // Sectors per FAT 32
	binary.LittleEndian.PutUint16(bs[40:42], 0)          // Flags (Mirror Active FAT)
	binary.LittleEndian.PutUint16(bs[42:44], 0)          // Filesystem Version
	binary.LittleEndian.PutUint32(bs[44:48], 2)          // Root Cluster
	binary.LittleEndian.PutUint16(bs[48:50], 1)          // FSInfo Sector
	binary.LittleEndian.PutUint16(bs[50:52], 6)          // Backup Boot Sector
	// bs[52:64] = 12 Reserved Bytes (bleiben 0)
	bs[64] = 0x80 // Drive Number (Hard Disk)
	bs[65] = 0    // Reserved
	bs[66] = 0x29 // Extended Boot Signature
	// Volume Serial
	binary.LittleEndian.PutUint32(bs[67:71], uint32(time.Now().Unix()))
	// Volume Label (11 Bytes)
	copy(bs[71:82], asciiLabel)
	// Filesystem Type (8 Bytes "FAT32   ")
	copy(bs[82:90], []byte("FAT32   "))

	// Boot Signature
	bs[510] = 0x55
	bs[511] = 0xAA
	return bs
}

// makeFAT32FSInfo erzeugt den FSInfo Sektor (512 Bytes).
func makeFAT32FSInfo() []byte {
	fsi := make([]byte, 512)
	binary.LittleEndian.PutUint32(fsi[0:4], 0x41615252) // Lead Signature "RRaA"
	// fsi[4..484] = 480 Reserved Bytes (bleiben 0)
	binary.LittleEndian.PutUint32(fsi[484:488], 0x61417272) // Struct Signature "rrAa"
	binary.LittleEndian.PutUint32(fsi[488:492], 0xFFFFFFFF) // Free Cluster Count (unbekannt)
	binary.LittleEndian.PutUint32(fsi[492:496], 0xFFFFFFFF) // Next Free Cluster (unbekannt)
	// fsi[496..508] = 12 Reserved Bytes (bleiben 0)
	binary.LittleEndian.PutUint32(fsi[508:512], 0xAA550000) // Trail Signature
	return fsi
}
