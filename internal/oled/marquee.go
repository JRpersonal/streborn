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
	// line by its face's scroll pixels (1 px in the small face, 2 px in the
	// large one), so both sizes run at about the same pace of two to three
	// letters a second. 15 frames a second read smooth on the Portable's
	// panel.
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
	s     string
	f     *smoothFont
	lvl   int
	x0, w int // the box, unshifted
	y     int // baseline, unshifted
	// left keeps a fitting text at the box's left edge instead of centring it
	left bool
}

// textWidth is the width in pixels of s in face f.
func textWidth(s string, f *smoothFont) int { return f.width(s) }

// overflow is how many pixels the text is wider than its box (0: it fits).
func (m marqueeLine) overflow() int { return max(0, textWidth(m.s, m.f)-m.w) }

// steps is how many marquee steps the line needs to show its end.
func (m marqueeLine) steps() int {
	ov := m.overflow()
	return (ov + m.f.scroll - 1) / m.f.scroll
}

// shift is how far the text has moved left at marquee step.
func (m marqueeLine) shift(step int) int { return min(step*m.f.scroll, m.overflow()) }

// draw paints the line at marquee step (ignored when it fits), moved by dx,
// dy, clipped to its box.
func (m marqueeLine) draw(c canvas, step, dx, dy int) {
	if m.s == "" {
		return
	}
	x0 := m.x0 + dx
	x := x0
	if m.overflow() > 0 {
		x -= m.shift(step)
	} else if !m.left {
		x += (m.w - textWidth(m.s, m.f)) / 2
	}
	m.f.draw(c, Width, m.s, x, m.y+dy, m.lvl, max(x0, 0), min(x0+m.w, Width), 0, Height)
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
// and as high as the face reaches above and below the baseline.
func (m marqueeLine) strip() []byte {
	w, h := textWidth(m.s, m.f), m.f.top+m.f.bottom
	s := make([]byte, w*h)
	m.f.draw(s, w, m.s, 0, m.f.top, m.lvl, 0, w, 0, h)
	return s
}

// blit lays the window of the line's strip that shows at marquee step over
// buf, clipped to the line's box and the panel. It brightens like drawing
// does (smoothFont.draw), so a strip whose empty rows reach over a
// neighbouring line's accents or descenders leaves them standing.
func (m marqueeLine) blit(buf, strip []byte, step, dx, dy int) {
	w, h := textWidth(m.s, m.f), m.f.top+m.f.bottom
	off := m.shift(step)
	x0 := m.x0 + dx
	// panel columns lo..hi show strip columns lo-x0+off.., all in range
	lo, hi := max(x0, 0, x0-off), min(x0+m.w, Width, x0-off+w)
	if lo >= hi {
		return
	}
	s0 := lo - x0 + off
	top := m.y + dy - m.f.top
	for r := 0; r < h; r++ {
		y := top + r
		if y < 0 || y >= Height {
			continue
		}
		dst := buf[y*Width+lo : y*Width+hi]
		for i, v := range strip[r*w+s0 : r*w+s0+hi-lo] {
			dst[i] = max(dst[i], v)
		}
	}
}
