//go:build !darwin

package main

import "fmt"

// The macOS in-place bundle swap has no meaning on other platforms; these keep
// the call sites free of build tags. Windows and Linux replace a single binary
// instead, see applyWindows / applyLinux.

func canSelfReplaceDarwin() bool { return false }

// SelfUpdateState is the darwin-only report; on every other platform the
// in-place update does not go through the bundle swap, so there is nothing to
// say here beyond naming the platform.
func SelfUpdateState() (path, reason string, ok bool) {
	return "", "not_macos", false
}

func (a *App) applyDarwin(string) error {
	return fmt.Errorf("the macOS in-place update is only available on macOS")
}
