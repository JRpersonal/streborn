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
