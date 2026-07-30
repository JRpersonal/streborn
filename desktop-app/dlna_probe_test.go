package main

import (
	"os"
	"testing"
)

// TestListMediaServersLive lists the DLNA servers on the LAN through the same
// binding the Library tab uses, so a reproduction can be built from real media
// instead of invented URLs.
func TestListMediaServersLive(t *testing.T) {
	if os.Getenv("STR_LIVE_DLNA") == "" {
		t.Skip("set STR_LIVE_DLNA=1 to scan the LAN for media servers")
	}
	a := NewApp()
	list, err := a.ListMediaServers(4)
	if err != nil {
		t.Fatalf("ListMediaServers: %v", err)
	}
	t.Logf("%d media server(s)", len(list))
	for _, s := range list {
		t.Logf("  udn=%s name=%q model=%q addr=%s", s.UDN, s.FriendlyName, s.ModelName, s.Address)
	}
}
