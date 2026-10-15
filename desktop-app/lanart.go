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
	"errors"
	"fmt"
	"image"
	_ "image/gif" // decoders for image.Decode
	"image/jpeg"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
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

// lanArtTimeout bounds one cover fetch, the whole request including the body.
// It has its own client (lanArtClient) because a.httpClient carries a shorter
// overall timeout that silently cut this one short before it could apply.
const lanArtTimeout = 8 * time.Second

// lanArtMissTTL is how long ResolveStationLogo answers "" for a cover that
// failed before it asks the media server again.
const lanArtMissTTL = 60 * time.Second

// lanArtClient fetches media-server covers. No client-level timeout: the
// per-request context (lanArtTimeout) is the one that applies.
var lanArtClient = &http.Client{}

// lanArtLogMax bounds how many distinct failing cover URLs are remembered for
// log de-duplication, so a library with thousands of broken covers cannot grow
// the set (or the log) without limit.
const lanArtLogMax = 256

// lanArtLogged remembers which failing cover URLs were already logged.
type lanArtLogged struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

// first reports whether url was not logged before, and records it. Once the
// set is full nothing new is recorded and nothing more is logged.
func (l *lanArtLogged) first(url string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen == nil {
		l.seen = map[string]struct{}{}
	}
	if _, ok := l.seen[url]; ok || len(l.seen) >= lanArtLogMax {
		return false
	}
	l.seen[url] = struct{}{}
	return true
}

// lanArtDataURI fetches a media server's album art and returns it as a small
// JPEG data: URI, or "" when the URL is not a LAN http URL, does not answer
// 200 with an image, or cannot be read. Every failure is logged once per URL
// with its reason: a key that showed its letter tile instead of the cover could
// not be explained from a diagnostic bundle before (#1301).
func (a *App) lanArtDataURI(raw string) string {
	if !isLANArtURL(raw) {
		return ""
	}
	raw = strings.TrimSpace(raw)
	ctx, cancel := context.WithTimeout(a.appCtx(), lanArtTimeout)
	defer cancel()
	uri, info, err := fetchLANArt(ctx, lanArtClient, raw)
	if a.logger == nil {
		return uri
	}
	if err != nil {
		if a.lanArtLog.first(raw) {
			a.logger.Info("library cover: not shown", "url", raw, "reason", err.Error(),
				"status", info.status, "contentType", info.ctype, "bytes", info.size)
		}
		return ""
	}
	a.logger.Debug("library cover: fetched", "url", raw, "contentType", info.ctype, "bytes", info.size)
	return uri
}

// lanArtInfo is what a cover fetch learned about the response, for the log.
type lanArtInfo struct {
	status int
	ctype  string
	size   int
}

// fetchLANArt GETs url with client and turns the answer into a tile-sized data:
// URI. The error names why no cover came out of it. It does no LAN check of its
// own (lanArtDataURI does), so tests can point it at a loopback server.
func fetchLANArt(ctx context.Context, client *http.Client, url string) (string, lanArtInfo, error) {
	var info lanArtInfo
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", info, fmt.Errorf("bad url: %w", err)
	}
	req.Header.Set("User-Agent", "STReborn-Desktop")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", info, fmt.Errorf("no answer in time: %w", err)
		}
		return "", info, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	info.status = resp.StatusCode
	info.ctype = strings.ToLower(strings.TrimSpace(strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0]))
	if resp.StatusCode != http.StatusOK {
		return "", info, fmt.Errorf("server answered HTTP %d", resp.StatusCode)
	}
	if !strings.HasPrefix(info.ctype, "image/") {
		return "", info, fmt.Errorf("not an image (content type %q)", info.ctype)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, lanArtMaxBytes+1))
	info.size = len(body)
	if err != nil {
		if ctx.Err() != nil {
			return "", info, fmt.Errorf("no complete image in time: %w", err)
		}
		return "", info, fmt.Errorf("read failed: %w", err)
	}
	if len(body) == 0 {
		return "", info, errors.New("empty body")
	}
	if len(body) > lanArtMaxBytes {
		return "", info, fmt.Errorf("image larger than %d bytes", lanArtMaxBytes)
	}
	uri, err := artDataURIErr(body, info.ctype)
	if err != nil {
		return "", info, err
	}
	return uri, info, nil
}

// artDataURI turns image bytes into a tile-sized JPEG data: URI. An image Go
// cannot decode is passed through as it came when it is small enough.
func artDataURI(body []byte, ctype string) string {
	uri, _ := artDataURIErr(body, ctype)
	return uri
}

// artDataURIErr is artDataURI with the reason when no URI comes out.
func artDataURIErr(body []byte, ctype string) (string, error) {
	src, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		if len(body) > lanArtRawMax {
			return "", fmt.Errorf("cannot decode %s image of %d bytes, too big to pass through: %w", ctype, len(body), err)
		}
		return "data:" + ctype + ";base64," + base64.StdEncoding.EncodeToString(body), nil
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return "", errors.New("image has no pixels")
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
		return "", fmt.Errorf("jpeg encode: %w", err)
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(out.Bytes()), nil
}
