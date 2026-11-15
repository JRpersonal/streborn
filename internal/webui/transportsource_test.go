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

// Next/Prev on a source the speaker plays itself goes out as the remote's key
// (#1903, Bluetooth from a phone). Radio and the inputs have no next track and
// must not get one, and STR's own sources stay with the queue.
func TestBoxSkipKey(t *testing.T) {
	cases := []struct {
		src     string
		forward bool
		key     string
		ok      bool
	}{
		{"BLUETOOTH", true, "NEXT_TRACK", true},
		{"BLUETOOTH", false, "PREV_TRACK", true},
		{"AIRPLAY", true, "NEXT_TRACK", true},
		{"SPOTIFY", false, "PREV_TRACK", true},
		{"SOME_FUTURE_SOURCE", true, "NEXT_TRACK", true},
		{"LOCAL_INTERNET_RADIO", true, "", false},
		{"TUNEIN", true, "", false},
		{"AUX", false, "", false},
		{"LOCAL", true, "", false},
		{"PRODUCT", true, "", false},
		{"UPNP", true, "", false},
		{"", true, "", false},
		{"STANDBY", true, "", false},
	}
	for _, tc := range cases {
		key, ok := boxSkipKey(tc.src, tc.forward)
		if key != tc.key || ok != tc.ok {
			t.Errorf("boxSkipKey(%q, %v) = %q, %v, want %q, %v", tc.src, tc.forward, key, ok, tc.key, tc.ok)
		}
	}
}

// No box configured: the skip falls through to the queue path.
func TestTransportSkipKeyWithoutABox(t *testing.T) {
	s := &Server{}
	if s.transportSkipKey(t.Context(), true) {
		t.Error("claimed to have skipped with no box configured")
	}
}
