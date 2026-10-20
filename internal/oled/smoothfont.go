package oled

// The track screen's font: Inter, anti-aliased, proportional, with lower
// case.
//
// The 5x7 dot matrix (logo.go) was drawn for the notices: capitals only and
// scaled by whole pixels, it made the track screen look pixelated next to the
// speaker's own screens, and its 6, 12 and 24 px sizes made the text sizes
// jump (discussion #1305). The track screen now draws Inter at two sizes
// close together, Medium at 11 px and SemiBold at 16.5 px, plus the digits
// at 30 px for the clock alone. The panel has 16 grey levels, so the glyphs
// carry 4-bit coverage and their edges are drawn in between the text's level
// and black, the way a desktop draws text.
//
// The glyphs are rasterised offline (fontgen, its own module) into
// smoothfont_data.go: the agent carries a few tens of kilobytes of tables and
// no font engine. The notices, the splash and the games keep the dot matrix.

import (
	"slices"
	"strings"
	"unicode"
)

//go:generate go -C fontgen run . -out ../smoothfont_data.go -license ../OFL-Inter.txt

// smoothGlyph is one rasterised character: its advance, and a w x h bitmap of
// coverage levels whose top left sits x, y from the pen on the baseline. The
// bitmap starts at off in the face's data, two pixels a byte, high nibble
// first.
type smoothGlyph struct {
	r         rune
	adv, x, y int8
	w, h      int8
	off       int32
}

// smoothFont is one face at one pixel size.
type smoothFont struct {
	capH, xH int // capital height and x-height in pixels
	// top and bottom are the most any glyph reaches above and below the
	// baseline: the height of a strip that holds any line of this face.
	top, bottom int
	// scroll is how many pixels one marquee step moves a line in this face,
	// so lines of both sizes pass at about the same letters a second.
	scroll int
	glyphs []smoothGlyph // sorted by rune
	data   string
}

// glyph looks r up; ok is false for a rune the face does not cover.
func (f *smoothFont) glyph(r rune) (g smoothGlyph, ok bool) {
	i, ok := slices.BinarySearchFunc(f.glyphs, r, func(g smoothGlyph, r rune) int { return int(g.r - r) })
	if !ok {
		return smoothGlyph{}, false
	}
	return f.glyphs[i], true
}

// has reports whether the face can draw r.
func (f *smoothFont) has(r rune) bool {
	_, ok := f.glyph(r)
	return ok
}

// width is how many pixels s advances the pen. Runes the face lacks count
// nothing (the sanitiser has removed them already).
func (f *smoothFont) width(s string) int {
	w := 0
	for _, r := range s {
		if g, ok := f.glyph(r); ok {
			w += int(g.adv)
		}
	}
	return w
}

// draw paints s with the pen starting at x on the baseline y, at level lvl,
// only the columns from clipX0 up to (not including) clipX1 and the rows from
// clipY0 up to clipY1. A glyph pixel of coverage c is lvl*c/15, and it only
// ever brightens what is there: two lines whose accents and descenders meet
// share the pixel instead of one erasing the other. Glyphs wholly outside the
// clip are skipped, so a long scrolling line costs only what shows.
func (f *smoothFont) draw(buf []byte, stride int, s string, x, y, lvl, clipX0, clipX1, clipY0, clipY1 int) {
	for _, r := range s {
		if x >= clipX1 {
			return
		}
		g, ok := f.glyph(r)
		if !ok {
			continue
		}
		gx0, gy0 := x+int(g.x), y+int(g.y)
		w, h := int(g.w), int(g.h)
		if gx0+w > clipX0 && h > 0 {
			for row := max(0, clipY0-gy0); row < h && gy0+row < clipY1; row++ {
				for col := max(0, clipX0-gx0); col < w && gx0+col < clipX1; col++ {
					i := row*w + col
					b := f.data[int(g.off)+i/2]
					c := int(b >> 4)
					if i%2 == 1 {
						c = int(b & 0x0f)
					}
					if c == 0 {
						continue
					}
					v := byte((lvl*c + 7) / 15)
					p := &buf[(gy0+row)*stride+gx0+col]
					*p = max(*p, v)
				}
			}
		}
		x += int(g.adv)
	}
}

// sanitizeSmooth makes s drawable in the smooth font: whitespace runs folded
// to one space, controls and the soft hyphen dropped, an accented letter the
// font lacks drawn as its base letter (Vietnamese, for one), everything else
// it cannot draw removed, and the result cut to limit characters. Case and
// the font's accents stay as they are: the track screen no longer spells
// Grönemeyer as GROENEMEYER.
func sanitizeSmooth(s string, limit int) string {
	var b strings.Builder
	n := 0
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if n >= limit {
			break
		}
		if !smoothSmall.has(r) {
			base, ok := accentBase[unicode.ToUpper(r)]
			if !ok {
				continue
			}
			if unicode.IsLower(r) {
				base = unicode.ToLower(base)
			}
			r = base
		}
		if space {
			b.WriteByte(' ')
			n++
			space = false
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimRight(b.String(), " ")
}
