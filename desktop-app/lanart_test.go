package main

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
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
