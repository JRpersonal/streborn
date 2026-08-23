//go:build windows

package main

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// firewallProfiles are the three Windows Defender Firewall profiles, in the
// order the user sees them in the control panel.
var firewallProfiles = []struct{ key, label string }{
	{"DomainProfile", "domain"},
	{"StandardProfile", "private"},
	{"PublicProfile", "public"},
}

// localFirewallState reads whether each Windows firewall profile is switched
// on, straight out of the registry.
//
// Read from the registry rather than from `netsh advfirewall`: netsh is a
// subprocess that can take a second and, on some managed machines, raises a
// consent dialog. A HKLM read of the firewall policy is allowed for a normal
// user, cannot prompt, and is the value the netsh output is printed from
// anyway. Any error at all simply omits the line; a failure report that fails
// is worse than a thin one.
//
// The wording is deliberately narrow. This says the firewall is ON, and that
// is ALL it says: it does not read the per-application rules, so it cannot
// tell whether ST Reborn is allowed through. Overstating it here would send
// the next user off to turn a firewall off for nothing, which is the same
// wrong-blame this whole report was rewritten to stop.
func localFirewallState() string {
	var parts []string
	for _, p := range firewallProfiles {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE,
			`SYSTEM\CurrentControlSet\Services\SharedAccess\Parameters\FirewallPolicy\`+p.key,
			registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		v, _, verr := k.GetIntegerValue("EnableFirewall")
		_ = k.Close()
		if verr != nil {
			continue
		}
		state := "off"
		if v != 0 {
			state = "on"
		}
		parts = append(parts, fmt.Sprintf("%s=%s", p.label, state))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Windows firewall " + strings.Join(parts, " ") +
		" (this only says the firewall is switched on, not whether ST Reborn is allowed through it)"
}
