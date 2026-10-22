package oled

import (
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

// The SoundTouch Wave reports the same 128x100 ssdspi framebuffer as the
// Portable, but its front is a segment LED display (fixed segments for the
// time and a few fixed words): nothing drawn into /dev/fb0 is ever visible
// (maintainer's own unit, user photo in discussion #1305). So the framebuffer
// check alone is not enough, and the model name overrides it.
//
// The model comes from the firmware's /info answer, which the agent learns
// some seconds after it starts (SetModel). Before that, the chassis codename
// decides: /proc/variant "lisa" is the Wave's chassis (shared with the SA-4
// and the CineMate consoles, none of which carries the pixel panel either),
// so a lisa box draws nothing until its model is known. A known model that is
// not a Wave is judged by the framebuffer as before.

// boxModel is the Bose model name from /info ("Wave SoundTouch music system
// IV", "SoundTouch 20", ...), "" until the agent learned it.
var boxModel atomic.Value // string

// SetModel records the speaker's model name from /info. The generic fallback
// "SoundTouch" the agent announces before /info answers counts as unknown.
func SetModel(model string) { boxModel.Store(strings.TrimSpace(model)) }

func currentModel() string {
	m, _ := boxModel.Load().(string)
	return m
}

// ModelWithoutPanel reports whether model names a speaker whose firmware
// claims a pixel panel it does not have (the Wave and its segment display).
func ModelWithoutPanel(model string) bool {
	return strings.Contains(strings.ToLower(model), "wave")
}

// variantWithoutPanel is the chassis codename used while the model is still
// unknown: "lisa" carries the Wave.
func variantWithoutPanel(variant string) bool {
	return strings.EqualFold(strings.TrimSpace(variant), "lisa")
}

// chassisVariant is /proc/variant, read once ("lisa", "taigan", ...; "" off
// the speaker).
var chassisVariant = sync.OnceValue(func() string {
	b, _ := os.ReadFile("/proc/variant")
	return strings.ToLower(strings.TrimSpace(string(b)))
})

// PhantomPanel reports whether this speaker is one whose firmware claims a
// pixel panel that nobody can see (the Wave), judged by the model once known
// and by the chassis before that. Nothing is drawn there, and settings that
// only act on that panel or on the now-playing title are not offered.
func PhantomPanel() bool { return !panelAllowed(currentModel(), chassisVariant()) }

// panelAllowed decides, from the model name and the chassis codename, whether
// a framebuffer that passes the geometry check is a panel anybody can see.
func panelAllowed(model, variant string) bool {
	m := strings.TrimSpace(model)
	if m != "" && !strings.EqualFold(m, "SoundTouch") {
		return !ModelWithoutPanel(m)
	}
	return !variantWithoutPanel(variant)
}
