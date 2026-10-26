package sticksetup

// Writing a Bose firmware file (Update.stu) onto a stick, for the firmware
// ladder: a speaker on very old firmware takes each in-between update from a
// USB stick (4 + Volume minus while powering on).
//
// The file is about 100 MB, so it is copied as a stream, never held in memory.
// It goes to Update.stu.part first, is fsynced (Windows otherwise keeps it in
// the lazy write cache and a pulled stick loses it), renamed, and then read back
// from the stick and checked against the expected size and CRC-32. A stick that
// silently drops or corrupts data fails here, on the PC, and not later on the
// speaker. The STR payload files on the stick are left alone.

import (
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
)

// FirmwareFileName is the name the speaker looks for at the root of the stick.
const FirmwareFileName = "Update.stu"

// MaxRecommendedStickBytes: sticks over 32 GB are often formatted with 64 KB
// clusters by third-party tools, which the speaker cannot read. A warning only.
const MaxRecommendedStickBytes int64 = 32 << 30

// MaxClusterBytes is the largest FAT32 cluster the speaker's old vfat driver
// reads (project memory: a 64 KB cluster gives "Input/output error").
const MaxClusterBytes int64 = 32 * 1024

// ErrFirmwareMismatch is returned when the bytes on the stick are not the
// expected ones.
var ErrFirmwareMismatch = errors.New("the file on the stick does not match")

// firmwareReadBack reads the written file back from the stick; a var so a test
// can play a stick that corrupts data.
var firmwareReadBack = func(path string) (int64, uint32, error) {
	fh, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer fh.Close()
	h := crc32.NewIEEE()
	n, err := io.Copy(h, fh)
	return n, h.Sum32(), err
}

// WriteFirmwareFile copies srcPath to <stickPath>/Update.stu and checks it.
// wantLen and wantCRC are the expected size and CRC-32 (IEEE) of the file.
// onProgress, when not nil, gets the bytes written so far.
func WriteFirmwareFile(stickPath, srcPath string, wantLen int64, wantCRC uint32, onProgress func(done int64)) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()
	return writeFirmwareFrom(stickPath, src, wantLen, wantCRC, onProgress)
}

func writeFirmwareFrom(stickPath string, src io.Reader, wantLen int64, wantCRC uint32, onProgress func(int64)) error {
	if wantLen <= 0 {
		return fmt.Errorf("no expected size for the firmware file")
	}
	st, err := os.Stat(stickPath)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a folder", stickPath)
	}
	final := filepath.Join(stickPath, FirmwareFileName)
	part := final + ".part"
	// A stale Update.stu from an earlier step must never stay behind: the
	// speaker would install the wrong version from it.
	if err := os.Remove(final); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove the old %s: %w", FirmwareFileName, err)
	}
	_ = os.Remove(part)

	out, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = out.Close()
			_ = os.Remove(part)
		}
	}()
	h := crc32.NewIEEE()
	w := io.MultiWriter(out, h)
	if onProgress != nil {
		w = &progressWriter{w: w, fn: onProgress}
	}
	n, err := io.Copy(w, io.LimitReader(src, wantLen+1))
	if err != nil {
		return fmt.Errorf("writing to the stick stopped after %d of %d bytes: %w", n, wantLen, err)
	}
	if n != wantLen || h.Sum32() != wantCRC {
		return fmt.Errorf("%w: source gave %d bytes, CRC %08x; expected %d bytes, CRC %08x", ErrFirmwareMismatch, n, h.Sum32(), wantLen, wantCRC)
	}
	if err := out.Sync(); err != nil {
		return fmt.Errorf("flush to the stick: %w", err)
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(part, final); err != nil {
		return err
	}
	ok = true
	rn, rcrc, err := firmwareReadBack(final)
	if err == nil && (rn != wantLen || rcrc != wantCRC) {
		err = fmt.Errorf("%w: read back %d bytes, CRC %08x; expected %d bytes, CRC %08x", ErrFirmwareMismatch, rn, rcrc, wantLen, wantCRC)
	}
	if err != nil {
		_ = os.Remove(final)
		return fmt.Errorf("checking the file on the stick: %w", err)
	}
	return nil
}

type progressWriter struct {
	w    io.Writer
	fn   func(int64)
	done int64
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.done += int64(n)
	p.fn(p.done)
	return n, err
}

// FirmwareStickProblem is the firmware ladder's verdict on a checked stick:
// "" when an Update.stu of need bytes can go on it, otherwise a code the app
// maps to a message. It adds to CheckStick's own Reason ("gone", "read-only",
// "not-fat32", ...) the two things only a firmware file needs: a cluster the
// speaker can read ("cluster-too-big") and room for the file ("no-space").
// existing is the size of an Update.stu already on the stick, which the write
// replaces.
func FirmwareStickProblem(c StickCheck, need, existing int64) string {
	if !c.OK {
		if c.Reason == "" {
			return "not-writable"
		}
		return c.Reason
	}
	if c.ClusterTooBig {
		return "cluster-too-big"
	}
	if c.FreeBytes+existing < need+FirmwareHeadroomBytes {
		return "no-space"
	}
	return ""
}

// FirmwareHeadroomBytes is kept free beyond the file itself.
const FirmwareHeadroomBytes int64 = 10 * 1000 * 1000

// ExistingFirmwareBytes is the size of an Update.stu already on the stick
// (0 when there is none). The write replaces it, so its space counts as free.
func ExistingFirmwareBytes(stickPath string) int64 {
	if st, err := os.Stat(filepath.Join(stickPath, FirmwareFileName)); err == nil {
		return st.Size()
	}
	return 0
}
