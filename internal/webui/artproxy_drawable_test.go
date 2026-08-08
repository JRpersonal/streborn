package webui

import (
	"bytes"
	"encoding/binary"
	"testing"
)

var (
	pngMagic  = []byte("\x89PNG\r\n\x1a\n")
	jpegMagic = []byte{0xFF, 0xD8, 0xFF, 0xE0}
)

func pad(head []byte, n int) []byte {
	b := make([]byte, 0, len(head)+n)
	b = append(b, head...)
	return append(b, bytes.Repeat([]byte{0x11}, n)...)
}

// buildICO wraps payloads in an ICO container the way a real favicon does.
func buildICO(payloads ...[]byte) []byte {
	const hdr, entry = 6, 16
	out := make([]byte, hdr+entry*len(payloads))
	out[2] = 1 // type: icon
	binary.LittleEndian.PutUint16(out[4:], uint16(len(payloads)))
	off := len(out)
	for i, p := range payloads {
		e := out[hdr+i*entry:]
		binary.LittleEndian.PutUint32(e[8:], uint32(len(p)))
		binary.LittleEndian.PutUint32(e[12:], uint32(off))
		off += len(p)
	}
	for _, p := range payloads {
		out = append(out, p...)
	}
	return out
}

// TestDrawableImagePassesRealPicturesThrough is the reported bug: the icon
// service answers a .ico URL with PNG bytes, and those must reach the display
// untouched instead of being swapped for STR's own logo.
func TestDrawableImagePassesRealPicturesThrough(t *testing.T) {
	cases := []struct {
		name string
		body []byte
		ct   string
	}{
		{"PNG served under a .ico URL (Sunshine Live)", pad(pngMagic, 64), "image/png"},
		{"JPEG", pad(jpegMagic, 64), "image/jpeg"},
		{"GIF", pad([]byte("GIF89a"), 64), "image/gif"},
		{"BMP", pad([]byte("BM"), 64), "image/bmp"},
		{"WEBP (MANGORADIO)", append([]byte("RIFF\x00\x00\x00\x00WEBP"), bytes.Repeat([]byte{1}, 32)...), "image/webp"},
	}
	for _, c := range cases {
		out, ct, note := drawableImage(c.body)
		if note != "" {
			t.Errorf("%s: was substituted (%s), want it served as-is", c.name, note)
		}
		if !bytes.Equal(out, c.body) {
			t.Errorf("%s: body was altered", c.name)
		}
		if ct != c.ct {
			t.Errorf("%s: content type = %q, want %q", c.name, ct, c.ct)
		}
	}
}

// TestDrawableImageUnwrapsAnICO keeps a station's own favicon.ico usable: the
// container shows nothing on the display, the PNG inside it draws fine.
func TestDrawableImageUnwrapsAnICO(t *testing.T) {
	small := pad(pngMagic, 16)
	large := pad(pngMagic, 200)
	out, ct, note := drawableImage(buildICO(small, large))
	if note != "" {
		t.Fatalf("an ICO carrying PNGs was substituted (%s)", note)
	}
	if ct != "image/png" {
		t.Errorf("content type = %q, want image/png", ct)
	}
	if !bytes.Equal(out, large) {
		t.Error("did not pick the largest embedded PNG")
	}
}

// TestDrawableImageSubstitutesWhatCannotBeDrawn keeps the 2026-08-05 fix alive
// at its new home: an SVG, or an ICO holding only old-style bitmaps, would
// leave the display blank, which reads as a fault rather than as a station
// without a picture.
func TestDrawableImageSubstitutesWhatCannotBeDrawn(t *testing.T) {
	cases := []struct {
		name string
		body []byte
	}{
		{"SVG", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect/></svg>`)},
		{"SVG served under a .png URL", []byte("<?xml version=\"1.0\"?><svg></svg>")},
		{"ICO with no PNG inside", buildICO(pad([]byte("BM"), 40))},
		{"something unrecognised", []byte("not an image at all")},
	}
	for _, c := range cases {
		out, ct, note := drawableImage(c.body)
		if note == "" {
			t.Errorf("%s: served as-is, want STR's logo in its place", c.name)
		}
		if !bytes.Equal(out, iconPNG) {
			t.Errorf("%s: did not fall back to the STR logo", c.name)
		}
		if ct != "image/png" {
			t.Errorf("%s: content type = %q, want image/png", c.name, ct)
		}
	}
}

// TestPNGInsideICORejectsRubbish guards the parser against a malformed or
// hostile container: the offsets come straight off the wire, and a speaker must
// not be crashed by a station logo.
func TestPNGInsideICORejectsRubbish(t *testing.T) {
	bad := [][]byte{
		nil,
		{},
		{0, 0, 1, 0},       // header cut short
		{0, 0, 1, 0, 5, 0}, // claims 5 entries, has none
		{0, 0, 2, 0, 1, 0}, // type 2 is a cursor, not an icon
		append([]byte{0, 0, 1, 0, 1, 0}, // offset past the end of the buffer
			bytes.Repeat([]byte{0xFF}, 16)...),
	}
	for i, b := range bad {
		if got := pngInsideICO(b); got != nil {
			t.Errorf("case %d: returned %d bytes, want nil", i, len(got))
		}
	}
}
