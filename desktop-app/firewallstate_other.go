//go:build !windows

package main

// localFirewallState returns nothing on macOS and Linux.
//
// There is no equivalent that can be read without either a subprocess that may
// prompt for elevation (pfctl, the macOS socketfilterfw tool) or root (iptables
// -L). The failure report must never be able to raise a password dialog, so the
// line is simply omitted rather than guessed at.
func localFirewallState() string { return "" }
