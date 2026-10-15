package main

// Album art from a media server on the LAN, made drawable in the webview.
//
// A DLNA/UPnP media server (MinimServer, MiniDLNA, Synology, Plex, ...) hands
// out a track's cover as upnp:albumArtURI, and that is always a plain http://
// URL on a LAN address: "http://<server>:9790/minimserver/*/.../01.m4a/
// $!picture-...jpg". ResolveStationLogo only accepted https art, because the
// secure webview does not draw http images, so every music-library key fell
// back to its letter tile even though the server served a real cover and the
// speaker's own display showed it (#1301). Here the app fetches that one image
// itself, scales it down to tile size and hands the webview a data: URI.
//
// Scope is deliberately narrow: only http URLs on a private, link-local or
// .local host, which is where a media server lives. Public http favicons of
// radio stations keep going through the DuckDuckGo cascade as before.

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	_ "image/gif" // decoders for image.Decode
	"image/jpeg"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/image/draw"
)

const (
	// lanArtMaxBytes caps what is read from the server. Embedded covers are
	// often full-resolution scans (a 1424x1424 cover of 2.2 MB in #1301).
	lanArtMaxBytes = 12 << 20
	// lanArtEdge is the longest edge of the scaled tile image: a preset logo
	// is drawn at 32 to 56 CSS px, so 160 px stays sharp on a 2x screen.
	lanArtEdge = 160
	// lanArtRawMax is the largest undecodable image passed through unscaled
	// (a format Go has no decoder for, e.g. webp): bigger ones are dropped.
	lanArtRawMax = 512 << 10
)

// isLANArtURL reports whether u is an http image URL on a host that can only
// be a machine on the user's own network: a private (RFC 1918 / ULA) or
// link-local address, or an mDNS .local name.
func isLANArtURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(u.Scheme, "http") || u.Host == "" {
		return false
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsPrivate() || ip.IsLinkLocalUnicast()
	}
	return strings.HasSuffix(strings.ToLower(host), ".local")
}

// lanArtDataURI fetches a media server's album art and returns it as a small
// JPEG data: URI, or "" when the URL is not a LAN http URL, does not answer
// 200 with an image, or cannot be read.
func (a *App) lanArtDataURI(raw string) string {
	if !isLANArtURL(raw) {
		return ""
	}
	ctx, cancel := context.WithTimeout(a.appCtx(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(raw), nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "STReborn-Desktop")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	ctype := strings.ToLower(strings.TrimSpace(strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0]))
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(ctype, "image/") {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, lanArtMaxBytes+1))
	if err != nil || len(body) == 0 || len(body) > lanArtMaxBytes {
		return ""
	}
	return artDataURI(body, ctype)
}

// artDataURI turns image bytes into a tile-sized JPEG data: URI. An image Go
// cannot decode is passed through as it came when it is small enough.
func artDataURI(body []byte, ctype string) string {
	src, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		if len(body) > lanArtRawMax {
			return ""
		}
		return "data:" + ctype + ";base64," + base64.StdEncoding.EncodeToString(body)
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return ""
	}
	if w > lanArtEdge || h > lanArtEdge {
		if w >= h {
			h = max(1, h*lanArtEdge/w)
			w = lanArtEdge
		} else {
			w = max(1, w*lanArtEdge/h)
			h = lanArtEdge
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	// JPEG has no alpha: a transparent PNG cover goes onto white, not black.
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 85}); err != nil {
		return ""
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(out.Bytes())
}
