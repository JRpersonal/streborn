//go:build !windows

package main

// noShortcutRetargeter: outside Windows the update never renames the app (the
// Linux binary and the macOS bundle never carried a version in their names),
// so there is nothing to point elsewhere.
type noShortcutRetargeter struct{}

func (noShortcutRetargeter) Retarget(string, string) ([]string, error) { return nil, nil }

func newShortcutRetargeter() shortcutRetargeter { return noShortcutRetargeter{} }

// firewallRulesNaming is Windows-only; elsewhere there is nothing to count.
func firewallRulesNaming(string) int { return -1 }
