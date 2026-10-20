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
