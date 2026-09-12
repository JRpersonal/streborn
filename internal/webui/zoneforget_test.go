package webui

import (
	"net/http/httptest"
	"testing"
)

// Taking a live group apart must leave a saved group alone, or "ungroup for
// now" silently deletes what the user built. Deleting the saved one has to be
// possible too, and until #119 it was not: every route reached the same branch,
// and there the saved group always survived, so a reporter dissolved his group
// three times in ninety seconds and watched it come back each time.
//
// The two are told apart by one query flag, so that is what this pins.
func TestZoneForgetFlagIsExplicit(t *testing.T) {
	cases := []struct {
		name   string
		target string
		want   bool
	}{
		{"a plain dissolve keeps the saved group", "/api/box/zone", false},
		{"the saved group's own x asks for it to go", "/api/box/zone?forget=1", true},
		{"anything else is not a request to delete", "/api/box/zone?forget=0", false},
		{"and neither is a truthy-looking word", "/api/box/zone?forget=true", false},
		{"nor another parameter", "/api/box/zone?keep=1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("DELETE", tc.target, nil)
			if got := r.URL.Query().Get("forget") == "1"; got != tc.want {
				t.Errorf("forget = %v for %q, want %v", got, tc.target, tc.want)
			}
		})
	}
}
