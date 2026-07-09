package main

import (
	"net"
	"path/filepath"
	"reflect"
	"testing"
)

// The primary-route subnet must move to the front (virtual adapters enumerate
// first on Windows and used to eat the sweep budget); unknown or missing
// primaries keep the original order.
func TestOrderSubnetsPrimaryFirst(t *testing.T) {
	subnets := []string{"172.28.16.", "172.17.0.", "192.168.178."}
	got := orderSubnetsPrimaryFirst(subnets, net.IPv4(192, 168, 178, 42).To4())
	want := []string{"192.168.178.", "172.28.16.", "172.17.0."}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("orderSubnetsPrimaryFirst = %v, want %v", got, want)
	}
	// Primary already first: unchanged.
	got = orderSubnetsPrimaryFirst(want, net.IPv4(192, 168, 178, 42).To4())
	if !reflect.DeepEqual(got, want) {
		t.Errorf("already-first reordered to %v", got)
	}
	// No route / unknown subnet: unchanged.
	if got := orderSubnetsPrimaryFirst(subnets, nil); !reflect.DeepEqual(got, subnets) {
		t.Errorf("nil primary reordered to %v", got)
	}
	if got := orderSubnetsPrimaryFirst(subnets, net.IPv4(10, 0, 0, 5).To4()); !reflect.DeepEqual(got, subnets) {
		t.Errorf("foreign primary reordered to %v", got)
	}
}

// known-speakers.json round-trips and caps its length.
func TestKnownSpeakersRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known-speakers.json")
	if got := loadKnownSpeakersFrom(path); got != nil {
		t.Fatalf("missing file should load nil, got %v", got)
	}
	list := []knownSpeaker{
		{Host: "192.168.178.79", DeviceID: "AABBCC", Kind: "str"},
		{Host: "192.168.178.30", Kind: "stock"},
	}
	if err := saveKnownSpeakersTo(path, list); err != nil {
		t.Fatal(err)
	}
	got := loadKnownSpeakersFrom(path)
	if !reflect.DeepEqual(got, list) {
		t.Errorf("round-trip = %v, want %v", got, list)
	}
	// Cap: 40 entries in, maxKnownSpeakers out.
	big := make([]knownSpeaker, 40)
	for i := range big {
		big[i] = knownSpeaker{Host: "10.0.0.1"}
	}
	if err := saveKnownSpeakersTo(path, big); err != nil {
		t.Fatal(err)
	}
	if got := loadKnownSpeakersFrom(path); len(got) != maxKnownSpeakers {
		t.Errorf("cap = %d entries, want %d", len(got), maxKnownSpeakers)
	}
}

// sweepSubnets must visit every host of every subnet when the context allows.
func TestSweepSubnetsVisitsAllHosts(t *testing.T) {
	var mu, seen = make(chan struct{}, 1), map[string]bool{}
	mu <- struct{}{}
	sweepSubnets(t.Context(), []string{"10.0.0.", "10.0.1."}, func(ip string) {
		<-mu
		seen[ip] = true
		mu <- struct{}{}
	})
	if len(seen) != 2*254 {
		t.Errorf("visited %d hosts, want %d", len(seen), 2*254)
	}
	if !seen["10.0.0.1"] || !seen["10.0.1.254"] {
		t.Error("edge hosts missing from the sweep")
	}
}
