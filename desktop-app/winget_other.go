//go:build !windows

package main

import "errors"

// winget exists only on Windows; wingetManagedExe never reports a managed
// install elsewhere, so these are never reached in practice. They keep the
// shared code free of build tags.

func (a *App) runWingetUpgrade(string, string) WingetUpdateResult {
	return wingetOutcome(wingetReasonUnsupported)
}

func createShortcut(string, string) error {
	return errors.New("start menu shortcuts exist only on Windows")
}
