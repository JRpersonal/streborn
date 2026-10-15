package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// #1301: a media server's album art is a plain http URL on the LAN. Only that
// shape is fetched app-side; public http favicons stay with the old cascade.
func TestIsLANArtURL(t *testing.T) {
	cases := map[string]bool{
		"http://192.168.1.20:9790/minimserver/*/Music/a.m4a/$!picture-1-2.jpg": true,
		"http://10.0.0.5:8200/AlbumArt/12-34.jpg":                              true,
		"http://172.16.4.2/art.png":                                            true,
		"http://169.254.10.3/art.png":                                          true,
		"http://[fd00::12]:9000/art.jpg":                                       true,
		"http://nas.local:50002/art.jpg":                                       true,
		"https://192.168.1.20/art.jpg":                                         false, // https goes the direct route
		"http://203.0.113.7/favicon.ico":                                       false, // public host
		"http://radio.example.com/logo.png":                                    false,
		"http://127.0.0.1:8888/icon.png":                                       false, // box loopback stand-in
		"":                                                                     false,
		"data:image/png;base64,AAAA":                                           false,
	}
	for in, want := range cases {
		if got := isLANArtURL(in); got != want {
			t.Errorf("isLANArtURL(%q) = %v, want %v", in, got, want)
		}
	}
}

// A full-size cover comes back as a tile-sized JPEG, aspect kept.
func TestArtDataURIScalesDown(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1424, 712))
	for y := 0; y < 712; y++ {
		for x := 0; x < 1424; x++ {
			src.Set(x, y, color.RGBA{R: 200, G: 40, B: 40, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	uri := artDataURI(buf.Bytes(), "image/png")
	const prefix = "data:image/jpeg;base64,"
	if !strings.HasPrefix(uri, prefix) {
		t.Fatalf("not a JPEG data URI: %.40q", uri)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, prefix))
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != lanArtEdge || b.Dy() != lanArtEdge/2 {
		t.Errorf("scaled to %dx%d, want %dx%d", b.Dx(), b.Dy(), lanArtEdge, lanArtEdge/2)
	}
}

// Bytes Go cannot decode pass through only while small.
func TestArtDataURIUndecodable(t *testing.T) {
	small := []byte("RIFF....WEBPVP8 tiny")
	if got := artDataURI(small, "image/webp"); !strings.HasPrefix(got, "data:image/webp;base64,") {
		t.Errorf("small undecodable image not passed through: %.40q", got)
	}
	big := bytes.Repeat([]byte{1}, lanArtRawMax+1)
	if got := artDataURI(big, "image/webp"); got != "" {
		t.Error("a large undecodable image must be dropped")
	}
}

// pngBytes is a small solid PNG for the fetch tests.
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	src := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// #1301: every way a cover fails names its reason, so the diagnostic bundle
// can tell a 404 from a wrong content type, a slow server or a huge file.
func TestFetchLANArtFailureReasons(t *testing.T) {
	img := pngBytes(t, 4, 4)
	mux := http.NewServeMux()
	mux.HandleFunc("/html", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html>no</html>"))
	})
	mux.HandleFunc("/missing", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	mux.HandleFunc("/slow", func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(bytes.Repeat([]byte{0xff}, lanArtMaxBytes+10))
	})
	mux.HandleFunc("/empty", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
	})
	mux.HandleFunc("/broken", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(bytes.Repeat([]byte{0x01}, lanArtRawMax+10))
	})
	mux.HandleFunc("/ok.png", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(img)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cases := []struct {
		path, want string
		status     int
	}{
		{"/html", "not an image", http.StatusOK},
		{"/missing", "HTTP 404", http.StatusNotFound},
		{"/slow", "no answer in time", 0},
		{"/big", "larger than", http.StatusOK},
		{"/empty", "empty body", http.StatusOK},
		{"/broken", "cannot decode", http.StatusOK},
	}
	for _, c := range cases {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if c.path == "/slow" {
			cancel()
			ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
		}
		uri, info, err := fetchLANArt(ctx, lanArtClient, srv.URL+c.path)
		cancel()
		if uri != "" || err == nil {
			t.Errorf("%s: got uri=%.20q err=%v, want a failure", c.path, uri, err)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: reason %q does not mention %q", c.path, err, c.want)
		}
		if info.status != c.status {
			t.Errorf("%s: status %d, want %d", c.path, info.status, c.status)
		}
	}

	uri, info, err := fetchLANArt(context.Background(), lanArtClient, srv.URL+"/ok.png")
	if err != nil || !strings.HasPrefix(uri, "data:image/jpeg;base64,") {
		t.Fatalf("success: uri=%.30q err=%v", uri, err)
	}
	if info.ctype != "image/png" || info.size != len(img) {
		t.Errorf("success info = %+v, want image/png and %d bytes", info, len(img))
	}
}

// The cover fetch must not inherit a.httpClient's shorter overall timeout,
// which cut it off before its own 8 s could apply.
func TestLANArtClientHasNoOverallTimeout(t *testing.T) {
	if lanArtClient.Timeout != 0 {
		t.Errorf("lanArtClient.Timeout = %s, want 0 (the per-request %s applies)", lanArtClient.Timeout, lanArtTimeout)
	}
}

// A failure is logged once per URL, and the remembered set stays bounded.
func TestLANArtLoggedOncePerURL(t *testing.T) {
	var l lanArtLogged
	if !l.first("http://10.0.0.5/a.jpg") || l.first("http://10.0.0.5/a.jpg") {
		t.Fatal("the same URL must log exactly once")
	}
	for i := 0; i < lanArtLogMax*2; i++ {
		l.first(fmt.Sprintf("http://10.0.0.5/%d.jpg", i))
	}
	if len(l.seen) > lanArtLogMax {
		t.Errorf("remembered %d URLs, cap is %d", len(l.seen), lanArtLogMax)
	}
}

// roundTripFunc lets a test answer the cover fetch without a LAN server.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// #1301: one failed cover fetch must not leave the key without art for the
// whole app run. The miss is held only briefly, then the server is asked again
// and a cover that loads now is cached as before.
func TestResolveStationLogoLANArtMissNotCachedForever(t *testing.T) {
	img := pngBytes(t, 4, 4)
	calls := 0
	fail := true
	orig := lanArtClient
	lanArtClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if fail {
			return nil, errors.New("connection reset")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/png"}},
			Body:       io.NopCloser(bytes.NewReader(img)),
			Request:    r,
		}, nil
	})}
	defer func() { lanArtClient = orig }()

	var logged bytes.Buffer
	a := &App{logger: slog.New(slog.NewTextHandler(&logged, nil))}
	const art = "http://192.168.1.20:9790/minimserver/*/a.m4a/$!picture-1.jpg"
	if got := a.ResolveStationLogo(art, "", nil); got != "" {
		t.Fatalf("failed fetch returned %.30q", got)
	}
	if got := a.ResolveStationLogo(art, "", nil); got != "" || calls != 1 {
		t.Fatalf("within the miss window: got %.30q after %d fetches, want \"\" after 1", got, calls)
	}
	if !strings.Contains(logged.String(), "library cover: not shown") || !strings.Contains(logged.String(), "connection reset") {
		t.Errorf("failure not logged with its reason: %s", logged.String())
	}
	// The miss window has passed and the server answers now.
	fail = false
	a.logoMu.Lock()
	for k := range a.lanArtMiss {
		a.lanArtMiss[k] = time.Now().Add(-time.Second)
	}
	a.logoMu.Unlock()
	got := a.ResolveStationLogo(art, "", nil)
	if !strings.HasPrefix(got, "data:image/jpeg;base64,") || calls != 2 {
		t.Fatalf("after the miss window: got %.30q after %d fetches", got, calls)
	}
	// A cover that loaded is cached as before.
	if again := a.ResolveStationLogo(art, "", nil); again != got || calls != 2 {
		t.Errorf("success not cached: %d fetches", calls)
	}
}
