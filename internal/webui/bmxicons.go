package webui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"strings"
	"sync"
)

// Icons for the emulated radio service.
//
// The BMX service registry STR serves points the speaker at a handful of icon
// assets for the radio service (see internal/marge/bmxservices.go, the
// {MEDIA_SERVER}/bmx-icons/... entries). Nothing served them, so every one of
// those paths fell through to the web UI's catchall and answered 200 with an
// HTML page. The speaker asked for an icon and got a web page.
//
// That is why a native radio preset showed nothing where a UPnP preset used to
// show the generic UPnP source icon, which users understandably read as the
// station's own logo disappearing (#510). Confirmed on a SoundTouch 30: after a
// native preset press the speaker requests
// /media/bmx-icons/orion/monochrome.svg, and it renders what it gets.
//
// What it gets is the STR mark: if a stand-in for the station logo is shown at
// all, it should be ours rather than an anonymous glyph.

const bmxIconPrefix = "/media/bmx-icons/"

var (
	bmxIconOnce sync.Once
	bmxIconPNG  []byte

	bmxIconLogMu  sync.Mutex
	bmxIconLogged = map[string]bool{}
)

// handleBMXIcon serves the radio service's icons. Anything under the prefix
// answers an image, by extension, so a path we did not anticipate still gets a
// picture rather than an HTML page.
func (s *Server) handleBMXIcon(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	// Log the first request per path: this is the evidence for whether the
	// firmware fetches these at all, and it must not become a log storm if the
	// speaker polls them.
	bmxIconLogMu.Lock()
	first := !bmxIconLogged[path]
	if first {
		bmxIconLogged[path] = true
	}
	bmxIconLogMu.Unlock()
	if first {
		s.logger.Info("bmx icon: the speaker fetched a radio service icon", "path", path)
	}

	// The mark is fixed in the binary, so it can be cached for as long as this
	// agent build runs. (It was no-store while the display layout was being
	// worked out: a cached icon makes every change look like it had no effect.
	// Keep no-store with the dev tools on, for the same reason.)
	if devToolsEnabled() {
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=86400")
	}
	// Diagnostic mode: serve a frame and a per-asset corner mark so it is
	// visible WHICH of the six assets the display actually shows and how much
	// of it survives. Enabled with the dev tools, off for users.
	if devToolsEnabled() {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write([]byte(diagnosticIconSVG(path)))
		return
	}
	if strings.HasSuffix(path, ".svg") {
		w.Header().Set("Content-Type", "image/svg+xml")
		// The "monochrome" assets are meant to be tinted by the firmware, so
		// that variant draws in currentColor instead of the brand colours.
		if strings.Contains(strings.ToLower(path), "monochrome") {
			_, _ = w.Write([]byte(strImageSVGMono))
			return
		}
		_, _ = w.Write([]byte(strLogoSVG))
		return
	}
	bmxIconOnce.Do(func() { bmxIconPNG = renderSTRLogoPNG() })
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(bmxIconPNG)
}

// strLogoSVG is the STR mark: six dots in two rows of three, the preset keys on
// a SoundTouch seen from above, over a heartbeat line. Kept in sync with
// desktop-app/frontend/public/logo.svg by hand; it is eleven shapes and has not
// changed since the project started.
//
// No width/height attributes on purpose: with them the speaker rendered the
// mark oversized, so it is left to scale into whatever box the firmware gives
// it (Jens on an ST30, 2026-08-04: "a bit too big").
const strLogoSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" fill="none" role="img" aria-label="ST Reborn">` +
	`<g fill="#000000">` +
	`<circle cx="22" cy="14" r="3"/><circle cx="32" cy="14" r="3"/><circle cx="42" cy="14" r="3"/>` +
	`<circle cx="22" cy="26" r="3"/><circle cx="32" cy="26" r="3"/><circle cx="42" cy="26" r="3"/>` +
	`</g>` +
	`<path d="M 4 44 L 22 44" stroke="#000000" stroke-width="3" stroke-linecap="round"/>` +
	`<path d="M 22 44 L 26 48 L 30 32 L 34 54 L 38 40 L 42 44" stroke="#cc0000" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"/>` +
	`<path d="M 42 44 L 60 44" stroke="#000000" stroke-width="3" stroke-linecap="round"/>` +
	`</svg>`

// strImageSVGMono is the same mark for the "monochrome" asset names.
//
// Explicit white, not currentColor. currentColor is only useful if the consumer
// sets a colour, and this firmware does not: it rendered the mark in the
// default black on a dark display, which read as "sehr sehr schwach" (ST30,
// 2026-08-04). These speaker displays are light-on-dark, so white is the
// legible choice.
//
// Also drawn heavier and smaller than the app logo: strokes at 3 units of 64
// nearly vanish once the firmware scales the mark into a small display slot, and
// the untouched mark filled too much of it. The group is scaled to 78% about the
// centre, which is padding rather than a redraw, so it stays the same logo.
const strImageSVGMono = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" fill="none" role="img" aria-label="ST Reborn">` +
	// 0.6, not a snugger fit: the firmware scales the mark to FILL its slot and
	// clips what overflows, and this mark is wide (x 4..60) but short
	// (y 10..54), so the sides are cut first and it reads as cropped even where
	// the display has room (ST30, 2026-08-04). The margin is what keeps it whole.
	`<g transform="translate(32 32) scale(0.6) translate(-32 -32)">` +
	`<g fill="#FFFFFF">` +
	`<circle cx="22" cy="14" r="4"/><circle cx="32" cy="14" r="4"/><circle cx="42" cy="14" r="4"/>` +
	`<circle cx="22" cy="26" r="4"/><circle cx="32" cy="26" r="4"/><circle cx="42" cy="26" r="4"/>` +
	`</g>` +
	`<path d="M 4 44 L 22 44 L 26 48 L 30 32 L 34 54 L 38 40 L 42 44 L 60 44" stroke="#FFFFFF" stroke-width="5" fill="none" stroke-linecap="round" stroke-linejoin="round"/>` +
	`</g></svg>`

// renderSTRLogoPNG draws the same mark for the registry entries that ask for a
// PNG. Drawn in code so the repo carries no binary blob and the raster cannot
// drift from the vector.
func renderSTRLogoPNG() []byte {
	// The firmware puts the image's TOP-LEFT corner at the centre of the
	// display and draws right and down from there, so only the top-left part of
	// whatever is sent is ever on screen (Portable, 2026-08-04: "das Logo
	// faengt in der Mitte des Displays mit der oberen linken Ecke an"). That
	// single fact explains the earlier confusion: a 256 px image showed just its
	// corner, and a 128x96 one showed almost nothing.
	//
	// Two consequences, and both matter:
	//   - the canvas must be SMALL, because only about a quarter of the display
	//     is reachable,
	//   - the mark must sit in the canvas's top-left, not centred in it, or it
	//     is pushed off the reachable area.
	// The canvas is DERIVED from the mark, not chosen. A square canvas left an
	// empty third under a mark that is wide and short, which bought nothing and
	// still got clipped; and every hand-picked size after that needed the
	// margins re-tuned. One scale knob, canvas computed to fit, so the only
	// thing left to tune is how big the mark is.
	const (
		x0, x1 = 4.0, 60.0 // the mark's authored box, on a 64 grid
		y0, y1 = 10.0, 54.0
		// The authored box is the CENTRELINE of the shapes: half a stroke and
		// the dot radius stick out beyond it on every side. Without room for
		// that the horizontal line ended up flat on the bottom edge with its
		// lower half cut ("die horizontale Linie liegt auf dem Boden",
		// Portable 2026-08-04).
		pad = 3.0
		// Sized by eye against a real display, one step at a time. 0.68 is the
		// largest that kept the heartbeat's lowest point clear of the cut.
		scale = 0.68
	)
	w := int(math.Ceil((x1-x0)*scale + 2*pad))
	h := int(math.Ceil((y1-y0)*scale + 2*pad))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// White on transparent: the display is light-on-dark and a dark mark
	// disappears into it (the earlier currentColor version rendered black and
	// read as "sehr sehr schwach").
	ink := color.RGBA{0xFF, 0xFF, 0xFF, 0xFF}
	red := color.RGBA{0xFF, 0x4D, 0x4D, 0xFF}
	atX := func(v float64) float64 { return pad + (v-x0)*scale }
	atY := func(v float64) float64 { return pad + (v-y0)*scale }
	// Stroke weights scale WITH the mark. Keeping radii at fixed pixel values
	// while the layout shrank is what made the dots look too fat and the
	// heartbeat line nearly vanish. Authored weights: dots r=3, strokes 3 wide.
	dotR := int(math.Round(3 * scale))
	if dotR < 2 {
		dotR = 2
	}
	penR := int(math.Round(1.5 * scale))
	if penR < 1 {
		penR = 1
	}

	disc := func(cx, cy, r int, col color.RGBA) {
		for y := -r; y <= r; y++ {
			for x := -r; x <= r; x++ {
				if x*x+y*y <= r*r {
					px, py := cx+x, cy+y
					if px >= 0 && py >= 0 && px < w && py < h {
						img.Set(px, py, col)
					}
				}
			}
		}
	}
	line := func(ax, ay, bx, by float64, col color.RGBA) {
		steps := int(math.Max(math.Abs(bx-ax), math.Abs(by-ay))) * 2
		if steps < 1 {
			steps = 1
		}
		for i := 0; i <= steps; i++ {
			t := float64(i) / float64(steps)
			disc(int(ax+(bx-ax)*t), int(ay+(by-ay)*t), penR, col)
		}
	}

	// Six preset dots, two rows of three.
	for _, cy := range []float64{14, 26} {
		for _, cx := range []float64{22, 32, 42} {
			disc(int(atX(cx)), int(atY(cy)), dotR, ink)
		}
	}
	// Baseline left and right, heartbeat in the middle.
	line(atX(4), atY(44), atX(22), atY(44), ink)
	line(atX(42), atY(44), atX(60), atY(44), ink)
	pts := [][2]float64{{22, 44}, {26, 48}, {30, 32}, {34, 54}, {38, 40}, {42, 44}}
	for i := 0; i+1 < len(pts); i++ {
		line(atX(pts[i][0]), atY(pts[i][1]), atX(pts[i+1][0]), atY(pts[i+1][1]), red)
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// diagnosticIconSVG draws a full-bleed frame plus a corner block that identifies
// which asset this is. It answers two questions a plain logo cannot: whether the
// firmware crops the image (the frame is cut) and WHICH of the six registry
// assets the display is actually showing (the corner block moves per asset).
//
// Only served when the development endpoints are enabled, so users never see it.
func diagnosticIconSVG(path string) string {
	// One distinct corner per asset, clockwise from top-left.
	corner := 0
	switch {
	case strings.Contains(path, "monochrome_v2"):
		corner = 1
	case strings.Contains(path, "monochromePng"):
		corner = 2
	case strings.Contains(path, "monochromeSvg"):
		corner = 3
	case strings.Contains(path, "smallSvg"):
		corner = 4
	case strings.Contains(path, "default-album-art"):
		corner = 5
	}
	cx, cy := 8, 8
	switch corner {
	case 1:
		cx, cy = 56, 8
	case 2:
		cx, cy = 56, 56
	case 3:
		cx, cy = 8, 56
	case 4:
		cx, cy = 32, 8
	case 5:
		cx, cy = 32, 56
	}
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" fill="none">` +
		`<rect x="1" y="1" width="62" height="62" fill="none" stroke="#FFFFFF" stroke-width="2"/>` +
		`<circle cx="` + itoaSmall(cx) + `" cy="` + itoaSmall(cy) + `" r="5" fill="#FFFFFF"/>` +
		`<path d="M 16 32 L 48 32" stroke="#FFFFFF" stroke-width="4" stroke-linecap="round"/>` +
		`</svg>`
}

func itoaSmall(v int) string {
	if v < 10 {
		return string(rune('0' + v))
	}
	return string(rune('0'+v/10)) + string(rune('0'+v%10))
}
