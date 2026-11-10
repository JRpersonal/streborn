package webui

import "testing"

func TestLeaseNeedsRenewal(t *testing.T) {
	if leaseNeedsRenewal(true) {
		t.Error("a switch to the network the speaker was already on must keep its lease")
	}
	if !leaseNeedsRenewal(false) {
		t.Error("a move to another network must ask for a fresh lease")
	}
}
