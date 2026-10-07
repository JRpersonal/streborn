//go:build !windows

package main

import (
	"context"
	"errors"
)

// firewallSupported: macOS and Linux have no per-app firewall rules the app
// could read or change without root, so the check and the unblock are Windows
// only and CheckFirewall reports Supported false here.
const firewallSupported = false

var errFirewallUnsupported = errors.New("the firewall check exists on Windows only")

func runFirewallQuery(context.Context, string) ([]byte, error) {
	return nil, errFirewallUnsupported
}

func runElevatedPowerShell(context.Context, string) (int, error) {
	return -1, errFirewallUnsupported
}
