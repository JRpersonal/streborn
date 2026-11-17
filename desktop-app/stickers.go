package main

// Stickers for the speakers: the Go half.
//
// The window lays the sticker sheet out and paints it, because station names
// come in every script and only the webview has a font for all of them. Two
// things it cannot do itself are done here: fetch a station logo without
// tainting its canvas (StickerImage hands the bytes back as a data: URI), and
// write a file (SaveStickerPDF wraps the painted pages into a PDF).
//
// The PDF is written by hand with the standard library. One full-page image
// per page is all it has to hold, and that is a dozen objects.

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	wailsrt "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	// stickerPDFName is the file name the save dialog proposes.
	stickerPDFName = "st-reborn-stickers.pdf"
	// stickerMaxPages bounds one document. A page holds three to four
	// speakers, so this is far beyond any household.
	stickerMaxPages = 40
	// stickerMaxEdge is the longest edge of a page image that is accepted. A4
	// at 300 dpi is 2480 x 3508; twice that leaves room for a sharper render
	// and still bounds the memory one page can ask for.
	stickerMaxEdge = 7100
	// A4 in PDF points (1/72 inch).
	stickerPageW = 595.28
	stickerPageH = 841.89
	// stickerAspectSlack is how far a page image may be off the A4 aspect
	// ratio. The image is stretched onto the page, so one that is not A4
	// would no longer print true to size.
	stickerAspectSlack = 0.01
)

// stickerSaveDialog and stickerOpenFile are the two steps of SaveStickerPDF
// that need a desktop, as variables so a test can stand in for them.
var (
	stickerSaveDialog = func(ctx context.Context, opts wailsrt.SaveDialogOptions) (string, error) {
		return wailsrt.SaveFileDialog(ctx, opts)
	}
	stickerOpenFile = openWithSystemViewer
)

// StickerImage fetches a station logo or cover for the sticker sheet and
// returns it as a data: URI, or "" when it cannot be had. The window draws the
// sheet onto a canvas, and an image drawn from another origin would taint the
// canvas so the page could not be read back; a data: URI does not.
//
// It is the media-server cover fetch (fetchLANArt) without the LAN restriction:
// same timeout, same size cap, same "must answer 200 with an image", same
// tile-sized result. Only http and https are fetched.
func (a *App) StickerImage(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (!strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https")) {
		return ""
	}
	ctx, cancel := context.WithTimeout(a.appCtx(), lanArtTimeout)
	defer cancel()
	uri, info, err := fetchLANArt(ctx, lanArtClient, raw)
	if err != nil {
		// Once per URL, like the cover fetch: the sheet is redrawn on every
		// colour switch and asks again each time.
		if a.logger != nil && a.lanArtLog.first("sticker\x1f"+raw) {
			a.logger.Info("sticker logo: not printed", "url", raw, "reason", err.Error(),
				"status", info.status, "contentType", info.ctype, "bytes", info.size)
		}
		return ""
	}
	if a.logger != nil {
		a.logger.Debug("sticker logo: fetched", "url", raw, "contentType", info.ctype, "bytes", info.size)
	}
	return uri
}

// SaveStickerPDF writes the sticker sheet. pages are the painted pages as PNG
// data: URIs, one per A4 page; the user picks where the file goes, and it is
// then opened in the system's PDF viewer so printing is one step away. Returns
// the path, or "" when the user cancelled the dialog.
func (a *App) SaveStickerPDF(pages []string) (string, error) {
	// Decoded before the dialog opens, so a page that cannot be used never
	// asks the user for a file name first.
	imgs, err := decodeStickerPages(pages)
	if err != nil {
		return "", err
	}
	path, err := stickerSaveDialog(a.appCtx(), wailsrt.SaveDialogOptions{
		DefaultDirectory: arcadeSaveDir(),
		DefaultFilename:  stickerPDFName,
		Title:            "Save stickers",
		Filters:          []wailsrt.FileFilter{{DisplayName: "PDF document (*.pdf)", Pattern: "*.pdf"}},
	})
	if err != nil || path == "" {
		return "", err
	}
	if !strings.EqualFold(filepath.Ext(path), ".pdf") {
		path += ".pdf"
	}
	var buf bytes.Buffer
	if err := writeStickerPDF(&buf, imgs); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return "", err
	}
	if a.logger != nil {
		a.logger.Info("stickers: PDF written", "path", path, "pages", len(imgs), "bytes", buf.Len())
	}
	// The file is saved either way; a viewer that does not start is not a
	// failed save.
	if err := stickerOpenFile(path); err != nil && a.logger != nil {
		a.logger.Warn("stickers: could not open the PDF in a viewer", "path", path, "err", err)
	}
	return path, nil
}

// openWithSystemViewer opens a file in the program the system has for it.
func openWithSystemViewer(path string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", path).Start()
	case "windows":
		return exec.Command("explorer", filepath.FromSlash(path)).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}

// decodeStickerPages turns the window's page images into pixels and refuses
// anything that is not a PNG data: URI of a portrait A4 page.
func decodeStickerPages(pages []string) ([]image.Image, error) {
	if len(pages) == 0 {
		return nil, errors.New("no page to save")
	}
	if len(pages) > stickerMaxPages {
		return nil, fmt.Errorf("too many pages (%d, at most %d)", len(pages), stickerMaxPages)
	}
	const prefix = "data:image/png;base64,"
	out := make([]image.Image, 0, len(pages))
	for i, p := range pages {
		if !strings.HasPrefix(p, prefix) {
			return nil, fmt.Errorf("page %d is not a PNG image", i+1)
		}
		raw, err := base64.StdEncoding.DecodeString(p[len(prefix):])
		if err != nil {
			return nil, fmt.Errorf("page %d: %w", i+1, err)
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("page %d: %w", i+1, err)
		}
		if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > stickerMaxEdge || cfg.Height > stickerMaxEdge {
			return nil, fmt.Errorf("page %d has an unusable size (%d x %d)", i+1, cfg.Width, cfg.Height)
		}
		aspect := float64(cfg.Width) / float64(cfg.Height)
		if want := stickerPageW / stickerPageH; aspect < want*(1-stickerAspectSlack) || aspect > want*(1+stickerAspectSlack) {
			return nil, fmt.Errorf("page %d is not an A4 page (%d x %d)", i+1, cfg.Width, cfg.Height)
		}
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("page %d: %w", i+1, err)
		}
		out = append(out, img)
	}
	return out, nil
}

// writeStickerPDF writes one A4 page per image, the image filling the page.
//
// Objects: 1 the catalog, 2 the page tree, then three per page (the page, its
// content stream, its image). The cross-reference table at the end holds the
// byte offset of each, which is why every write goes through the counter.
func writeStickerPDF(w io.Writer, pages []image.Image) error {
	if len(pages) == 0 {
		return errors.New("no page to save")
	}
	cw := &countingWriter{w: w}
	offsets := make([]int, 2+3*len(pages))
	obj := func(n int, format string, args ...any) {
		offsets[n-1] = cw.n
		fmt.Fprintf(cw, "%d 0 obj\n", n)
		fmt.Fprintf(cw, format, args...)
		io.WriteString(cw, "\nendobj\n")
	}
	stream := func(n int, dict string, data []byte) {
		offsets[n-1] = cw.n
		fmt.Fprintf(cw, "%d 0 obj\n<< %s /Length %d >>\nstream\n", n, dict, len(data))
		cw.Write(data)
		io.WriteString(cw, "\nendstream\nendobj\n")
	}

	// The second line is a comment of four bytes above 127: it tells a
	// transfer program that the file is binary.
	io.WriteString(cw, "%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	kids := make([]string, len(pages))
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 3+3*i)
	}
	obj(2, "<< /Type /Pages /Count %d /Kids [%s] >>", len(pages), strings.Join(kids, " "))
	for i, img := range pages {
		pageObj, contentObj, imageObj := 3+3*i, 4+3*i, 5+3*i
		obj(pageObj, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
			stickerPageW, stickerPageH, imageObj, contentObj)
		// An image is one unit square; the matrix stretches it over the page.
		stream(contentObj, "", []byte(fmt.Sprintf("q %.2f 0 0 %.2f 0 0 cm /Im0 Do Q", stickerPageW, stickerPageH)))
		b := img.Bounds()
		data, err := deflate(rgbBytes(img))
		if err != nil {
			return err
		}
		stream(imageObj, fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /FlateDecode",
			b.Dx(), b.Dy()), data)
	}
	xref := cw.n
	fmt.Fprintf(cw, "xref\n0 %d\n", len(offsets)+1)
	// Every entry is exactly 20 bytes; readers index the table by that.
	io.WriteString(cw, "0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(cw, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(cw, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return cw.err
}

// countingWriter counts what went through and keeps the first error, so the
// PDF writer can check once at the end.
type countingWriter struct {
	w   io.Writer
	n   int
	err error
}

func (c *countingWriter) Write(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	n, err := c.w.Write(p)
	c.n += n
	c.err = err
	return n, err
}

// rgbBytes is the image as 8-bit RGB rows, which is what a DeviceRGB image
// holds. A PDF image has no alpha, so anything translucent goes onto white,
// the paper.
func rgbBytes(img image.Image) []byte {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := make([]byte, 0, w*h*3)
	switch src := img.(type) {
	case *image.NRGBA:
		// What a canvas page decodes to; read straight from the pixel slice.
		for y := 0; y < h; y++ {
			row := src.Pix[y*src.Stride : y*src.Stride+w*4]
			for x := 0; x < w; x++ {
				r, g, bl, a := uint32(row[x*4]), uint32(row[x*4+1]), uint32(row[x*4+2]), uint32(row[x*4+3])
				out = append(out, byte((r*a+255*(255-a))/255), byte((g*a+255*(255-a))/255), byte((bl*a+255*(255-a))/255))
			}
		}
	default:
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				// Premultiplied 16-bit channels: adding the uncovered share of
				// white composites onto the paper.
				r, g, bl, a := img.At(x, y).RGBA()
				rest := 0xffff - a
				out = append(out, byte((r+rest)>>8), byte((g+rest)>>8), byte((bl+rest)>>8))
			}
		}
	}
	return out
}

// deflate compresses an image stream the way /FlateDecode expects it.
func deflate(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
