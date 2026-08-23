package main

import (
	"reflect"
	"testing"
)

// candidatePorts must only ever offer the two STR agent ports. The install's
// verify step handed it a box record still carrying the pre-install Bose :8090,
// which produced [8090, 8888]: :17008 was unreachable, so on a BCO/whitelisted
// chassis a successful install was reported as failed, and :8090's own 404 was
// printed back at the user as "NOT REACHABLE (status 404)".

func TestCandidatePortsOnlyOffersAgentPorts(t *testing.T) {
	a := &App{}
	for _, tc := range []struct {
		name string
		port int
		want []int
	}{
		{"unset means both, redirect first", 0, []int{17008, 8888}},
		{"the stock Bose port is not an agent port", 8090, []int{17008, 8888}},
		{"the box web UI port is not one either", 8091, []int{17008, 8888}},
		{"a direct agent port is honoured", 8888, []int{8888, 17008}},
		{"any other port stays the caller's (the altAgentPortFor test seam)", 45123, []int{45123, 8888}},
		{"so is the redirect", 17008, []int{17008, 8888}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := a.candidatePorts("192.0.2.1", tc.port); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("candidatePorts(_, %d) = %v, want %v", tc.port, got, tc.want)
			}
		})
	}
}

func TestCandidatePortsPutsTheCachedPortFirst(t *testing.T) {
	a := &App{}
	a.rememberPort("192.0.2.1", 8888)

	// The cached port leads, and neither agent port is dropped or duplicated.
	if got, want := a.candidatePorts("192.0.2.1", 17008), []int{8888, 17008}; !reflect.DeepEqual(got, want) {
		t.Errorf("candidatePorts with cache = %v, want %v", got, want)
	}
	// Even when the caller passes a port that is not an agent port at all.
	if got, want := a.candidatePorts("192.0.2.1", 8090), []int{8888, 17008}; !reflect.DeepEqual(got, want) {
		t.Errorf("candidatePorts with cache and a stock port = %v, want %v", got, want)
	}
	// A different host is unaffected by that cache entry.
	if got, want := a.candidatePorts("192.0.2.2", 8090), []int{17008, 8888}; !reflect.DeepEqual(got, want) {
		t.Errorf("candidatePorts for another host = %v, want %v", got, want)
	}
}
