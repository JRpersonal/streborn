package main

import "testing"

// #494: a speaker rendered as "str-<ip>" in the picker is the same defect as a
// bare address, so the placeholder must be recognised and never a real name.
func TestPlaceholderPeerName(t *testing.T) {
	placeholders := []string{"str-192.0.2.10", "str-10.0.0.1", "str-192.168.178.49"}
	for _, n := range placeholders {
		if !placeholderPeerName(n) {
			t.Errorf("%q must be recognised as a placeholder", n)
		}
	}
	real := []string{"Wohnzimmer", "SoundTouch 10b", "str-Kueche", "str-", "Arbeitszimmer300", "streborn"}
	for _, n := range real {
		if placeholderPeerName(n) {
			t.Errorf("%q is a real name and must not be treated as a placeholder", n)
		}
	}
}
