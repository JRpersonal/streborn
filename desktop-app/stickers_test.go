package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	wailsrt "github.com/wailsapp/wails/v2/pkg/runtime"
)

// stickerPage is a small page image with the A4 aspect ratio, filled with one
// colour.
func stickerPage(t *testing.T, c color.Color) image.Image {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 210, 297))
	for y := 0; y < 297; y++ {
		for x := 0; x < 210; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

func pngDataURI(t *testing.T, img image.Image) string {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// parsedPDF is what a reader finds by following the file the way the format
// says to: startxref to the table, the table to each object.
type parsedPDF struct {
	objects map[int]string
}

func parseStickerPDF(t *testing.T, data []byte) parsedPDF {
	t.Helper()
	if !bytes.HasPrefix(data, []byte("%PDF-1.")) {
		t.Fatalf("no PDF header: %.20q", data)
	}
	if !bytes.HasSuffix(data, []byte("%%EOF\n")) {
		t.Fatalf("no EOF marker at the end: %.20q", data[len(data)-20:])
	}
	m := regexp.MustCompile(`startxref\n(\d+)\n%%EOF\n$`).FindSubmatch(data)
	if m == nil {
		t.Fatal("no startxref")
	}
	xref, _ := strconv.Atoi(string(m[1]))
	if xref >= len(data) || !bytes.HasPrefix(data[xref:], []byte("xref\n0 ")) {
		t.Fatalf("startxref %d does not point at the table", xref)
	}
	rest := data[xref+len("xref\n"):]
	var first, count int
	if _, err := fmt.Sscanf(string(rest), "%d %d\n", &first, &count); err != nil {
		t.Fatal(err)
	}
	rest = rest[bytes.IndexByte(rest, '\n')+1:]
	out := parsedPDF{objects: map[int]string{}}
	for i := 0; i < count; i++ {
		entry := string(rest[i*20 : i*20+20])
		if !regexp.MustCompile(`^\d{10} \d{5} [nf] \n$`).MatchString(entry) {
			t.Fatalf("xref entry %d is not 20 bytes in the fixed form: %q", i, entry)
		}
		if entry[17] == 'f' {
			continue
		}
		off, _ := strconv.Atoi(entry[:10])
		head := fmt.Sprintf("%d 0 obj\n", i)
		if off >= len(data) || !bytes.HasPrefix(data[off:], []byte(head)) {
			t.Fatalf("xref offset of object %d (%d) does not point at %q", i, off, head)
		}
		end := bytes.Index(data[off:], []byte("\nendobj\n"))
		if end < 0 {
			t.Fatalf("object %d has no endobj", i)
		}
		out.objects[i] = string(data[off+len(head) : off+end])
	}
	if !regexp.MustCompile(`trailer\n<< /Size ` + strconv.Itoa(count) + ` /Root 1 0 R >>`).Match(data) {
		t.Error("trailer does not name the catalog and the object count")
	}
	return out
}

// pages follows catalog -> page tree -> kids.
func (p parsedPDF) pages(t *testing.T) []string {
	t.Helper()
	if !strings.Contains(p.objects[1], "/Type /Catalog") || !strings.Contains(p.objects[1], "/Pages 2 0 R") {
		t.Fatalf("object 1 is not the catalog: %q", p.objects[1])
	}
	m := regexp.MustCompile(`/Count (\d+) /Kids \[([^\]]*)\]`).FindStringSubmatch(p.objects[2])
	if m == nil {
		t.Fatalf("object 2 is not a page tree: %q", p.objects[2])
	}
	var out []string
	for _, ref := range regexp.MustCompile(`(\d+) 0 R`).FindAllStringSubmatch(m[2], -1) {
		n, _ := strconv.Atoi(ref[1])
		out = append(out, p.objects[n])
	}
	if want, _ := strconv.Atoi(m[1]); want != len(out) {
		t.Errorf("/Count says %d, /Kids holds %d", want, len(out))
	}
	return out
}

// streamOf returns an object's stream, checked against its /Length.
func (p parsedPDF) streamOf(t *testing.T, n int) (dict string, data []byte) {
	t.Helper()
	obj := p.objects[n]
	i := strings.Index(obj, "\nstream\n")
	if i < 0 || !strings.HasSuffix(obj, "\nendstream") {
		t.Fatalf("object %d is not a stream", n)
	}
	dict = obj[:i]
	data = []byte(obj[i+len("\nstream\n") : len(obj)-len("\nendstream")])
	m := regexp.MustCompile(`/Length (\d+)`).FindStringSubmatch(dict)
	if m == nil {
		t.Fatalf("object %d has no /Length", n)
	}
	if want, _ := strconv.Atoi(m[1]); want != len(data) {
		t.Errorf("object %d: /Length %d, stream holds %d bytes", n, want, len(data))
	}
	return dict, data
}

func TestWriteStickerPDFParsesBack(t *testing.T) {
	red := color.NRGBA{R: 200, G: 30, B: 40, A: 255}
	pages := []image.Image{stickerPage(t, red), stickerPage(t, color.NRGBA{A: 0}), stickerPage(t, color.NRGBA{B: 255, A: 255})}
	var buf bytes.Buffer
	if err := writeStickerPDF(&buf, pages); err != nil {
		t.Fatal(err)
	}
	pdf := parseStickerPDF(t, buf.Bytes())
	got := pdf.pages(t)
	if len(got) != 3 {
		t.Fatalf("%d pages, want 3", len(got))
	}
	for i, page := range got {
		if !strings.Contains(page, "/Type /Page ") {
			t.Errorf("page %d is not a page object: %q", i+1, page)
		}
		// A4 is 210 x 297 mm, in points 595.28 x 841.89.
		if !strings.Contains(page, "/MediaBox [0 0 595.28 841.89]") {
			t.Errorf("page %d is not A4: %q", i+1, page)
		}
		m := regexp.MustCompile(`/Im0 (\d+) 0 R >> >> /Contents (\d+) 0 R`).FindStringSubmatch(page)
		if m == nil {
			t.Fatalf("page %d names no image and content: %q", i+1, page)
		}
		contentObj, _ := strconv.Atoi(m[2])
		if _, content := pdf.streamOf(t, contentObj); string(content) != "q 595.28 0 0 841.89 0 0 cm /Im0 Do Q" {
			t.Errorf("page %d does not draw its image over the whole page: %q", i+1, content)
		}
		imageObj, _ := strconv.Atoi(m[1])
		dict, data := pdf.streamOf(t, imageObj)
		for _, want := range []string{"/Subtype /Image", "/Width 210", "/Height 297", "/ColorSpace /DeviceRGB", "/BitsPerComponent 8", "/Filter /FlateDecode"} {
			if !strings.Contains(dict, want) {
				t.Errorf("page %d image lacks %s: %q", i+1, want, dict)
			}
		}
		zr, err := zlib.NewReader(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		pix, err := io.ReadAll(zr)
		if err != nil {
			t.Fatal(err)
		}
		if len(pix) != 210*297*3 {
			t.Fatalf("page %d image holds %d bytes, want %d", i+1, len(pix), 210*297*3)
		}
		want := [][3]byte{{200, 30, 40}, {255, 255, 255}, {0, 0, 255}}[i] // a transparent page prints as paper
		if got := [3]byte{pix[0], pix[1], pix[2]}; got != want {
			t.Errorf("page %d first pixel %v, want %v", i+1, got, want)
		}
	}
}

// An image type other than the canvas's NRGBA takes the general path.
func TestRGBBytesGeneralImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	// Half-covered black, premultiplied: half way to the white paper.
	img.SetRGBA(1, 0, color.RGBA{A: 128})
	got := rgbBytes(img)
	if want := []byte{10, 20, 30, 127, 127, 127}; !bytes.Equal(got, want) {
		t.Errorf("rgbBytes = %v, want %v", got, want)
	}
}

// withStickerSeams swaps the save dialog and the viewer for the test.
func withStickerSeams(t *testing.T, dialog func(wailsrt.SaveDialogOptions) (string, error), opened *[]string) {
	t.Helper()
	oldDialog, oldOpen := stickerSaveDialog, stickerOpenFile
	stickerSaveDialog = func(_ context.Context, opts wailsrt.SaveDialogOptions) (string, error) { return dialog(opts) }
	stickerOpenFile = func(path string) error {
		*opened = append(*opened, path)
		return nil
	}
	t.Cleanup(func() { stickerSaveDialog, stickerOpenFile = oldDialog, oldOpen })
}

func TestSaveStickerPDFWritesAndOpens(t *testing.T) {
	dir := t.TempDir()
	var opened []string
	var asked wailsrt.SaveDialogOptions
	withStickerSeams(t, func(opts wailsrt.SaveDialogOptions) (string, error) {
		asked = opts
		// No extension, as a user types it: the file still ends in .pdf.
		return filepath.Join(dir, "kitchen"), nil
	}, &opened)

	page := pngDataURI(t, stickerPage(t, color.NRGBA{R: 255, G: 255, B: 255, A: 255}))
	path, err := (&App{}).SaveStickerPDF([]string{page, page})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "kitchen.pdf"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if asked.DefaultFilename != "st-reborn-stickers.pdf" {
		t.Errorf("dialog proposes %q", asked.DefaultFilename)
	}
	if len(opened) != 1 || opened[0] != path {
		t.Errorf("opened %v, want the saved file once", opened)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(parseStickerPDF(t, data).pages(t)); n != 2 {
		t.Errorf("%d pages in the file, want 2", n)
	}
}

func TestSaveStickerPDFCancelled(t *testing.T) {
	var opened []string
	withStickerSeams(t, func(wailsrt.SaveDialogOptions) (string, error) { return "", nil }, &opened)
	page := pngDataURI(t, stickerPage(t, color.White))
	path, err := (&App{}).SaveStickerPDF([]string{page})
	if path != "" || err != nil {
		t.Errorf("cancelled dialog: path %q, err %v, want neither", path, err)
	}
	if len(opened) != 0 {
		t.Errorf("a cancelled save opened %v", opened)
	}
}

func TestSaveStickerPDFDialogError(t *testing.T) {
	var opened []string
	withStickerSeams(t, func(wailsrt.SaveDialogOptions) (string, error) { return "", errors.New("no dialog") }, &opened)
	page := pngDataURI(t, stickerPage(t, color.White))
	if _, err := (&App{}).SaveStickerPDF([]string{page}); err == nil {
		t.Error("a failing dialog must be reported")
	}
}

func TestSaveStickerPDFRejectsBadInput(t *testing.T) {
	good := pngDataURI(t, stickerPage(t, color.White))
	landscape := pngDataURI(t, image.NewNRGBA(image.Rect(0, 0, 297, 210)))
	huge := pngDataURI(t, image.NewGray(image.Rect(0, 0, stickerMaxEdge+1, 1)))
	many := make([]string, stickerMaxPages+1)
	for i := range many {
		many[i] = good
	}
	cases := map[string][]string{
		"no pages":        nil,
		"too many pages":  many,
		"not a data URI":  {"https://example.com/page.png"},
		"a JPEG data URI": {"data:image/jpeg;base64,AAAA"},
		"bad base64":      {"data:image/png;base64,!!!"},
		"not a PNG":       {"data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("not a png"))},
		"not A4":          {landscape},
		"too large":       {huge},
		"one bad of two":  {good, "data:image/png;base64,"},
	}
	for name, pages := range cases {
		t.Run(name, func(t *testing.T) {
			var opened []string
			asked := false
			withStickerSeams(t, func(wailsrt.SaveDialogOptions) (string, error) {
				asked = true
				return filepath.Join(t.TempDir(), "x.pdf"), nil
			}, &opened)
			path, err := (&App{}).SaveStickerPDF(pages)
			if err == nil || path != "" {
				t.Errorf("accepted: path %q, err %v", path, err)
			}
			if asked {
				t.Error("the save dialog opened for input that cannot be saved")
			}
		})
	}
}

func TestStickerImage(t *testing.T) {
	img := pngBytes(t, 8, 8)
	mux := http.NewServeMux()
	mux.HandleFunc("/logo.png", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(img)
	})
	mux.HandleFunc("/page", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html></html>"))
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(bytes.Repeat([]byte{0xff}, lanArtMaxBytes+10))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	a := &App{}
	if got := a.StickerImage(srv.URL + "/logo.png"); !strings.HasPrefix(got, "data:image/jpeg;base64,") {
		t.Errorf("a logo did not come back as a data URI: %.40q", got)
	}
	for name, u := range map[string]string{
		"missing":       srv.URL + "/nope",
		"not an image":  srv.URL + "/page",
		"over the cap":  srv.URL + "/big",
		"file scheme":   "file:///etc/passwd",
		"data URI":      "data:image/png;base64,AAAA",
		"no scheme":     "example.com/logo.png",
		"empty":         "",
		"nothing there": "http://127.0.0.1:1/logo.png",
	} {
		if got := a.StickerImage(u); got != "" {
			t.Errorf("%s: StickerImage(%q) = %.40q, want \"\"", name, u, got)
		}
	}
}
