package discovery

import "testing"

func TestStockServiceTypePrimary(t *testing.T) {
	if StockServiceType != "_soundtouch._tcp" {
		t.Fatalf("primary stock service type regressed: got %q, want _soundtouch._tcp", StockServiceType)
	}
}

func TestStockServiceTypeAliasesContainsBoseSpelling(t *testing.T) {
	found := false
	for _, a := range StockServiceTypeAliases {
		if a == "_bose-soundtouch._tcp" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected _bose-soundtouch._tcp in StockServiceTypeAliases, got %v", StockServiceTypeAliases)
	}
}

func TestStockModelLabel(t *testing.T) {
	cases := map[string]string{
		"soundtouch_10":       "SoundTouch 10",
		"SOUNDTOUCH_20":       "SoundTouch 20",
		"st30":                "SoundTouch 30",
		"soundtouch_portable": "SoundTouch Portable",
		"SoundTouch 10":       "SoundTouch 10", // already a label, passthrough
		"unknown_model":       "unknown_model",
	}
	for in, want := range cases {
		if got := stockModelLabel(in); got != want {
			t.Errorf("stockModelLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
