package boxsnapshot

import "github.com/JRpersonal/streborn/internal/atomicfile"

// writeState is the one writer behind the snapshot, the reflect-sources list
// and the box label. It writes durably (temp file "<path>.tmp", fsync, rename,
// directory fsync): the earlier temp + rename without an fsync could leave the
// file empty after a power cut, because the rename reached the NAND before the
// data did. All three files are 0644.
func writeState(path string, data []byte) error {
	return atomicfile.WriteFile(path, data, 0o644)
}
