package speakername

import "testing"

func TestSanitise(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Kitchen", "Kitchen"},
		{"Living Room", "Living Room"},
		{"Küche", "Küche"},
		{"Test&B", "Test+B"},
		{"Tom & Jerry", "Tom + Jerry"},
		{"<Office>", "Office"},
		{"a<b>c", "abc"},
		{"K\xfcche", "Küche"},
		{"Bad\tRoom\n", "BadRoom"},
		{"  Den  ", "Den"},
		{`"Quoted" O'Brien`, `"Quoted" O'Brien`},
		{"", ""},
		{"<>", ""},
	}
	for _, c := range cases {
		if got := Sanitise(c.in); got != c.want {
			t.Errorf("Sanitise(%q) = %q, want %q", c.in, got, c.want)
		}
		if got := Sanitise(c.in); Unsafe(got) {
			t.Errorf("Sanitise(%q) = %q is still unsafe", c.in, got)
		}
	}
}

func TestUnsafe(t *testing.T) {
	for _, n := range []string{"Test&B", "a<b", "a>b", "K\xfcche", "a\x01b"} {
		if !Unsafe(n) {
			t.Errorf("Unsafe(%q) = false, want true", n)
		}
	}
	for _, n := range []string{"Kitchen", "Küche", "Tom + Jerry", `"Den"`, "", "日本"} {
		if Unsafe(n) {
			t.Errorf("Unsafe(%q) = true, want false", n)
		}
	}
}

// Sanitise is idempotent, so a rename of an already cleaned name changes
// nothing and the repair can be run twice without drifting.
func TestSanitiseIdempotent(t *testing.T) {
	for _, n := range []string{"Test&B", "K\xfcche <x>", "a & b & c"} {
		once := Sanitise(n)
		if twice := Sanitise(once); twice != once {
			t.Errorf("Sanitise not idempotent for %q: %q then %q", n, once, twice)
		}
	}
}
