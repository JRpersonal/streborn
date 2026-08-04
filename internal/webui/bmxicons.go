package webui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Icons for the emulated radio service.
//
// The BMX service registry STR serves points the speaker at a handful of icon
// assets for the radio service (see internal/marge/bmxservices.go, the
// {MEDIA_SERVER}/bmx-icons/... entries). Nothing served them, so every one of
// those paths fell through to the web UI's catchall and answered 200 with an
// HTML page. The speaker asked for an icon and got a web page.
//
// That is why a native radio preset shows nothing where a UPnP preset used to
// show the generic UPnP source icon, which users understandably read as the
// station's own logo disappearing (reported on #510, confirmed on a SoundTouch
// 30 here: the UPnP placeholder is what was always displayed, never the station
// artwork).
//
// Whether this firmware actually fetches these assets is not documented
// anywhere, so every request is logged once per path. If the speaker never asks,
// the log stays empty and we know to stop looking here.

// bmxIconPrefix is the path the registry points at.
const bmxIconPrefix = "/media/bmx-icons/"

var (
	bmxIconOnce sync.Once
	bmxIconPNG  []byte

	bmxIconLogMu   sync.Mutex
	bmxIconLogged  = map[string]bool{}
	bmxIconLastLog time.Time
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
		bmxIconLastLog = time.Now()
	}
	bmxIconLogMu.Unlock()
	if first {
		s.logger.Info("bmx icon: the speaker fetched a radio service icon", "path", path)
	}

	w.Header().Set("Cache-Control", "public, max-age=86400")
	switch {
	case strings.HasSuffix(path, ".svg"):
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write([]byte(bmxIconSVG))
	default:
		bmxIconOnce.Do(func() { bmxIconPNG = renderBMXIconPNG() })
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(bmxIconPNG)
	}
}

// bmxIconSVG is a plain broadcast mark: a dot with two arcs. Monochrome and
// currentColor so the firmware can tint it, which is what the registry's
// "monochrome" asset names imply.
const bmxIconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round">` +
	`<circle cx="12" cy="12" r="2" fill="currentColor" stroke="none"/>` +
	`<path d="M7.8 7.8a6 6 0 0 0 0 8.4"/><path d="M16.2 16.2a6 6 0 0 0 0-8.4"/>` +
	`<path d="M4.9 4.9a10 10 0 0 0 0 14.2"/><path d="M19.1 19.1a10 10 0 0 0 0-14.2"/>` +
	`</svg>`

// renderBMXIconPNG draws the same broadcast mark as a small opaque PNG, for the
// registry entries that ask for one. Drawn rather than embedded so the repo
// carries no binary blob and the icon cannot drift from the SVG.
func renderBMXIconPNG() []byte {
	const size = 64
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	fg := color.RGBA{0xFF, 0xFF, 0xFF, 0xFF}
	c := float64(size) / 2

	plot := func(x, y int) {
		if x >= 0 && y >= 0 && x < size && y < size {
			img.Set(x, y, fg)
		}
	}
	// Centre dot.
	for y := -3; y <= 3; y++ {
		for x := -3; x <= 3; x++ {
			if x*x+y*y <= 9 {
				plot(int(c)+x, int(c)+y)
			}
		}
	}
	// Two pairs of arcs, left and right of the dot.
	for _, rad := range []float64{12, 20} {
		for deg := -55; deg <= 55; deg++ {
			rd := float64(deg) * 3.14159265 / 180
			dx, dy := rad*cosApprox(rd), rad*sinApprox(rd)
			for t := -1; t <= 1; t++ {
				plot(int(c+dx)+t, int(c+dy))
				plot(int(c-dx)+t, int(c+dy))
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// Small local trig so the icon needs no extra import and stays deterministic.
func cosApprox(x float64) float64 { return 1 - x*x/2 + x*x*x*x/24 - x*x*x*x*x*x/720 }
func sinApprox(x float64) float64 {
	return x - x*x*x/6 + x*x*x*x*x/120 - x*x*x*x*x*x*x/5040
}
