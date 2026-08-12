//go:build !darwin

package main

import "fmt"

// The macOS in-place bundle swap has no meaning on other platforms; these keep
// the call sites free of build tags. Windows and Linux replace a single binary
// instead, see applyWindows / applyLinux.

func canSelfReplaceDarwin() bool { return false }

func (a *App) applyDarwin(string) error {
	return fmt.Errorf("the macOS in-place update is only available on macOS")
}
