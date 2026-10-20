package webui

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Light theme: every text token meets WCAG AA (4.5:1) on every surface it
// sits on (#1339, "text on white is too pale").
func TestLightThemeTextMeetsWCAGAA(t *testing.T) {
	page := readIndexHTML(t)
	i := strings.Index(page, "html.a11y-light {")
	if i < 0 {
		t.Fatal("no light theme block")
	}
	block := page[i : i+strings.Index(page[i:], "}")]
	tok := map[string]string{}
	for _, m := range regexp.MustCompile(`--([a-z0-9]+):(#[0-9a-fA-F]{6})`).FindAllStringSubmatch(block, -1) {
		tok[m[1]] = m[2]
	}
	for _, fg := range []string{"fg", "muted", "accent"} {
		for _, bg := range []string{"bg", "card", "card2", "hover", "nowgrad2"} {
			if tok[fg] == "" || tok[bg] == "" {
				t.Fatalf("token missing: %s or %s in %q", fg, bg, block)
			}
			if c := contrastRatio(tok[fg], tok[bg]); c < 4.5 {
				t.Errorf("--%s %s on --%s %s: %.2f:1, want at least 4.5:1", fg, tok[fg], bg, tok[bg], c)
			}
		}
	}
}

func contrastRatio(a, b string) float64 {
	lum := func(h string) float64 {
		var c [3]float64
		for i := range 3 {
			v, _ := strconv.ParseUint(h[1+2*i:3+2*i], 16, 8)
			x := float64(v) / 255
			if x <= 0.03928 {
				c[i] = x / 12.92
			} else {
				c[i] = math.Pow((x+0.055)/1.055, 2.4)
			}
		}
		return 0.2126*c[0] + 0.7152*c[1] + 0.0722*c[2]
	}
	x, y := lum(a), lum(b)
	if x < y {
		x, y = y, x
	}
	return (x + 0.05) / (y + 0.05)
}
