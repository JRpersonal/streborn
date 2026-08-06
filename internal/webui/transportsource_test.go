package webui

import (
	"testing"
)

// The allowlist decides who receives a Pause. Getting it wrong towards "the box
// owns it" costs one harmless remote key; getting it wrong the other way is the
// dead Pause button this whole file exists to fix, so the defaults matter.
func TestBoxOwnedSource(t *testing.T) {
	cases := []struct {
		src  string
		want bool
	}{
		// STR pushes the audio on these, so the UPnP renderer is the right
		// addressee and must keep the proven path.
		{"UPNP", false},
		{"", false},
		{"STANDBY", false},
		{"INVALID_SOURCE", false},
		// The speaker runs these itself.
		{"LOCAL_INTERNET_RADIO", true},
		{"SPOTIFY", true},
		{"BLUETOOTH", true},
		{"AUX", true},
		{"TUNEIN", true},
		// A source name no one has seen yet falls on the safe side.
		{"SOME_FUTURE_SOURCE", true},
	}
	for _, tc := range cases {
		if got := boxOwnedSource(tc.src); got != tc.want {
			t.Errorf("boxOwnedSource(%q) = %v, want %v", tc.src, got, tc.want)
		}
	}
}

// Without a box host there is nothing to send a key to, and the caller must
// fall through to its existing path rather than reporting success.
func TestTransportKeyFallbackWithoutABox(t *testing.T) {
	s := &Server{}
	if s.transportKeyFallback(t.Context(), "PAUSE") {
		t.Error("claimed to have handled the key with no box configured")
	}
}
