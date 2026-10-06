package oled

import (
	"math"
	"math/rand/v2"
)

// Panel geometry of the SoundTouch Portable's OLED (ssdspi, 8 bpp, 16 grey
// levels). Frames handed around in this package hold grey levels 0..15, one
// byte per pixel; the panel code scales them to the 0x00..0xFF the driver
// expects.
const (
	Width     = 128
	Height    = 100
	FrameSize = Width * Height
)

// font is a plain 5x7 dot-matrix alphabet drawn for this project, covering
// exactly the letters the splash texts and the games use.
var font = map[rune][7]string{
	'S': {"01111", "10000", "10000", "01110", "00001", "00001", "11110"},
	'T': {"11111", "00100", "00100", "00100", "00100", "00100", "00100"},
	'R': {"11110", "10001", "10001", "11110", "10100", "10010", "10001"},
	'E': {"11111", "10000", "10000", "11110", "10000", "10000", "11111"},
	'B': {"11110", "10001", "10001", "11110", "10001", "10001", "11110"},
	'O': {"01110", "10001", "10001", "10001", "10001", "10001", "01110"},
	'N': {"10001", "11001", "10101", "10011", "10001", "10001", "10001"},
	'I': {"01110", "00100", "00100", "00100", "00100", "00100", "01110"},
	'L': {"10000", "10000", "10000", "10000", "10000", "10000", "11111"},
	'A': {"01110", "10001", "10001", "11111", "10001", "10001", "10001"},
	'D': {"11110", "10001", "10001", "10001", "10001", "10001", "11110"},
	'U': {"10001", "10001", "10001", "10001", "10001", "10001", "01110"},
	'P': {"11110", "10001", "10001", "11110", "10000", "10000", "10000"},
	'G': {"01111", "10000", "10000", "10011", "10001", "10001", "01111"},
	'M': {"10001", "11011", "10101", "10101", "10001", "10001", "10001"},
	'V': {"10001", "10001", "10001", "10001", "10001", "01010", "00100"},
	'F': {"11111", "10000", "10000", "11110", "10000", "10000", "10000"},
	'K': {"10001", "10010", "10100", "11000", "10100", "10010", "10001"},
	'C': {"01110", "10001", "10000", "10000", "10000", "10001", "01110"},
	'H': {"10001", "10001", "10001", "11111", "10001", "10001", "10001"},
	'W': {"10001", "10001", "10001", "10101", "10101", "11011", "10001"},
	'X': {"10001", "10001", "01010", "00100", "01010", "10001", "10001"},
	'Y': {"10001", "10001", "01010", "00100", "00100", "00100", "00100"},
}

// Logo animates the STR mark: the dots of the big letters fly in from outside
// the panel and settle, the subtitle types in under it, a highlight band
// sweeps across, and the finale zooms the whole picture towards the viewer
// until it is gone. With Hold set it never zooms out and instead shows a
// running bar (for "UPDATING", which ends with the reboot).
type Logo struct {
	Subtitle string
	Total    float64 // seconds; the zoom-out fills the last 1.6 s
	Hold     bool    // stay assembled with a running bar until Exit is called

	dots    []logoDot
	exitAt  float64 // set by Exit: start of the zoom-out in Hold mode
	exiting bool
}

type logoDot struct {
	tx, ty, sx, sy, delay float64
}

const (
	logoPitch = 5 // one font dot = 4x4 block on a 5 px grid
	logoBlock = 4
	logoY     = 14
	subY      = 68
	subScale  = 2
	zoomLen   = 1.6
)

// NewLogo lays out the mark; seed only varies where the dots fly in from.
func NewLogo(subtitle string, total float64, seed uint64) *Logo {
	r := rand.New(rand.NewPCG(seed, 0x5354520a))
	l := &Logo{Subtitle: subtitle, Total: total}
	width := 3*5*logoPitch + 2*2*logoPitch - 1
	x0 := (Width - width) / 2
	for li, ch := range "STR" {
		for gy, row := range font[ch] {
			for gx, b := range row {
				if b != '1' {
					continue
				}
				a := r.Float64() * 2 * math.Pi
				l.dots = append(l.dots, logoDot{
					tx:    float64(x0 + li*7*logoPitch + gx*logoPitch),
					ty:    float64(logoY + gy*logoPitch),
					sx:    64 + math.Cos(a)*110,
					sy:    50 + math.Sin(a)*110,
					delay: float64(li)*0.25 + r.Float64()*0.6,
				})
			}
		}
	}
	return l
}

// Exit starts the zoom-out of a Hold logo at time t; Done reports true once
// it has finished.
func (l *Logo) Exit(t float64) {
	if !l.exiting {
		l.exiting = true
		l.exitAt = t
	}
}

// Done reports whether the animation has nothing left to show at time t.
func (l *Logo) Done(t float64) bool {
	if l.Hold {
		return l.exiting && t >= l.exitAt+zoomLen
	}
	return t >= l.Total
}

// Frame renders time t (seconds since start) into buf (grey levels 0..15).
func (l *Logo) Frame(t float64, buf []byte) {
	clear(buf)
	zoomStart := l.Total - zoomLen
	if l.Hold {
		zoomStart = math.Inf(1)
		if l.exiting {
			zoomStart = l.exitAt
		}
	}
	z, fade := 1.0, 1.0
	if t > zoomStart {
		u := math.Min((t-zoomStart)/(zoomLen-0.2), 1)
		z = math.Pow(28, u*u)
		fade = 1 - u*u*u
	}
	const cx, cy = 64.0, 49.0
	rect := func(x, y, w, h float64, lvl int) {
		if z != 1 {
			x, y = cx+(x-cx)*z, cy+(y-cy)*z
			w, h = math.Ceil(w*z), math.Ceil(h*z)
			lvl = int(float64(lvl)*fade + 0.5)
		}
		fillRect(buf, int(x), int(y), int(w), int(h), lvl)
	}
	settled := 2.2
	for _, d := range l.dots {
		k := math.Min((t-d.delay)/1.2, 1)
		if k < 0 {
			continue
		}
		ik := 1 - k
		e := 1 - ik*ik*ik // ease out
		x := d.sx + (d.tx-d.sx)*e
		y := d.sy + (d.ty-d.sy)*e
		lvl := 4 + int(7*e)
		if t > settled && t < zoomStart {
			// a diagonal highlight band sweeps across every 2.5 s
			band := math.Mod((t-settled)*70, 175) - 30
			if dd := math.Abs(x + y*0.6 - band); dd < 8 {
				lvl = 15 - int(dd/2)
			}
		}
		rect(x, y, logoBlock, logoBlock, lvl)
	}
	// subtitle types in behind a block cursor
	typeAt := 1.6
	if t > typeAt {
		n := min(int((t-typeAt)/0.12), len(l.Subtitle))
		sx := float64((Width - (len(l.Subtitle)*6*subScale - subScale)) / 2)
		for ci, ch := range l.Subtitle[:n] {
			for gy, row := range font[ch] {
				for gx, b := range row {
					if b == '1' {
						rect(sx+float64(ci*6*subScale+gx*subScale), float64(subY+gy*subScale), subScale, subScale, 9)
					}
				}
			}
		}
		if t < zoomStart && n < len(l.Subtitle) {
			fillRect(buf, int(sx)+n*12, subY, 10, 14, 5)
		}
	}
	// running bar for Hold mode: a short block gliding back and forth
	if l.Hold && t > settled && t < zoomStart {
		const by, bw = 92, 24
		fillRect(buf, 8, by+1, Width-16, 1, 2)
		ph := math.Mod(t*0.8, 2)
		if ph > 1 {
			ph = 2 - ph
		}
		ph = ph * ph * (3 - 2*ph) // smoothstep at the ends
		x := 8 + int(ph*float64(Width-16-bw))
		fillRect(buf, x, by, bw, 3, 11)
	}
}

func fillRect(buf []byte, x0, y0, w, h, lvl int) {
	if lvl <= 0 {
		return
	}
	if lvl > 15 {
		lvl = 15
	}
	for y := max(y0, 0); y < min(y0+h, Height); y++ {
		for x := max(x0, 0); x < min(x0+w, Width); x++ {
			buf[y*Width+x] = byte(lvl)
		}
	}
}
