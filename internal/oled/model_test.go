package oled

import "testing"

// The Wave's firmware reports the Portable's framebuffer, but its front is a
// segment display (#1305): the model name, or before it is known the lisa
// chassis, must keep STR from drawing there.
func TestPanelAllowedWave(t *testing.T) {
	cases := []struct {
		model, variant string
		want           bool
	}{
		{"Wave SoundTouch music system IV", "lisa", false},
		{"Wave SoundTouch", "", false},
		{"", "lisa", false},
		{"SoundTouch", "lisa", false}, // the agent's generic fallback is not a model
		{"", "taigan", true},
		{"", "", true},
		{"SoundTouch Portable", "taigan", true},
		{"SoundTouch 20", "spotty", true},
		{"SoundTouch 30", "mojo", true},
		// a known model that is not a Wave is judged by the framebuffer alone
		{"SoundTouch SA-4", "lisa", true},
	}
	for _, c := range cases {
		if got := panelAllowed(c.model, c.variant); got != c.want {
			t.Errorf("panelAllowed(%q, %q) = %v, want %v", c.model, c.variant, got, c.want)
		}
	}
}

func TestSetModelFeedsPanelGate(t *testing.T) {
	t.Cleanup(func() { SetModel("") })
	SetModel("  Wave SoundTouch music system IV ")
	if currentModel() != "Wave SoundTouch music system IV" {
		t.Fatalf("model not trimmed: %q", currentModel())
	}
	if panelAllowed(currentModel(), "") {
		t.Fatal("a Wave must not get the panel")
	}
	if !ModelWithoutPanel("WAVE SOUNDTOUCH") || ModelWithoutPanel("SoundTouch 20") {
		t.Fatal("ModelWithoutPanel must match the Wave only")
	}
}
