// fat32.go — native FAT32 quick formatter.
//
// FmIfs.dll FormatEx refuses a quick format on a volume with NTFS remnants
// (packet 9 = FmIfsCantQuickFormat) and is hard to control otherwise. So we
// write the FAT32 structures ourselves directly to the locked, dismounted
// volume — which is exactly what Rufus + fat32format do internally.
//
// What we write:
//   Sector 0       boot sector with BPB (BIOS parameter block)
//   Sector 1       FSInfo
//   Sector 6       backup boot sector
//   Sector 7       backup FSInfo
//   Sector 32..    FAT 1 (large, with 3 init entries)
//   after FAT 1    FAT 2 (identical to FAT 1)
//   after FAT 2    root directory cluster (cluster 2, with volume label)
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

// fat32QuickFormat writes the FAT32 structures directly to the locked +
// dismounted volume handle. Expects:
//   - h is the CreateFile handle on \\.\X: from lockAndDismount
//   - totalBytes is the volume size
//   - clusterSize is the desired cluster size in bytes
//   - label is the volume label (max 11 characters, converted to ASCII)
func fat32QuickFormat(h uintptr, totalBytes uint64, clusterSize uint32, label string) error {
	const sectorSize = uint32(512)
	if clusterSize == 0 {
		clusterSize = 32 * 1024
	}
	if clusterSize%sectorSize != 0 {
		return fmt.Errorf("clusterSize %d is not a multiple of 512", clusterSize)
	}
	if totalBytes < 64*1024*1024 {
		return fmt.Errorf("volume too small (%d bytes), minimum size is 64 MB", totalBytes)
	}
	sectorsPerCluster := uint8(clusterSize / sectorSize)
	totalSectors := uint32(totalBytes / uint64(sectorSize))
	reservedSectors := uint16(32)
	numFATs := uint8(2)

	// Iterative FAT size: first estimate, then more precise.
	approxClusters := (totalSectors - uint32(reservedSectors)) / uint32(sectorsPerCluster)
	fatEntries := approxClusters + 2 // 2 reserved entries
	fatBytes := fatEntries * 4
	fatSectors := (fatBytes + sectorSize - 1) / sectorSize

	// Refinement: recompute with the FAT footprint included
	dataSectors := totalSectors - uint32(reservedSectors) - uint32(numFATs)*fatSectors
	clusterCount := dataSectors / uint32(sectorsPerCluster)
	if clusterCount < 65525 {
		return fmt.Errorf("too few clusters for FAT32: %d (min 65525)", clusterCount)
	}
	if clusterCount > 0x0FFFFFF5 {
		return fmt.Errorf("too many clusters for FAT32: %d (max 268435445)", clusterCount)
	}

	asciiLabel := makeASCIILabel(label)

	// Build the boot sector + FSInfo.
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
		_ = setFilePointer // possibly later for a 32-bit fallback
		if r == 0 {
			return fmt.Errorf("SetFilePointerEx to %d: %v", offset, e)
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
			return fmt.Errorf("WriteFile at offset %d: %v", offset, e)
		}
		if written != uint32(len(data)) {
			return fmt.Errorf("WriteFile at offset %d: wrote %d of %d", offset, written, len(data))
		}
		return nil
	}

	// 1. Boot sector + FSInfo + backups.
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
	// The first 12 bytes have special entries:
	//   Cluster 0: media descriptor (0x0FFFFFF8) + reserved
	//   Cluster 1: end-of-chain marker (0x0FFFFFFF)
	//   Cluster 2: end-of-chain — marks the root dir as allocated
	fatFirstSector := make([]byte, sectorSize)
	binary.LittleEndian.PutUint32(fatFirstSector[0:4], 0x0FFFFFF8)
	binary.LittleEndian.PutUint32(fatFirstSector[4:8], 0xFFFFFFFF)
	binary.LittleEndian.PutUint32(fatFirstSector[8:12], 0x0FFFFFFF)

	// Instead of writing sector by sector (slow): upload zeros in 1 MB
	// blocks.
	zeroBlock := make([]byte, 1024*1024)
	writeFATRegion := func(startSector uint32) error {
		// First sector with init entries
		if err := writeAt(int64(startSector)*int64(sectorSize), fatFirstSector); err != nil {
			return err
		}
		// Rest with zeros in MB chunks
		remaining := int64(fatSectors-1) * int64(sectorSize)
		offset := int64(startSector+1) * int64(sectorSize)
		for remaining > 0 {
			n := int64(len(zeroBlock))
			if n > remaining {
				n = remaining
			}
			// round n to a multiple of sectorSize
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

	// 3. Root directory cluster (cluster 2 = first data cluster).
	rootDirSector := fat1Start + uint32(numFATs)*fatSectors
	rootDir := make([]byte, clusterSize)
	if len(asciiLabel) > 0 {
		copy(rootDir[0:11], asciiLabel)
		rootDir[11] = 0x08 // volume label attribute
	}
	if err := writeAt(int64(rootDirSector)*int64(sectorSize), rootDir); err != nil {
		return err
	}

	// Flush so all of this lands on the hardware before we release the
	// handle and Windows starts its automount.
	flushFileBuffers.Call(h)
	return nil
}

// makeASCIILabel converts the label to 11 bytes of upper-case ASCII with
// space padding, as FAT32 expects.
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

// makeFAT32BootSector creates a 512-byte boot sector with a FAT32 BPB.
// Layout per the Microsoft FAT32 spec.
func makeFAT32BootSector(totalSectors uint32, secPerCluster uint8, fatSectors uint32, reservedSectors uint16, numFATs uint8, asciiLabel []byte) []byte {
	bs := make([]byte, 512)

	// Jump instruction (EB 58 90 = jmp +0x58)
	bs[0] = 0xEB
	bs[1] = 0x58
	bs[2] = 0x90

	// OEM name (8 bytes)
	copy(bs[3:11], []byte("MSWIN4.1"))

	// BPB
	binary.LittleEndian.PutUint16(bs[11:13], 512)             // bytes per sector
	bs[13] = secPerCluster                                    // sectors per cluster
	binary.LittleEndian.PutUint16(bs[14:16], reservedSectors) // reserved sectors
	bs[16] = numFATs                                          // number of FATs
	binary.LittleEndian.PutUint16(bs[17:19], 0)               // root entries (0 for FAT32)
	binary.LittleEndian.PutUint16(bs[19:21], 0)               // total sectors 16 (0 for FAT32)
	bs[21] = 0xF8                                             // media descriptor (fixed disk)
	binary.LittleEndian.PutUint16(bs[22:24], 0)               // sectors per FAT 16 (0 for FAT32)
	binary.LittleEndian.PutUint16(bs[24:26], 63)              // sectors per track
	binary.LittleEndian.PutUint16(bs[26:28], 255)             // heads
	binary.LittleEndian.PutUint32(bs[28:32], 0)               // hidden sectors
	binary.LittleEndian.PutUint32(bs[32:36], totalSectors)    // total sectors 32

	// FAT32-specific extended BPB
	binary.LittleEndian.PutUint32(bs[36:40], fatSectors) // sectors per FAT 32
	binary.LittleEndian.PutUint16(bs[40:42], 0)          // flags (mirror active FAT)
	binary.LittleEndian.PutUint16(bs[42:44], 0)          // filesystem version
	binary.LittleEndian.PutUint32(bs[44:48], 2)          // root cluster
	binary.LittleEndian.PutUint16(bs[48:50], 1)          // FSInfo sector
	binary.LittleEndian.PutUint16(bs[50:52], 6)          // backup boot sector
	// bs[52:64] = 12 reserved bytes (stay 0)
	bs[64] = 0x80 // drive number (hard disk)
	bs[65] = 0    // reserved
	bs[66] = 0x29 // extended boot signature
	// volume serial
	binary.LittleEndian.PutUint32(bs[67:71], uint32(time.Now().Unix()))
	// volume label (11 bytes)
	copy(bs[71:82], asciiLabel)
	// filesystem type (8 bytes "FAT32   ")
	copy(bs[82:90], []byte("FAT32   "))

	// boot signature
	bs[510] = 0x55
	bs[511] = 0xAA
	return bs
}

// makeFAT32FSInfo creates the FSInfo sector (512 bytes).
func makeFAT32FSInfo() []byte {
	fsi := make([]byte, 512)
	binary.LittleEndian.PutUint32(fsi[0:4], 0x41615252) // lead signature "RRaA"
	// fsi[4..484] = 480 reserved bytes (stay 0)
	binary.LittleEndian.PutUint32(fsi[484:488], 0x61417272) // struct signature "rrAa"
	binary.LittleEndian.PutUint32(fsi[488:492], 0xFFFFFFFF) // free cluster count (unknown)
	binary.LittleEndian.PutUint32(fsi[492:496], 0xFFFFFFFF) // next free cluster (unknown)
	// fsi[496..508] = 12 reserved bytes (stay 0)
	binary.LittleEndian.PutUint32(fsi[508:512], 0xAA550000) // trail signature
	return fsi
}
