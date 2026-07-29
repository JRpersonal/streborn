package main

import "testing"

// #494: a speaker rendered as "str-<ip>" in the picker is the same defect as a
// bare address, so the placeholder must be recognised and never a real name.
func TestPlaceholderPeerName(t *testing.T) {
	placeholders := []string{
		"str-192.0.2.10", "str-10.0.0.1", "str-192.168.178.49",
		// mDNS announces "STR-<last 6 of the device ID>", i.e. a MAC fragment,
		// which one reporter saw in the picker as "part of their MAC addresses".
		"STR-3E6CE1", "STR-c94a51", "str-AABBCC", "STR-488D",
	}
	for _, n := range placeholders {
		if !placeholderPeerName(n) {
			t.Errorf("%q must be recognised as a placeholder", n)
		}
	}
	real := []string{
		"Wohnzimmer", "SoundTouch 10b", "str-Kueche", "str-", "Arbeitszimmer300", "streborn",
		"STR-Bad",     // too short and not hex: a person chose this
		"str-office2", // letters beyond hex
		"STR",         // the bare service name, no separator
	}
	for _, n := range real {
		if placeholderPeerName(n) {
			t.Errorf("%q is a real name and must not be treated as a placeholder", n)
		}
	}
}
