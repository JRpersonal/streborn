package radiobrowser

import "testing"

func TestNeverChecked(t *testing.T) {
	cases := map[string]bool{
		"":                    true,  // radio-browser omits the field for a fresh station
		"   ":                 true,  // whitespace only
		"0000-00-00 00:00:00": true,  // some mirrors report an all-zero timestamp
		"2026-07-01 09:44:00": false, // genuinely checked
	}
	for in, want := range cases {
		if got := neverChecked(Station{LastCheckTime: in}); got != want {
			t.Errorf("neverChecked(%q) = %v, want %v", in, got, want)
		}
	}
}

// keepReachableOrUnchecked is the client-side stand-in for hidebroken=true that
// does NOT hide a just-added (never-checked) station: it drops only the stations
// radio-browser checked and found broken, keeping reachable and never-checked
// ones, in order (#252).
func TestKeepReachableOrUnchecked(t *testing.T) {
	in := []Station{
		{Name: "Reachable", LastCheckOK: 1, LastCheckTime: "2026-07-01 09:44:00"},
		{Name: "JustAddedByUser", LastCheckOK: 0, LastCheckTime: ""},           // never checked -> keep
		{Name: "CheckedButBroken", LastCheckOK: 0, LastCheckTime: "2026-06-30 12:00:00"}, // dead -> drop
		{Name: "ZeroTime", LastCheckOK: 0, LastCheckTime: "0000-00-00 00:00:00"}, // never checked -> keep
	}
	out := keepReachableOrUnchecked(in)
	got := make([]string, 0, len(out))
	for _, s := range out {
		got = append(got, s.Name)
	}
	want := []string{"Reachable", "JustAddedByUser", "ZeroTime"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order/content mismatch: got %v, want %v", got, want)
		}
	}
}
