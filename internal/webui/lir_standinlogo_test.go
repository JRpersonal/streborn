package webui

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// locWith builds a station location carrying a given imageUrl, the way an agent
// of any vintage would have written it.
func locWith(imageURL string) string {
	payload, _ := json.Marshal(map[string]any{
		"streamUrl": "http://box/stream/1", "name": "Sunshine Live",
		"imageUrl": imageURL, "streamType": "liveRadio", "isRealtime": true,
	})
	return "/station?data=" + base64.RawURLEncoding.EncodeToString(payload)
}

// The repair has to recognise exactly one thing: a slot already carrying STR's
// stand-in logo. Anything wider would rewrite presets for harmless differences,
// and every rewrite wakes the speaker and restarts its standby countdown.
func TestStationLocationCarriesStandInLogo(t *testing.T) {
	cases := []struct {
		name string
		loc  string
		want bool
	}{
		{"our stand-in, the slot to repair", locWith("http://127.0.0.1:8888" + strLogoPath), true},
		{"stand-in on some other authority", locWith("http://192.0.2.9:17008" + strLogoPath), true},
		{"a real logo through the proxy", locWith("http://127.0.0.1:8888/art?u=aHR0cHM6Ly94L2xvZ28ucG5n"), false},
		{"a plain http logo passed straight through", locWith("http://cdn.example/logo.png"), false},
		{"no image at all", locWith(""), false},

		// Anything unreadable must be left alone rather than guessed at: a
		// false positive here is a needless write to a sleeping speaker.
		{"not a station location", "/upnp/whatever", false},
		{"station location with unreadable payload", "/station?data=@@@not-base64@@@", false},
		{"station location with non-JSON payload", "/station?data=" + base64.RawURLEncoding.EncodeToString([]byte("nonsense")), false},
		{"empty", "", false},
	}
	for _, c := range cases {
		if got := StationLocationCarriesStandInLogo(c.loc); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// The freshly built location for a station that HAS a logo must not itself look
// like a slot needing repair, or the reconcile would rewrite the same slot on
// every boot and hold the speaker out of deep standby.
func TestRepairConvergesInsteadOfLooping(t *testing.T) {
	before := locWith("http://127.0.0.1:8888" + strLogoPath)
	after := OrionStationLocation("http://box/stream/1", "Sunshine Live",
		"https://icons.duckduckgo.com/ip3/www.sunshine-live.de.ico")

	if !StationLocationCarriesStandInLogo(before) {
		t.Fatal("the slot written by the old agent is not recognised as needing repair")
	}
	if StationLocationCarriesStandInLogo(after) {
		t.Fatal("the repaired slot still looks like it needs repair: this would rewrite on every boot")
	}
	if !strings.Contains(after, "data=") {
		t.Error("the rewritten location is not a station payload")
	}

	// A station that genuinely has no logo keeps the stand-in for ever, and so
	// keeps matching the first half of the condition. It must never be
	// rewritten, which is why the reconcile also requires the FRESH location to
	// have a real picture. Both halves, as the reconcile evaluates them:
	none := OrionStationLocation("http://box/stream/2", "1LIVE", "")
	repair := func(onBox, fresh string) bool {
		return StationLocationCarriesStandInLogo(onBox) && !StationLocationCarriesStandInLogo(fresh)
	}
	if repair(none, none) {
		t.Error("a station that simply has no logo would be rewritten on every boot")
	}
	if !repair(before, after) {
		t.Error("the slot that lost its logo would not be repaired")
	}
	if repair(after, after) {
		t.Error("an already-repaired slot would be rewritten again")
	}
}
