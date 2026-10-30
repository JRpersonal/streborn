package oled

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Renders the track screen's states with real-looking text to PNG, at panel
// size and three times enlarged, for the documentation and for looking at a
// layout change before it reaches a speaker. Runs only with
// STR_DISPLAY_PNG_DIR set; a relative directory is taken from this package's
// directory, as go test runs there:
//
//	STR_DISPLAY_PNG_DIR=$PWD/docs/images/display/v2 go test ./internal/oled -run DisplayPNG
func TestRenderDisplayPNG(t *testing.T) {
	dir := os.Getenv("STR_DISPLAY_PNG_DIR")
	if dir == "" {
		t.Skip("STR_DISPLAY_PNG_DIR not set")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 20, 15, 42, 0, 0, time.Local)
	short := TrackText{Station: "WDR 2", Artist: "Herbert Grönemeyer", Title: "Männer"}
	long := TrackText{Station: "FIP", Artist: "Ibrahim Maalouf", Title: "Beirut (Live à l'Olympia)"}
	withClock := func(t TrackText) TrackText { t.Clock = true; return t }
	playing := trackAnim{playing: true, frame: 3}
	for _, c := range []struct {
		name string
		t    TrackText
		a    trackAnim
	}{
		{"track-clock", withClock(short), playing},
		{"track-only", short, playing},
		{"clock-only", TrackText{Station: "WDR 2", Clock: true}, playing},
		{"paused", withClock(short), trackAnim{}},
		{"long-track-clock", withClock(long), playing},
		{"long-track-only", long, playing},
	} {
		buf := make([]byte, FrameSize)
		renderTrack(c.t.sanitized(), at, c.a, 0, 0, 0, buf)
		writePNG(t, filepath.Join(dir, "display-"+c.name+".png"), buf, 1)
		writePNG(t, filepath.Join(dir, "display-"+c.name+"@3x.png"), buf, 3)
	}
	// The notice a preset key shows over the track screen, in the track
	// screen's own faces so the hand-over does not change the lettering
	// (discussion #1305): one large line, a name with a word too wide for
	// the large face (so in the small one), and a name cut at three lines.
	for name, n := range map[string]Notice{
		"preset-notice-short": PresetPlaying(1, "WDR 2"),
		"preset-notice":       PresetPlaying(2, "Deutschlandfunk Kultur"),
		"preset-notice-cut":   PresetPlaying(4, "Absolut relax - Entspannungsmusik und mehr für den ganzen Tag"),
	} {
		r := newNoticeRenderer(n)
		writePNG(t, filepath.Join(dir, "display-"+name+".png"), r.full, 1)
		writePNG(t, filepath.Join(dir, "display-"+name+"@3x.png"), r.full, 3)
	}
}

// writePNG writes frame enlarged k times, each panel pixel a k x k block,
// its level 0..15 spread over the grey range.
func writePNG(t *testing.T, path string, frame []byte, k int) {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, Width*k, Height*k))
	for y := 0; y < Height*k; y++ {
		for x := 0; x < Width*k; x++ {
			img.SetGray(x, y, color.Gray{Y: min(frame[(y/k)*Width+x/k], 15) * 0x11})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Renders the sleep timer's states: the notices a key press shows and the
// track screen's top line with the countdown, with and without the time, a
// long station and a 12-hour clock. Same switch as above:
//
//	STR_DISPLAY_PNG_DIR=$PWD/docs/images/display/sleep go test ./internal/oled -run SleepDisplayPNG
func TestRenderSleepDisplayPNG(t *testing.T) {
	dir := os.Getenv("STR_DISPLAY_PNG_DIR")
	if dir == "" {
		t.Skip("STR_DISPLAY_PNG_DIR not set")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 20, 15, 42, 0, 0, time.Local)
	short := TrackText{Station: "WDR 2", Artist: "Herbert Grönemeyer", Title: "Männer"}
	long := TrackText{Station: "Deutschlandfunk Kultur", Artist: "Ibrahim Maalouf", Title: "Beirut (Live à l'Olympia)"}
	withClock := func(t TrackText, h12 bool) TrackText { t.Clock, t.Hour12 = true, h12; return t }
	playing := trackAnim{playing: true, frame: 3}
	for _, c := range []struct {
		name string
		t    TrackText
		min  int
	}{
		{"track-clock-23", withClock(short, false), 23},
		{"track-clock12-60", withClock(short, true), 60},
		{"track-only-7", short, 7},
		{"long-station-clock12-15", withClock(long, true), 15},
		{"long-station-only-30", long, 30},
		{"clock-only-45", TrackText{Station: "WDR 2", Clock: true}, 45},
	} {
		tt := c.t.sanitized()
		tt.sleepMin = c.min
		buf := make([]byte, FrameSize)
		renderTrack(tt, at, playing, 0, 0, 0, buf)
		writePNG(t, filepath.Join(dir, "sleep-"+c.name+".png"), buf, 1)
		writePNG(t, filepath.Join(dir, "sleep-"+c.name+"@3x.png"), buf, 3)
	}
	for name, n := range map[string]Notice{"notice-30": SleepSet(30), "notice-off": SleepOff()} {
		r := newNoticeRenderer(n)
		writePNG(t, filepath.Join(dir, "sleep-"+name+".png"), r.full, 1)
		writePNG(t, filepath.Join(dir, "sleep-"+name+"@3x.png"), r.full, 3)
	}
}

// Renders the examples the desktop app shows next to the display settings:
// each "Show" choice with and without the time, and the time alone. The
// TrackText per choice is the one webui.panelText builds for it. Same switch:
//
//	STR_DISPLAY_PNG_DIR=$PWD/desktop-app/frontend/src/assets/images/display go test ./internal/oled -run SettingsPreviewPNG
func TestRenderSettingsPreviewPNG(t *testing.T) {
	dir := os.Getenv("STR_DISPLAY_PNG_DIR")
	if dir == "" {
		t.Skip("STR_DISPLAY_PNG_DIR not set")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 20, 15, 42, 0, 0, time.Local)
	playing := trackAnim{playing: true, frame: 3}
	modes := map[string]TrackText{
		"both":   {Station: "WDR 2", Artist: "Herbert Grönemeyer", Title: "Männer"},
		"artist": {Station: "WDR 2", Title: "Herbert Grönemeyer"},
		"title":  {Station: "WDR 2", Title: "Männer"},
	}
	render := func(name string, tt TrackText) {
		buf := make([]byte, FrameSize)
		renderTrack(tt.sanitized(), at, playing, 0, 0, 0, buf)
		writePNG(t, filepath.Join(dir, "preview-"+name+".png"), buf, 2)
	}
	for mode, tt := range modes {
		render(mode, tt)
		tt.Clock = true
		render(mode+"-clock", tt)
	}
	render("clock", TrackText{Station: "WDR 2", Clock: true})
}
