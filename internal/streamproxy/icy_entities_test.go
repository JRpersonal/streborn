package streamproxy

import (
	"io"
	"log/slog"
	"testing"
)

// A station that encodes its non-ASCII letters as HTML character references in
// the StreamTitle (Rádio pohádka, #1982) must have them decoded to the real
// characters, while a literal ampersand that opens no entity stays as it is.
func TestSetTitleDecodesHTMLEntities(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.setTitle("http://example.com/stream",
		"Macourek & Náro&#382;ný - O tom jak Mach a &#352;ebestová podstoupili ob&#283;&#357;")
	want := "Macourek & Nárožný - O tom jak Mach a Šebestová podstoupili oběť"
	if got := s.CurrentTitle(); got != want {
		t.Fatalf("entities not decoded:\n got %q\nwant %q", got, want)
	}
}

// Numeric decimal, numeric hex and named references all decode, and a title
// with no entity at all is left byte for byte.
func TestSetTitleEntityForms(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	cases := map[string]string{
		"Caf&#xe9; del Mar":  "Café del Mar",
		"AT&amp;T Radio":     "AT&T Radio",
		"Jack & Jill":        "Jack & Jill",
		"Nárožný already ok": "Nárožný already ok",
	}
	for in, want := range cases {
		s.setTitle("u", in)
		if got := s.CurrentTitle(); got != want {
			t.Fatalf("setTitle(%q): got %q want %q", in, got, want)
		}
	}
}
