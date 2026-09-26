package groupkeys

import (
	"context"
	"strings"
	"testing"
)

// The bug this pins, measured on real hardware on 2026-09-26.
//
// A SoundTouch answers to TWO ids: the firmware's own, and the one STR's agent
// announces. On an sm2 chassis they DIFFER (three ST10s measured: firmware
// DEV#5e3cac7a vs agent DEV#e2976145 and so on), on scm they are identical (an
// ST30 and a Portable). The desktop app deliberately stores the FIRMWARE id in
// a template member, and the peer roster indexed only the announced one, so the
// guard below compared two names for the same speaker, found them different,
// and refused EVERY group-key press with "another speaker answers at this
// address now" on a LAN where nothing had moved.
//
// Two users reported it as "the thumbs key does nothing" and re-saved the group
// over and over, which could never have helped.
func twoChipClient(t *testing.T, ids map[string][]string) *httpClient {
	t.Helper()
	return &httpClient{opts: HTTPOptions{
		PeerDeviceIDs: func(ip string) []string { return ids[ip] },
	}}
}

// A member carrying the FIRMWARE id must resolve against a roster that also
// knows the announced one. This failed before the fix.
func TestResolveAcceptsTheFirmwareIDOnATwoChipChassis(t *testing.T) {
	c := twoChipClient(t, map[string][]string{
		"192.0.2.31": {"DEV#e2976145", "DEV#5e3cac7a"}, // announced, firmware
	})
	m, err := c.resolve(context.Background(), Member{DeviceID: "DEV#5e3cac7a", IP: "192.0.2.31"})
	if err != nil {
		t.Fatalf("a member named by its firmware id must resolve, got: %v", err)
	}
	if m.IP != "192.0.2.31" {
		t.Errorf("address changed unexpectedly: %+v", m)
	}
}

// The announced id keeps working, which is the common single-id chassis.
func TestResolveStillAcceptsTheAnnouncedID(t *testing.T) {
	c := twoChipClient(t, map[string][]string{
		"192.0.2.31": {"DEV#e2976145", "DEV#5e3cac7a"},
	})
	if _, err := c.resolve(context.Background(), Member{DeviceID: "DEV#e2976145", IP: "192.0.2.31"}); err != nil {
		t.Fatalf("the announced id must still resolve, got: %v", err)
	}
}

// The guard must still do its job: a genuinely different speaker at the saved
// address is refused. That is what it was written for (a DHCP renumbering
// handing the old address to another box) and the fix must not soften it.
func TestResolveStillRefusesAGenuinelyDifferentSpeaker(t *testing.T) {
	c := twoChipClient(t, map[string][]string{
		"192.0.2.31": {"DEV#aaaaaaaa", "DEV#bbbbbbbb"},
	})
	_, err := c.resolve(context.Background(), Member{DeviceID: "DEV#5e3cac7a", IP: "192.0.2.31"})
	if err == nil {
		t.Fatal("an address now held by another speaker must still be refused")
	}
	if !strings.Contains(err.Error(), "another speaker answers") {
		t.Errorf("unexpected refusal text: %v", err)
	}
}

// An address the roster knows nothing about is not a refusal: the roster is
// allowed to be incomplete, and the stored address is then simply used.
func TestResolveAllowsAnUnknownAddress(t *testing.T) {
	c := twoChipClient(t, map[string][]string{})
	if _, err := c.resolve(context.Background(), Member{DeviceID: "DEV#5e3cac7a", IP: "192.0.2.99"}); err != nil {
		t.Fatalf("an address the roster does not know must not be refused, got: %v", err)
	}
}

// The older single-id wiring keeps working, so an agent that has not been
// updated still behaves exactly as before.
func TestResolveFallsBackToTheSingleIDLookup(t *testing.T) {
	c := &httpClient{opts: HTTPOptions{
		PeerDeviceID: func(string) string { return "DEV#e2976145" },
	}}
	if _, err := c.resolve(context.Background(), Member{DeviceID: "DEV#e2976145", IP: "192.0.2.31"}); err != nil {
		t.Fatalf("the single-id path must still accept a match, got: %v", err)
	}
	if _, err := c.resolve(context.Background(), Member{DeviceID: "DEV#5e3cac7a", IP: "192.0.2.31"}); err == nil {
		t.Fatal("the single-id path must still refuse a mismatch")
	}
}

func TestIDIn(t *testing.T) {
	ids := []string{"DEV#aaaa", "DEV#BBBB"}
	for _, want := range []string{"DEV#aaaa", "dev#bbbb", "DEV#BBBB"} {
		if !idIn(ids, want) {
			t.Errorf("%q should match case-insensitively", want)
		}
	}
	for _, want := range []string{"", "   ", "DEV#cccc"} {
		if idIn(ids, want) {
			t.Errorf("%q should not match", want)
		}
	}
}
