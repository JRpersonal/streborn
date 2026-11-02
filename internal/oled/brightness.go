package oled

import "sync/atomic"

// The owner's clock brightness (#1226) on STR's own playback screen.
//
// The firmware's standby clock carries brightnessLevel 0 to 100, 70 out of the
// box. Where STR draws the playback screen itself, the same choice dims STR's
// pixels too, so the panel does not jump from a dimmed standby clock to a full
// bright track screen. 70 and above leave the screen as drawn (STR draws at the
// firmware's default look, and grey 15 cannot get brighter); below 70 every lit
// pixel is scaled by level/70 and kept at least at grey 1, so even 0 stays
// readable in a dark room instead of turning the screen off.
//
// Whether the firmware's own level also changes the panel's contrast for
// everything drawn on it is untested; if it does, STR's screen would be
// dimmed twice on such a speaker.

// panelFullLevel is the level at and above which STR draws unchanged.
const panelFullLevel = 70

// trackLevel is the brightness the track screen follows, -1 when none is known.
var trackLevel atomic.Int32

func init() { trackLevel.Store(-1) }

// SetTrackBrightness sets the level (0 to 100) STR's playback screen follows;
// a negative level means none. A visible screen is redrawn at once.
func SetTrackBrightness(level int) {
	if level > 100 {
		level = 100
	}
	if level < 0 {
		level = -1
	}
	if int(trackLevel.Swap(int32(level))) != level {
		tracks.poke()
	}
}

// TrackBrightness is the level STR's playback screen follows, -1 when none.
func TrackBrightness() int { return int(trackLevel.Load()) }

// dimFrame scales a frame of grey levels 0..15 in place for level.
func dimFrame(buf []byte, level int) {
	if level < 0 || level >= panelFullLevel {
		return
	}
	for i, v := range buf {
		if v == 0 {
			continue
		}
		d := (int(v)*level + panelFullLevel/2) / panelFullLevel
		buf[i] = byte(max(d, 1))
	}
}
