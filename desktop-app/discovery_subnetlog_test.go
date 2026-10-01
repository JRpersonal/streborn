package main

import (
	"net"
	"strings"
	"testing"
)

// localIPv4Subnets returns sweep PREFIXES like "192.168.178.", not CIDR. A
// reader that expects a "/24" in them produces nothing on every machine, which
// is how the first version of the zero-result logging came out as dead code.
// Pin the shape so the next reader of this value does not repeat it.
func TestLocalIPv4SubnetsReturnsDottedPrefixes(t *testing.T) {
	for _, s := range localIPv4Subnets() {
		if strings.Contains(s, "/") {
			t.Errorf("subnet %q carries a CIDR mask; the sweep builds dotted prefixes", s)
		}
		if !strings.HasSuffix(s, ".") {
			t.Errorf("subnet %q does not end in a dot, so appending a host octet breaks", s)
		}
		if net.ParseIP(s+"1") == nil {
			t.Errorf("subnet %q plus a host octet is not an address", s)
		}
	}
}

// The count is what a bundle with no speakers has to carry. It must be
// derivable without a network, so the log line can never panic or block on a
// machine with no usable interface.
func TestLocalIPv4SubnetsIsSafeToCallAnywhere(t *testing.T) {
	got := localIPv4Subnets()
	if got == nil {
		return // no private IPv4 here; that is a valid answer and logs as 0
	}
	seen := map[string]bool{}
	for _, s := range got {
		if seen[s] {
			t.Errorf("subnet %q listed twice, so the count would overstate", s)
		}
		seen[s] = true
	}
}
