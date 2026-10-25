package webui

import (
	"bytes"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestBrowserImageKeepsABitmapICO is #1467: the icon service answers the
// homepage of Exclusively Rush with an .ico holding only old-style bitmaps.
// The speaker's display cannot draw that and gets STR's logo, but the phone
// remote is a browser and must get the station's own icon, the one the desktop
// app shows.
func TestBrowserImageKeepsABitmapICO(t *testing.T) {
	ico := buildICO(pad([]byte("\x28\x00\x00\x00"), 40), pad([]byte("\x28\x00\x00\x00"), 80))
	out, ct, ok := browserImage(ico)
	if !ok {
		t.Fatal("a bitmap ICO was refused; the phone key would fall back instead of showing it")
	}
	if !bytes.Equal(out, ico) || ct != "image/x-icon" {
		t.Errorf("got %d bytes as %q, want the ICO untouched as image/x-icon", len(out), ct)
	}
	if bytes.Equal(out, iconPNG) {
		t.Error("STR's own logo stood in for the station's icon")
	}
}

// Raster images pass through exactly as for the display; an ICO carrying a
// PNG is unwrapped the same way.
func TestBrowserImagePassesRasterImages(t *testing.T) {
	for _, body := range [][]byte{pad(pngMagic, 32), pad(jpegMagic, 32), pad([]byte("GIF89a"), 32)} {
		out, _, ok := browserImage(body)
		if !ok || !bytes.Equal(out, body) {
			t.Errorf("raster image %q was not passed through", body[:4])
		}
	}
	inner := pad(pngMagic, 50)
	if out, ct, ok := browserImage(buildICO(inner)); !ok || ct != "image/png" || !bytes.Equal(out, inner) {
		t.Error("an ICO carrying a PNG was not unwrapped")
	}
}

// What a browser key cannot safely show is an error, never STR's logo: the
// phone then tries the next candidate or shows the plain text tile. SVG is
// refused on purpose, the agent's origin must not serve script-capable images.
func TestBrowserImageRefusesInsteadOfSubstituting(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		[]byte("not an image at all"),
		{0, 0, 1, 0, 0, 0}, // an ICO header announcing no images
		nil,
	} {
		if out, _, ok := browserImage(body); ok {
			t.Errorf("%q was served (%d bytes), want a refusal", body, len(out))
		}
	}
}

// TestArtProxyBrowserModeEndToEnd runs the handler: the same bitmap ICO is the
// STR logo for the speaker (no to=), and the station's icon for the phone
// (to=browser).
func TestArtProxyBrowserModeEndToEnd(t *testing.T) {
	ico := buildICO(pad([]byte("\x28\x00\x00\x00"), 40))
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/x-icon")
		_, _ = w.Write(ico)
	}))
	defer up.Close()
	// The guarded client refuses loopback, which is all httptest offers.
	saved := artFetchClient
	artFetchClient = up.Client()
	defer func() { artFetchClient = saved }()

	s := &Server{logger: slog.New(slog.DiscardHandler)}
	enc := base64.RawURLEncoding.EncodeToString([]byte(up.URL + "/ip3/you.radio.ico"))
	get := func(q string) ([]byte, *http.Response) {
		rec := httptest.NewRecorder()
		s.handleArt(rec, httptest.NewRequest(http.MethodGet, "/art?u="+enc+q, nil))
		res := rec.Result()
		b, _ := io.ReadAll(res.Body)
		return b, res
	}

	if b, res := get(""); res.StatusCode != http.StatusOK || !bytes.Equal(b, iconPNG) {
		t.Errorf("speaker: status %d, STR logo=%v; the display still needs the stand-in", res.StatusCode, bytes.Equal(b, iconPNG))
	}
	b, res := get("&to=browser")
	if res.StatusCode != http.StatusOK || !bytes.Equal(b, ico) {
		t.Fatalf("phone: status %d, got the ICO=%v, STR logo=%v", res.StatusCode, bytes.Equal(b, ico), bytes.Equal(b, iconPNG))
	}
	if ct := res.Header.Get("Content-Type"); ct != "image/x-icon" {
		t.Errorf("phone: content type %q, want image/x-icon", ct)
	}
	if res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("phone: nosniff missing")
	}
}

// The phone's fetch must not take the speaker's one log slot for a URL:
// otherwise a key the phone loaded first never logs the display's
// substitution, and the log reads as if the speaker got the icon untouched.
func TestArtProxyBrowserFetchKeepsSpeakerLogSlot(t *testing.T) {
	ico := buildICO(pad([]byte("\x28\x00\x00\x00"), 40))
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/x-icon")
		_, _ = w.Write(ico)
	}))
	defer up.Close()
	saved := artFetchClient
	artFetchClient = up.Client()
	defer func() { artFetchClient = saved }()

	var logBuf bytes.Buffer
	s := &Server{logger: slog.New(slog.NewTextHandler(&logBuf, nil))}
	enc := base64.RawURLEncoding.EncodeToString([]byte(up.URL + "/ip3/log-slot.example.ico"))
	for _, q := range []string{"&to=browser", ""} {
		rec := httptest.NewRecorder()
		s.handleArt(rec, httptest.NewRequest(http.MethodGet, "/art?u="+enc+q, nil))
	}
	if !bytes.Contains(logBuf.Bytes(), []byte("substituted the station logo")) {
		t.Errorf("speaker substitution not logged after a phone fetch:\n%s", logBuf.String())
	}
}
