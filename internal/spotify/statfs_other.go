//go:build !linux

package spotify

// freeBytes has no statfs on non-Linux hosts. The agent only ever runs on the
// speaker's ARM Linux; this stub exists so the package builds and its tests
// run on dev hosts. ok=false makes every caller fail OPEN (an unknown free
// figure never blocks), exactly like a failed statfs on the box.
func freeBytes(string) (int64, bool) { return 0, false }
