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

func queryFirewall(context.Context) ([]fwRule, []string, error) {
	return nil, nil, errFirewallUnsupported
}

func applyFirewallUnblock([]string, bool) error {
	return errFirewallUnsupported
}

func runElevatedFirewallHelper(context.Context, []string) (int, error) {
	return -1, errFirewallUnsupported
}
