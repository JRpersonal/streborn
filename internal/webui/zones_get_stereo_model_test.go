package webui

import "testing"

// Only a SoundTouch 10 can be half of a Bose stereo pair, so only there is the
// /getGroup read worth its cost; on the scm/BCO chassis it hangs BoseApp's web
// server instead (2026-11-15). An unknown model keeps the read.
func TestOnlyASoundTouch10IsAskedForItsStereoPair(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"":                                 true,
		"SoundTouch 10":                    true,
		"soundtouch 10":                    true,
		"SoundTouch 20":                    false,
		"SoundTouch 30":                    false,
		"SoundTouch 300":                   false,
		"SoundTouch Portable":              false,
		"SoundTouch Wireless Link Adapter": false,
		"Wave SoundTouch music system IV":  false,
	}
	for model, want := range cases {
		if got := ModelMayHoldStereoPair(model); got != want {
			t.Errorf("%q: %v, want %v", model, got, want)
		}
	}
	s := &Server{}
	if !s.mayHoldStereoPair() {
		t.Error("a speaker whose model is not known yet must still be asked")
	}
	s.boxNameFn = func() (string, string) { return "Kitchen", "SoundTouch 20" }
	if s.mayHoldStereoPair() {
		t.Error("a SoundTouch 20 was asked for its stereo pair")
	}
}
