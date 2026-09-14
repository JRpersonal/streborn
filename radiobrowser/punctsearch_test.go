package radiobrowser

import "testing"

// A station whose own name carries punctuation was unfindable by typing it the
// way a person says it: radio-browser's byname is a plain substring match, so
// "Mi Soul" returned nothing while "Mi-Soul" returned the station. Reported by
// a user with that exact example, and confirmed against the live API.
func TestNormalizeNameIgnoresPunctuation(t *testing.T) {
	same := [][2]string{
		{"Mi Soul", "Mi-Soul"},
		{"Mi Soul", "Mi-Soul Radio"}, // substring after normalising
		{"Mi Soul", "mi.soul"},
		{"1 Live", "1LIVE"},
		{"Radio Paradise", "Radio  Paradise "},
	}
	for _, c := range same {
		q, n := normalizeName(c[0]), normalizeName(c[1])
		if q == "" || !contains(n, q) {
			t.Errorf("%q should find %q, normalised %q vs %q", c[0], c[1], q, n)
		}
	}
}

// The whole-query comparison is what keeps it tight. "Missoula" contains both
// "mi" and "soul" as separate pieces, so a per-token filter would drag public
// radio from Montana into a search for a London soul station.
func TestNormalizeNameStaysTight(t *testing.T) {
	q := normalizeName("Mi Soul")
	for _, other := range []string{
		`KUFM 89.1 "Montana Public Radio" Missoula, MT`,
		"MixStream Radio - Soul",
		"Soul Mi Radio", // right words, wrong order: not a substring
	} {
		if contains(normalizeName(other), q) {
			t.Errorf("%q must not match a search for \"Mi Soul\" (normalised %q)", other, normalizeName(other))
		}
	}
}

func TestNormalizeNameEdges(t *testing.T) {
	if normalizeName("") != "" {
		t.Error("empty stays empty")
	}
	if normalizeName("---") != "" {
		t.Error("punctuation only collapses to empty")
	}
	// Digits carry meaning in station names and must survive.
	if got := normalizeName("SWR 3"); got != "swr3" {
		t.Errorf("normalizeName(\"SWR 3\") = %q, want \"swr3\"", got)
	}
	// Non-ASCII letters are letters.
	if got := normalizeName("Österreich 1"); got != "österreich1" {
		t.Errorf("normalizeName dropped a non-ASCII letter: %q", got)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
