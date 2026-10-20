package oled

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// Every rune the track screen promises to draw has a glyph in both text
// faces, the clock face has the digits and the colon, and every glyph's
// bitmap lies inside the face's table and its declared height.
func TestSmoothFontCoverage(t *testing.T) {
	var want []rune
	for r := rune(0x20); r <= 0x7e; r++ {
		want = append(want, r)
	}
	for r := rune(0xa0); r <= 0x17f; r++ {
		// the soft hyphen is dropped by the sanitiser; U+0149 is
		// deprecated and Inter has no glyph for it
		if r != 0xad && r != 0x149 {
			want = append(want, r)
		}
	}
	want = append(want, []rune("ẞȘșȚț‘’‚“”„…–—•€")...)
	for name, f := range map[string]*smoothFont{"small": smoothSmall, "large": smoothLarge} {
		for _, r := range want {
			if !f.has(r) {
				t.Errorf("%s: no glyph for %U %q", name, r, r)
			}
		}
	}
	for _, r := range "0123456789:" {
		if !smoothClock.has(r) {
			t.Errorf("clock: no glyph for %q", r)
		}
	}
	for name, f := range map[string]*smoothFont{"small": smoothSmall, "large": smoothLarge, "clock": smoothClock} {
		if !slices.IsSortedFunc(f.glyphs, func(a, b smoothGlyph) int { return int(a.r - b.r) }) {
			t.Errorf("%s: glyphs not sorted", name)
		}
		for _, g := range f.glyphs {
			if g.adv <= 0 {
				t.Errorf("%s %q: advance %d", name, g.r, g.adv)
			}
			n := (int(g.w)*int(g.h) + 1) / 2
			if int(g.off)+n > len(f.data) || -int(g.y) > f.top || int(g.y)+int(g.h) > f.bottom {
				t.Errorf("%s %q: bitmap out of range %+v", name, g.r, g)
			}
		}
	}
	// The sizes are chosen so the capitals and the x-height end on whole
	// pixels (fontgen): a regenerated table with other sizes must say so.
	if smoothSmall.capH != 8 || smoothLarge.capH != 12 || smoothClock.capH != 22 {
		t.Errorf("capital heights %d, %d, %d", smoothSmall.capH, smoothLarge.capH, smoothClock.capH)
	}
}

// A glyph is drawn at the line's level, its edges in between, and drawing
// only ever brightens: a dim line over a bright one leaves the bright pixels.
func TestSmoothFontDraw(t *testing.T) {
	buf := make([]byte, FrameSize)
	smoothLarge.draw(buf, Width, "H", 10, 30, 12, 0, Width, 0, Height)
	if m := maxByte(buf); m != 12 {
		t.Fatalf("H at level 12 peaks at %d", m)
	}
	before := append([]byte(nil), buf...)
	smoothLarge.draw(buf, Width, "H", 10, 30, 3, 0, Width, 0, Height)
	if string(buf) != string(before) {
		t.Error("a dimmer glyph darkened a brighter one")
	}
	// the clip keeps every pixel inside
	clear(buf)
	smoothLarge.draw(buf, Width, "WWWW", 10, 30, 15, 20, 30, 25, 28)
	for i, v := range buf {
		if x, y := i%Width, i/Width; v != 0 && (x < 20 || x >= 30 || y < 25 || y >= 28) {
			t.Fatalf("pixel %d,%d outside the clip", x, y)
		}
	}
}

func TestSanitizeSmooth(t *testing.T) {
	for _, c := range []struct {
		in    string
		limit int
		want  string
	}{
		{"Herbert Grönemeyer", 160, "Herbert Grönemeyer"},
		{"  Beirut   (Live à\tl’Olympia) ", 160, "Beirut (Live à l’Olympia)"},
		{"Straße – Łódź … „Zitat“", 160, "Straße – Łódź … „Zitat“"},
		// a Vietnamese letter outside the covered ranges: its base letter
		{"Sơn Tùng – Lạc Trôi", 160, "Son Tùng – Lac Trôi"},
		// soft hyphen, controls and emoji are dropped
		{"Hy\u00adphen\x07 ok \U0001F3B5", 160, "Hyphen ok"},
		{"Männer und Frauen", 6, "Männer"},
	} {
		if got := sanitizeSmooth(c.in, c.limit); got != c.want {
			t.Errorf("sanitizeSmooth(%q, %d) = %q, want %q", c.in, c.limit, got, c.want)
		}
	}
}

// Wrapping goes by pixels: narrow letters fit more to a line than wide ones,
// no line is wider than the box, a word wider than a line is split, and a
// text that does not fit ends in an ellipsis within the width.
func TestWrapWidth(t *testing.T) {
	f := smoothSmall
	if l, cut := wrapWidth("", f, 50, 2); l != nil || cut {
		t.Errorf("empty: %q %v", l, cut)
	}
	if l, cut := wrapWidth("Hello", f, blockBoxW, 2); !reflect.DeepEqual(l, []string{"Hello"}) || cut {
		t.Errorf("fits: %q %v", l, cut)
	}
	narrow, wide := strings.Repeat("i", 20), strings.Repeat("M", 20)
	if f.width(narrow) >= f.width(wide) {
		t.Fatalf("the font is not proportional: %d >= %d", f.width(narrow), f.width(wide))
	}
	for _, c := range []struct {
		s        string
		w, n     int
		wantCut  bool
		minLines int
	}{
		{"Herbert Grönemeyer", 80, 2, false, 2},
		{wide, 60, 5, false, 4},
		{"one two three four five six seven eight nine ten", 50, 2, true, 2},
		{"Żółć gęślą jaźń", 40, 3, false, 2},
	} {
		lines, cut := wrapWidth(c.s, f, c.w, c.n)
		if cut != c.wantCut || len(lines) < c.minLines || len(lines) > c.n {
			t.Errorf("wrapWidth(%q, %d, %d) = %q cut=%v", c.s, c.w, c.n, lines, cut)
		}
		for _, l := range lines {
			if f.width(l) > c.w {
				t.Errorf("wrapWidth(%q): line %q is %d px, box %d", c.s, l, f.width(l), c.w)
			}
		}
		if cut && !strings.HasSuffix(lines[len(lines)-1], "…") {
			t.Errorf("wrapWidth(%q): cut without an ellipsis: %q", c.s, lines)
		}
	}
	// a box narrower than one letter still makes progress, a letter a line
	if l, _ := wrapWidth("WWW", f, 1, 5); len(l) != 3 {
		t.Errorf("narrow box: %q", l)
	}
}

// Long German and French titles either wrap onto two lines that fit or
// scroll to their very end; no frame of the cycle runs into the panel's
// edge.
func TestLongTitlesWrapOrScrollWithoutClipping(t *testing.T) {
	at := time.Date(2026, 10, 20, 15, 42, 0, 0, time.Local)
	for _, tt := range []TrackText{
		{Station: "WDR 2", Artist: "Herbert Grönemeyer", Title: "Männer", Clock: true},
		{Station: "FIP", Artist: "Ibrahim Maalouf", Title: "Beirut (Live à l'Olympia)"},
		{Station: "Bayern 3", Artist: "Die Fantastischen Vier", Title: "Tag am Meer (Unplugged Version)", Clock: true},
		{Station: "France Inter", Artist: "Françoise Hardy", Title: "Tous les garçons et les filles de mon âge"},
		{Station: "NDR 2", Artist: "Rammstein", Title: "Donaudampfschifffahrtsgesellschaftskapitän"},
	} {
		tt = tt.sanitized()
		f := trackLayout(tt, at)
		var titles []marqueeLine
		for _, l := range f.lines {
			if l.lvl == lvlTrackTitle {
				titles = append(titles, l)
			}
		}
		switch {
		case len(titles) == 0 || len(titles) > 2:
			t.Errorf("%q: %d title lines", tt.Title, len(titles))
		case len(titles) == 2:
			for _, l := range titles {
				if l.overflow() > 0 {
					t.Errorf("%q: wrapped line %q does not fit", tt.Title, l.s)
				}
			}
		case titles[0].overflow() > 0:
			// scrolls: the last step shows the end of the title in the box
			end := titles[0]
			if got := textWidth(end.s, end.f) - end.shift(end.steps()); got != end.w {
				t.Errorf("%q: the end shows %d px of a %d px box", tt.Title, got, end.w)
			}
		}
		buf := make([]byte, FrameSize)
		for step := 0; step <= f.steps(); step++ {
			f.render(trackAnim{}, step, 0, 0, buf)
			if _, last, bad := litRows(buf); bad || last >= Height-1 {
				t.Fatalf("%q step %d: last row %d bad=%v", tt.Title, step, last, bad)
			}
			for y := 0; y < Height; y++ {
				if buf[y*Width] != 0 || buf[y*Width+Width-1] != 0 {
					t.Fatalf("%q step %d: text touches the panel edge at row %d", tt.Title, step, y)
				}
			}
		}
	}
}
