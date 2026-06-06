package main

import (
	"sort"
	"testing"
)

// TestBoxSortName covers the key the speaker list orders by: friendly name,
// then mDNS name, then host, all case-insensitive. Guards the stable ordering
// that keeps speakers from jumping position across discovery cycles (#108).
func TestBoxSortName(t *testing.T) {
	cases := []struct {
		box  BoxInfo
		want string
	}{
		{BoxInfo{FriendlyName: "Kitchen", Name: "str-x", Host: "192.168.0.5"}, "kitchen"},
		{BoxInfo{Name: "Living Room", Host: "192.168.0.6"}, "living room"},
		{BoxInfo{Host: "192.168.0.7"}, "192.168.0.7"},
		{BoxInfo{FriendlyName: "WohnZimmer"}, "wohnzimmer"},
	}
	for _, c := range cases {
		if got := boxSortName(c.box); got != c.want {
			t.Errorf("boxSortName(%+v) = %q, want %q", c.box, got, c.want)
		}
	}
}

// TestBoxSortStableOrder verifies the list is ordered by name then host, so a
// re-discovery returning the same boxes in a different (map) order produces the
// same displayed sequence.
func TestBoxSortStableOrder(t *testing.T) {
	boxes := []BoxInfo{
		{FriendlyName: "Zebra", Host: "192.168.0.9"},
		{FriendlyName: "alpha", Host: "192.168.0.2"},
		{FriendlyName: "alpha", Host: "192.168.0.1"}, // same name, lower host first
		{Host: "192.168.0.50"},                       // no name -> sorts by host string
	}
	sort.Slice(boxes, func(i, j int) bool {
		ni, nj := boxSortName(boxes[i]), boxSortName(boxes[j])
		if ni != nj {
			return ni < nj
		}
		return boxes[i].Host < boxes[j].Host
	})
	want := []string{"192.168.0.50", "192.168.0.1", "192.168.0.2", "192.168.0.9"}
	for i, w := range want {
		if boxes[i].Host != w {
			t.Errorf("position %d host = %q, want %q (order=%v)", i, boxes[i].Host, w,
				func() []string {
					var hs []string
					for _, b := range boxes {
						hs = append(hs, b.Host)
					}
					return hs
				}())
		}
	}
}
