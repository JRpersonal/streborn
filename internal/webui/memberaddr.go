package webui

import (
	"errors"
	"net/netip"
	"strings"
)

// memberAddr takes a group member's address from a request and returns it
// as the canonical text of the IP address it names. An empty value stays
// empty (a member whose address the caller does not know is allowed and is
// resolved later). Anything that is not a unicast IP address is an error:
// the address ends up in URLs the agent fetches from, and the firmware's own
// zone API takes IP addresses only, so a host name or a port would fail
// there anyway, later and less clearly.
func memberAddr(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return "", errors.New("address is not an IP address")
	}
	addr = addr.Unmap()
	if addr.IsUnspecified() || addr.IsMulticast() {
		return "", errors.New("address is not a unicast IP address")
	}
	return addr.String(), nil
}
