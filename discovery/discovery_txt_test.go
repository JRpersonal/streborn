package discovery

import "testing"

// TestUnescapeTXT covers the box-name mojibake fix: the mDNS wire-decoder returns
// TXT values in DNS presentation form (non-printable bytes as \DDD decimal), so a
// raw-UTF-8 name "Küche" (bytes C3 BC) arrives as "K\195\188che" and must be
// decoded back to bytes. The function must be idempotent on already-correct
// names and must preserve a lone high byte for the downstream UTF-8 widening.
func TestUnescapeTXT(t *testing.T) {
	cases := map[string]string{
		`K\195\188che`:      "Küche",           // C3 BC -> ü (the reported bug)
		"Küche":             "Küche",           // already correct: idempotent
		"Living Room":       "Living Room",     // plain ASCII: unchanged
		"":                  "",                // empty
		`name\\with\"quote`: `name\with"quote`, // backslash + quote escapes
		`caf\233`:           "caf\xe9",         // lone Latin-1 byte preserved (é = 0xE9)
		`a\999b`:            "a999b",           // >255 is not a valid byte escape: drop the backslash
	}
	for in, want := range cases {
		if got := unescapeTXT(in); got != want {
			t.Errorf("unescapeTXT(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDecodeMDNSText covers the instance-name half of the same fix (field report
// 2026-10-20): the speaker list showed "K\195\131\194\188che", an escaped name
// whose bytes were UTF-8 encoded twice. Both layers have to come off, and names
// that were right all along must come through untouched.
func TestDecodeMDNSText(t *testing.T) {
	cases := map[string]string{
		`K\195\131\194\188che`: "Küche",         // escaped AND double-encoded
		`K\195\188che`:         "Küche",         // escaped only
		"KÃ¼che":               "Küche",         // double-encoded only
		"Küche":                "Küche",         // correct: unchanged
		"Café":                 "Café",          // é alone is not a UTF-8 sequence
		`caf\233`:              "caf\xe9",       // lone Latin-1 byte kept for the widening downstream
		"Wohnzimmer 2":         "Wohnzimmer 2",  // ASCII
		"Kühlschrank Ω":        "Kühlschrank Ω", // a rune above 0xFF: never folded
	}
	for in, want := range cases {
		if got := decodeMDNSText(in); got != want {
			t.Errorf("decodeMDNSText(%q) = %q, want %q", in, got, want)
		}
	}
}
