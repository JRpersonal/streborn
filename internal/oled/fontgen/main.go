// Command fontgen rasterises the track screen's font into Go tables.
//
// The speaker's panel is 128x100 pixels with 16 grey levels. A font engine
// on the speaker would cost a rasteriser, the font file and CPU on every
// draw; the panel only ever needs a few hundred glyphs at three sizes. So
// the glyphs are drawn once, here, on the developer's machine, as 4-bit
// coverage (0..15), and the agent links only the tables (smoothfont_data.go).
//
// The font is Inter (SIL Open Font License 1.1). Its capital height is 0.7275
// of the em and its x-height 0.546, almost exactly 4:3, so at 11 px and 16.5
// px both land on whole pixels (8 and 6, 12 and 9): the tops of the capitals
// and of the lower-case letters are a sharp row instead of a half-lit one,
// which is most of what makes small anti-aliased text read crisp. Noto Sans
// was tried beside it and lost: wider, so fewer letters fit a line, and no
// sharper at 11 px. The small face is Medium and the large one SemiBold,
// because the panel's dim levels thin a Regular stroke to a grey hairline.
//
// The release archive is fetched from GitHub and checked against a pinned
// SHA-256, so every run makes the same tables; -dir reads the TTFs and the
// licence from a local directory instead.
//
// Run through go generate in internal/oled:
//
//	go generate ./internal/oled
//
// or from this directory:
//
//	go run . -out ../smoothfont_data.go -license ../OFL-Inter.txt
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"go/format"
	"image"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const (
	interURL    = "https://github.com/rsms/inter/releases/download/v4.1/Inter-4.1.zip"
	interSHA256 = "9883fdd4a49d4fb66bd8177ba6625ef9a64aa45899767dde3d36aa425756b11e"
)

// faceSpec is one generated face.
type faceSpec struct {
	varName string  // Go variable in the generated file
	file    string  // TTF inside the release archive (extras/ttf/...)
	size    float64 // pixels per em
	runes   []rune
	// scroll is how many pixels a marquee step moves a line in this face.
	scroll int
	// gamma < 1 lifts the half-covered edge pixels a little, so thin
	// strokes keep their weight on the panel.
	gamma float64
}

// textRunes is what the text faces cover: printable ASCII, Latin-1,
// Latin Extended-A, the Romanian comma-below letters, the capital sharp s,
// and the typographic marks that turn up in song titles and station names.
func textRunes() []rune {
	var rs []rune
	add := func(lo, hi rune) {
		for r := lo; r <= hi; r++ {
			rs = append(rs, r)
		}
	}
	add(0x20, 0x7e)
	add(0xa0, 0xff)
	add(0x100, 0x17f)
	add(0x218, 0x21b)
	for _, r := range "\u1e9e\u2010\u2011\u2013\u2014\u2018\u2019\u201a\u201c\u201d\u201e\u2022\u2026\u2032\u2033\u2039\u203a\u20ac\u2122\u02bc" {
		rs = append(rs, r)
	}
	// U+00AD, the soft hyphen, is invisible by definition and the sanitiser
	// drops it; U+0149 (n preceded by apostrophe) is deprecated by Unicode
	// and Inter does not draw it. Neither gets a glyph.
	rs = slices.DeleteFunc(rs, func(r rune) bool { return r == 0xad || r == 0x149 })
	slices.Sort(rs)
	return slices.Compact(rs)
}

func specs() []faceSpec {
	text := textRunes()
	return []faceSpec{
		{varName: "smoothSmall", file: "Inter-Medium.ttf", size: 11, runes: text, scroll: 1, gamma: 0.85},
		{varName: "smoothLarge", file: "Inter-SemiBold.ttf", size: 16.5, runes: text, scroll: 2, gamma: 0.9},
		// The clock alone: digits and the colon only. 30.25 px puts the
		// digits (capital height) on exactly 22 pixels.
		{varName: "smoothClock", file: "Inter-Medium.ttf", size: 30.25, runes: []rune("0123456789:"), scroll: 2, gamma: 1},
	}
}

type glyph struct {
	r         rune
	adv, x, y int // advance; bitmap left and top relative to the pen on the baseline
	w, h      int
	cov       []byte // w*h levels 0..15
}

type face struct {
	spec        faceSpec
	capH, xH    int
	top, bottom int // the most any glyph reaches above / below the baseline
	glyphs      []glyph
}

func main() {
	out := flag.String("out", "../smoothfont_data.go", "generated Go file")
	lic := flag.String("license", "../OFL-Inter.txt", "where to write the font's licence")
	dir := flag.String("dir", "", "read the TTFs and LICENSE.txt from this directory instead of the release archive")
	flag.Parse()

	files := map[string][]byte{}
	need := []string{"LICENSE.txt"}
	for _, s := range specs() {
		if !slices.Contains(need, s.file) {
			need = append(need, s.file)
		}
	}
	if *dir != "" {
		for _, n := range need {
			b, err := os.ReadFile(filepath.Join(*dir, n))
			if err != nil {
				log.Fatal(err)
			}
			files[n] = b
		}
	} else {
		files = fromArchive(need)
	}

	var faces []face
	for _, s := range specs() {
		faces = append(faces, rasterise(s, files[s.file]))
	}
	src := emit(faces)
	if err := os.WriteFile(*out, src, 0o644); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*lic, files["LICENSE.txt"], 0o644); err != nil {
		log.Fatal(err)
	}
	for _, f := range faces {
		log.Printf("%s: %s %.2f px, cap %d, x-height %d, %d glyphs, %d..%d around the baseline",
			f.spec.varName, f.spec.file, f.spec.size, f.capH, f.xH, len(f.glyphs), -f.top, f.bottom)
	}
}

// fromArchive fetches the Inter release (cached in the user cache dir),
// checks it against the pinned hash and returns the named members.
func fromArchive(names []string) map[string][]byte {
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	path := filepath.Join(cache, "streborn-fontgen", "Inter-4.1.zip")
	b, err := os.ReadFile(path)
	if err != nil || sum(b) != interSHA256 {
		log.Printf("fetching %s", interURL)
		resp, err := http.Get(interURL)
		if err != nil {
			log.Fatal(err)
		}
		b, err = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			log.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			log.Fatalf("%s: %s", interURL, resp.Status)
		}
		if got := sum(b); got != interSHA256 {
			log.Fatalf("%s: sha256 %s, want %s", interURL, got, interSHA256)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			_ = os.WriteFile(path, b, 0o644)
		}
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		log.Fatal(err)
	}
	files := map[string][]byte{}
	for _, f := range zr.File {
		base := f.Name
		if f.Name != "LICENSE.txt" {
			if !strings.HasPrefix(f.Name, "extras/ttf/") {
				continue
			}
			base = strings.TrimPrefix(f.Name, "extras/ttf/")
		}
		if !slices.Contains(names, base) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			log.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			log.Fatal(err)
		}
		files[base] = data
	}
	for _, n := range names {
		if files[n] == nil {
			log.Fatalf("%s not in %s", n, interURL)
		}
	}
	return files
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func rasterise(s faceSpec, ttf []byte) face {
	f, err := opentype.Parse(ttf)
	if err != nil {
		log.Fatal(err)
	}
	fc, err := opentype.NewFace(f, &opentype.FaceOptions{Size: s.size, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		log.Fatal(err)
	}
	m := fc.Metrics()
	out := face{spec: s, capH: m.CapHeight.Round(), xH: m.XHeight.Round()}
	for _, r := range s.runes {
		if _, ok := fc.GlyphAdvance(r); !ok {
			log.Fatalf("%s has no glyph for %U", s.file, r)
		}
		idx, _ := f.GlyphIndex(nil, r)
		if idx == 0 {
			log.Fatalf("%s has no glyph for %U", s.file, r)
		}
		g := glyph{r: r}
		dr, mask, mp, adv, ok := fc.Glyph(fixed.Point26_6{}, r)
		g.adv = adv.Round()
		if ok && !dr.Empty() {
			g.x, g.y, g.w, g.h, g.cov = crop(dr, mask, mp, s.gamma)
		}
		if g.h > 0 {
			out.top = max(out.top, -g.y)
			out.bottom = max(out.bottom, g.y+g.h)
		}
		out.glyphs = append(out.glyphs, g)
	}
	return out
}

// crop quantises the glyph's coverage to 0..15 and trims the rows and
// columns that came out empty.
func crop(dr image.Rectangle, mask image.Image, mp image.Point, gamma float64) (x, y, w, h int, cov []byte) {
	full := make([]byte, dr.Dx()*dr.Dy())
	for yy := 0; yy < dr.Dy(); yy++ {
		for xx := 0; xx < dr.Dx(); xx++ {
			_, _, _, a := mask.At(mp.X+xx, mp.Y+yy).RGBA()
			v := math.Pow(float64(a)/0xffff, gamma)
			full[yy*dr.Dx()+xx] = byte(math.Round(v * 15))
		}
	}
	x0, y0, x1, y1 := dr.Dx(), dr.Dy(), -1, -1
	for yy := 0; yy < dr.Dy(); yy++ {
		for xx := 0; xx < dr.Dx(); xx++ {
			if full[yy*dr.Dx()+xx] != 0 {
				x0, y0, x1, y1 = min(x0, xx), min(y0, yy), max(x1, xx), max(y1, yy)
			}
		}
	}
	if x1 < 0 {
		return 0, 0, 0, 0, nil
	}
	w, h = x1-x0+1, y1-y0+1
	cov = make([]byte, 0, w*h)
	for yy := y0; yy <= y1; yy++ {
		cov = append(cov, full[yy*dr.Dx()+x0:yy*dr.Dx()+x1+1]...)
	}
	return dr.Min.X + x0, dr.Min.Y + y0, w, h, cov
}

func emit(faces []face) []byte {
	var b bytes.Buffer
	b.WriteString("// Code generated by internal/oled/fontgen; DO NOT EDIT.\n\n")
	b.WriteString("// Inter, Copyright (c) 2016 The Inter Project Authors, SIL Open Font\n")
	b.WriteString("// License 1.1 (OFL-Inter.txt). Rasterised from the v4.1 release.\n\n")
	b.WriteString("package oled\n\n")
	for _, f := range faces {
		var data []byte
		fmt.Fprintf(&b, "// %s is Inter %s at %g px: capitals %d px, x-height %d px.\n",
			f.spec.varName, strings.TrimSuffix(strings.TrimPrefix(f.spec.file, "Inter-"), ".ttf"), f.spec.size, f.capH, f.xH)
		fmt.Fprintf(&b, "var %s = &smoothFont{\n", f.spec.varName)
		fmt.Fprintf(&b, "capH: %d, xH: %d, top: %d, bottom: %d, scroll: %d,\n", f.capH, f.xH, f.top, f.bottom, f.spec.scroll)
		b.WriteString("glyphs: []smoothGlyph{\n")
		for _, g := range f.glyphs {
			fmt.Fprintf(&b, "{0x%04x, %d, %d, %d, %d, %d, %d},\n", g.r, g.adv, g.x, g.y, g.w, g.h, len(data))
			// two pixels a byte, high nibble first; a glyph starts on a byte
			for i := 0; i < len(g.cov); i += 2 {
				v := g.cov[i] << 4
				if i+1 < len(g.cov) {
					v |= g.cov[i+1]
				}
				data = append(data, v)
			}
		}
		b.WriteString("},\n")
		b.WriteString("data: \"\" +\n")
		for i := 0; i < len(data); i += 32 {
			b.WriteString("\"")
			for _, v := range data[i:min(i+32, len(data))] {
				fmt.Fprintf(&b, "\\x%02x", v)
			}
			b.WriteString("\"")
			if i+32 < len(data) {
				b.WriteString(" +")
			} else {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString("}\n\n")
	}
	src, err := format.Source(b.Bytes())
	if err != nil {
		log.Fatalf("generated source: %v", err)
	}
	return src
}
