package region

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePrecedence(t *testing.T) {
	cases := []struct {
		str, box, want, src string
	}{
		{"", "", "", ""},
		{"us", "", "US", "str"},
		{"", "US", "US", "box"},
		{"DE", "US", "DE", "str"},
		{" ", "us ", "US", "box"},
		{"USA", "GB", "GB", "box"},
		{"1A", "", "", ""},
	}
	for _, c := range cases {
		got := Resolve(c.str, c.box)
		if got.Country != c.want || got.Source != c.src {
			t.Errorf("Resolve(%q,%q) = %+v, want %q from %q", c.str, c.box, got, c.want, c.src)
		}
	}
	if !Resolve("", "US").IsUS() || Resolve("CA", "US").IsUS() {
		t.Error("IsUS wrong")
	}
}

func TestResolverCachesBoxCountry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "box-country.txt")
	r := New(path, nil)
	if r.Country() != "" {
		t.Fatal("fresh resolver should be unknown")
	}
	r.SetBox("us")
	if r.Info().Source != "box" || r.Country() != "US" {
		t.Fatalf("after SetBox: %+v", r.Info())
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "US\n" {
		t.Fatalf("cache = %q, %v", b, err)
	}
	// A failed /info read (empty value) must not erase the known country.
	r.SetBox("")
	if r.Country() != "US" {
		t.Fatal("empty SetBox erased the country")
	}
	// A restart reads the cache before /info is available.
	if New(path, nil).Country() != "US" {
		t.Fatal("cache not read on start")
	}
	r.SetSTR("DE")
	if r.Info().Country != "DE" || r.Info().Box != "US" {
		t.Fatalf("STR region should win: %+v", r.Info())
	}
}

func TestNilResolver(t *testing.T) {
	var r *Resolver
	r.SetBox("US")
	r.SetSTR("US")
	if r.Country() != "" {
		t.Fatal("nil resolver reports a country")
	}
}
