package oled

// Scrolling text on the track screen.
//
// A line that does not fit the panel at its normal size is not drawn smaller
// and not cut: it scrolls, as a marquee. The start stands for a moment so it
// can be read, the text then moves at an even pace until its end is in view,
// the end stands for a moment, and the line jumps back to the start. Every
// scrolling line on the screen runs on one shared cycle: they start together,
// each stops at its own end, and all jump back together once the longest one
// is through. So the screen stands completely still during both pauses, and
// the frames in between come only while something actually moves.
//
// The marquee needs no timer of its own: the panel hold already redraws when
// nextDraw says so, and nextDraw asks for the next scroll step only while a
// line is moving and the speaker plays. On pause, in standby, with the screen
// off or when everything fits, no scroll frame is drawn at all.

import "time"

const (
	// marqueeHoldStart is how long the start of a scrolling line stands.
	marqueeHoldStart = 2 * time.Second
	// marqueeHoldEnd is how long its end stands before the jump back.
	marqueeHoldEnd = 1500 * time.Millisecond
	// marqueeStep is the time between two scroll frames. Every step moves a
	// line by one font pixel (1 px at scale 1, 2 px at the large scale 2),
	// so both sizes run at the same pace of about 2.5 characters a second
	// and the large text never stands between its own pixels. 15 frames a
	// second read smooth on the Portable's panel.
	marqueeStep = 66 * time.Millisecond
)

// marqueeAt is the shared marquee cycle at elapsed time e since it started,
// for lines of which the longest needs maxSteps scroll steps to show its end.
// step is how far every line has moved (in steps; each line stops at its own
// end), next how long until step changes. maxSteps <= 0 means nothing
// scrolls: step 0, next 0 (no redraw needed).
func marqueeAt(e time.Duration, maxSteps int) (step int, next time.Duration) {
	if maxSteps <= 0 {
		return 0, 0
	}
	if e < 0 {
		e = 0
	}
	scroll := time.Duration(maxSteps) * marqueeStep
	period := marqueeHoldStart + scroll + marqueeHoldEnd
	t := e % period
	switch {
	case t < marqueeHoldStart:
		return 0, marqueeHoldStart - t
	case t < marqueeHoldStart+scroll-marqueeStep:
		in := t - marqueeHoldStart
		return int(in/marqueeStep) + 1, marqueeStep - in%marqueeStep
	default:
		// the last step is reached: stand until the jump back
		return maxSteps, period - t
	}
}

// marqueeLine is one line of text with the box it is drawn in. A text wider
// than the box scrolls in it; one that fits is centred in it.
type marqueeLine struct {
	s       string
	sc, lvl int
	x0, w   int // the box, unshifted
	y       int // top, unshifted
	// left keeps a fitting text at the box's left edge instead of centring it
	left bool
}

// textWidth is the width in pixels of s at font scale sc.
func textWidth(s string, sc int) int {
	if s == "" {
		return 0
	}
	return len(s)*6*sc - sc
}

// overflow is how many pixels the text is wider than its box (0: it fits).
func (m marqueeLine) overflow() int { return max(0, textWidth(m.s, m.sc)-m.w) }

// steps is how many marquee steps the line needs to show its end.
func (m marqueeLine) steps() int {
	ov := m.overflow()
	return (ov + m.sc - 1) / m.sc
}

// draw paints the line at marquee step (ignored when it fits), moved by dx,
// dy, clipped to its box.
func (m marqueeLine) draw(c canvas, step, dx, dy int) {
	if m.s == "" {
		return
	}
	x0 := m.x0 + dx
	x := x0
	if ov := m.overflow(); ov > 0 {
		x -= min(step*m.sc, ov)
	} else if !m.left {
		x += (m.w - textWidth(m.s, m.sc)) / 2
	}
	c.textClip(m.s, x, m.y+dy, m.sc, m.lvl, x0, x0+m.w)
}

// textClip paints s with its left edge at x and its top at y, only the
// columns from clipX0 up to (not including) clipX1. Characters wholly
// outside are skipped, so a long scrolling line costs only what shows.
func (c canvas) textClip(s string, x, y, sc, lvl, clipX0, clipX1 int) {
	clipX0, clipX1 = max(clipX0, 0), min(clipX1, Width)
	adv := 6 * sc
	for _, ch := range s {
		if x >= clipX1 {
			return
		}
		if x+adv > clipX0 {
			for gy, row := range font[ch] {
				for gx, b := range row {
					if b != '1' {
						continue
					}
					px0 := x + gx*sc
					for xx := max(px0, clipX0); xx < min(px0+sc, clipX1); xx++ {
						for yy := y + gy*sc; yy < y+gy*sc+sc; yy++ {
							c.px(xx, yy, lvl)
						}
					}
				}
			}
		}
		x += adv
	}
}

// trackCache keeps what does not move between two marquee frames: the
// layout, the frame with every standing line and the rule drawn, and each
// scrolling line drawn once into a strip of its own. A scroll frame is then
// a copy of the standing frame, the playing symbol and a window of each
// strip, instead of every glyph drawn again 15 times a second.
type trackCache struct {
	valid  bool
	t      TrackText
	hm     string
	dx, dy int
	f      trackFrame
	base   []byte
	strips map[int][]byte // line index -> strip, for the lines that scroll
}

// prepare makes the cache hold the track screen for t at now, shifted by
// dx, dy, rebuilding it only when the text, the time shown or the shift
// changed. Returns the layout.
func (tc *trackCache) prepare(t TrackText, now time.Time, dx, dy int) trackFrame {
	hm := ""
	if t.Clock {
		h, ap := clockText(now, t.Hour12)
		hm = h + ap
	}
	if !tc.valid || tc.t != t || tc.hm != hm || tc.dx != dx || tc.dy != dy {
		tc.build(t, now, dx, dy)
		tc.hm = hm
	}
	return tc.f
}

// render draws the prepared screen at marquee step with the playing symbol
// in state a into buf: exactly what trackFrame.render draws.
func (tc *trackCache) render(a trackAnim, step int, buf []byte) {
	copy(buf, tc.base)
	if len(tc.f.lines) == 0 {
		return
	}
	canvas(buf).bars(1+tc.dx, tc.f.barsBottom+tc.dy, a)
	for i, strip := range tc.strips {
		tc.f.lines[i].blit(buf, strip, step, tc.dx, tc.dy)
	}
}

func (tc *trackCache) build(t TrackText, now time.Time, dx, dy int) {
	tc.valid, tc.t, tc.dx, tc.dy = true, t, dx, dy
	tc.f = trackLayout(t, now)
	if tc.base == nil {
		tc.base = make([]byte, FrameSize)
	}
	clear(tc.base)
	tc.strips = map[int][]byte{}
	if len(tc.f.lines) == 0 {
		return
	}
	c := canvas(tc.base)
	if tc.f.ruleY >= 0 {
		c.fill(24+dx, tc.f.ruleY+dy, Width-48, 1, lvlTrackRule)
	}
	for i, l := range tc.f.lines {
		if l.overflow() > 0 {
			tc.strips[i] = l.strip()
			continue
		}
		l.draw(c, 0, dx, dy)
	}
}

// strip draws the whole line once into a buffer of its own, textWidth wide
// and 7*sc high.
func (m marqueeLine) strip() []byte {
	w, h := textWidth(m.s, m.sc), 7*m.sc
	s := make([]byte, w*h)
	x := 0
	for _, ch := range m.s {
		for gy, row := range font[ch] {
			for gx, b := range row {
				if b != '1' {
					continue
				}
				for yy := gy * m.sc; yy < (gy+1)*m.sc; yy++ {
					for xx := x + gx*m.sc; xx < x+(gx+1)*m.sc && xx < w; xx++ {
						s[yy*w+xx] = byte(m.lvl)
					}
				}
			}
		}
		x += 6 * m.sc
	}
	return s
}

// blit copies the window of the line's strip that shows at marquee step
// into buf, clipped to the line's box and the panel.
func (m marqueeLine) blit(buf, strip []byte, step, dx, dy int) {
	w, h := textWidth(m.s, m.sc), 7*m.sc
	off := min(step*m.sc, m.overflow())
	x0 := m.x0 + dx
	// panel columns lo..hi show strip columns lo-x0+off.., all in range
	lo, hi := max(x0, 0, x0-off), min(x0+m.w, Width, x0-off+w)
	if lo >= hi {
		return
	}
	s0 := lo - x0 + off
	for r := 0; r < h; r++ {
		y := m.y + dy + r
		if y < 0 || y >= Height {
			continue
		}
		copy(buf[y*Width+lo:y*Width+hi], strip[r*w+s0:])
	}
}
