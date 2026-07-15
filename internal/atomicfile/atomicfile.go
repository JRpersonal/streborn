// Package atomicfile writes a file durably, so an unclean shutdown cannot leave
// it empty or torn.
//
// A plain os.WriteFile followed by os.Rename is ATOMIC for a concurrent reader
// (it sees either the old file or the whole new one) but it is NOT DURABLE: the
// rename's metadata can reach stable storage while the file's data blocks are
// still sitting in the page cache. If power is lost at that moment, the renamed
// file is present but its data is gone, so it comes back as ZERO BYTES.
//
// On the SoundTouch speakers this is not a corner case. They cut power at
// standby, so a file written shortly before an overnight standby routinely came
// back empty the next morning: presets.json and last-play.json were found at 0
// bytes on users' boxes, which wiped every preset (2026-07-15). WriteFile fixes
// that by fsyncing the data to flash BEFORE the rename and fsyncing the directory
// AFTER, so both the data and the rename survive a power-cut.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFile durably writes data to path. It writes a sibling temp file (same
// directory, so the rename stays on one filesystem and is itself atomic),
// fsyncs its data, renames it over path, then fsyncs the directory. On any
// failure the temp file is removed and the original path is left untouched.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("open temp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write temp: %w", err)
	}
	// fsync the DATA to flash before the rename. This is the step that makes the
	// write survive a power-cut: without it the rename can land while the data is
	// still buffered, leaving a 0-byte file after the next unclean boot.
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("fsync temp: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	// fsync the DIRECTORY so the rename itself is durable; otherwise a power loss
	// just after the rename could revert path to its old entry. Best-effort: some
	// filesystems do not allow opening a directory for sync, and the data fsync
	// above is the part that prevents the 0-byte loss.
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
