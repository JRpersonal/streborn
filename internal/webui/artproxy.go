package webui

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Station artwork proxy.
//
// The speaker fetches the station image itself, and this firmware cannot do
// HTTPS: that is the same reason audio goes through the stream proxy. Almost
// every station logo STR knows is an https:// URL, so handing one to the
// speaker means it silently fails to load and the display falls back to the
// service icon.
//
// This serves the image over plain HTTP on the loopback address the speaker
// already fetches its audio from, so the artwork has the same chance of
// arriving as the audio does.

const artProxyPath = "/art"

var (
	artLogMu  sync.Mutex
	artLogged = map[string]bool{}
)

// ArtProxyURL wraps an image URL so the speaker can fetch it over plain HTTP.
// Returns "" for an empty input, and passes a plain-HTTP URL through unchanged
// (nothing to gain from a second hop).
func ArtProxyURL(base, imageURL string) string {
	imageURL = strings.TrimSpace(imageURL)
	if imageURL == "" {
		return ""
	}
	if strings.HasPrefix(imageURL, "http://") {
		return imageURL
	}
	return strings.TrimSuffix(base, "/") + artProxyPath + "?u=" +
		base64.RawURLEncoding.EncodeToString([]byte(imageURL))
}

// handleArt fetches the upstream image and streams it back.
func (s *Server) handleArt(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("u")
	if raw == "" {
		http.Error(w, "u parameter required", http.StatusBadRequest)
		return
	}
	dec, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		if dec, err = base64.URLEncoding.DecodeString(raw); err != nil {
			http.Error(w, "u is not base64", http.StatusBadRequest)
			return
		}
	}
	target := string(dec)
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		http.Error(w, "only http(s) images", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		http.Error(w, "bad image url", http.StatusBadRequest)
		return
	}
	resp, err := artFetchClient.Do(req)
	if err != nil {
		s.logger.Info("art proxy: image fetch failed", "url", target, "err", err)
		http.Error(w, "image unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		s.logger.Info("art proxy: image refused", "url", target, "status", resp.StatusCode)
		http.Error(w, "image unavailable", http.StatusBadGateway)
		return
	}
	// An image or nothing. Passing anything else through would turn this into a
	// general-purpose proxy sitting on the speaker.
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") {
		s.logger.Info("art proxy: refusing a non-image response", "url", target, "contentType", ct)
		http.Error(w, "not an image", http.StatusBadGateway)
		return
	}
	// Log the first fetch per image, once. A successful fetch used to log
	// nothing at all, so the log could not answer the one question that
	// matters when a station logo does not show up: did the speaker even ask?
	// Without this, "no lines about /art" reads as "never requested" when it
	// may equally mean "requested and served fine" (field report 2026-08-05,
	// the logo above the station name gone). Rate-limited per URL so a speaker
	// that re-fetches on every track change cannot fill the NAND log.
	artLogMu.Lock()
	first := !artLogged[target]
	if first {
		if len(artLogged) > 64 {
			artLogged = map[string]bool{}
		}
		artLogged[target] = true
	}
	artLogMu.Unlock()
	if first {
		s.logger.Info("art proxy: the speaker fetched a station logo",
			"url", target, "contentType", ct, "status", resp.StatusCode)
	}

	// Bounded: a station logo is small, and an unbounded copy onto a speaker
	// with little memory is not worth the risk.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil || len(body) == 0 {
		s.logger.Info("art proxy: image body could not be read", "url", target, "err", err)
		http.Error(w, "image unreadable", http.StatusBadGateway)
		return
	}

	out, outCT, note := drawableImage(body)
	if note != "" && first {
		s.logger.Info("art proxy: substituted the station logo", "url", target, "reason", note)
	}
	w.Header().Set("Content-Type", outCT)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(out)
}

// drawableImage returns the bytes to serve for a fetched logo, their content
// type, and a non-empty note when the original had to be replaced.
//
// The speaker's display draws ordinary raster formats and nothing else, so
// something has to decide whether a logo is usable. That decision used to be
// made from the URL's file extension, at preset-save time, because sniffing
// each candidate would have meant a network round trip per station. It was
// wrong often enough to matter: the icon service STR falls back to serves
// PNG bytes under a .ico URL, so stations whose only logo came from it
// (Sunshine Live among them) had a perfectly drawable picture thrown away and
// replaced with STR's own logo. Reported 2026-08-07 by an owner who could
// compare a not-yet-updated speaker side by side.
//
// The proxy already fetches the image, so the evidence is right here and costs
// nothing extra. Deciding it from the bytes also holds the other way round: a
// .png URL that actually serves an SVG no longer reaches the display as
// something it cannot draw.
func drawableImage(body []byte) (out []byte, contentType, note string) {
	switch {
	case len(body) >= 8 && string(body[:8]) == "\x89PNG\r\n\x1a\n":
		return body, "image/png", ""
	case len(body) >= 3 && body[0] == 0xFF && body[1] == 0xD8 && body[2] == 0xFF:
		return body, "image/jpeg", ""
	case len(body) >= 6 && (string(body[:6]) == "GIF87a" || string(body[:6]) == "GIF89a"):
		return body, "image/gif", ""
	case len(body) >= 2 && body[0] == 'B' && body[1] == 'M':
		return body, "image/bmp", ""
	case len(body) >= 12 && string(body[:4]) == "RIFF" && string(body[8:12]) == "WEBP":
		return body, "image/webp", ""
	}
	// An .ico is a container, and since Vista its entries are very often whole
	// PNGs. Handing the container to the display shows nothing; the PNG inside
	// it draws fine, so unwrap it rather than give up on the station's own logo.
	if png := pngInsideICO(body); png != nil {
		return png, "image/png", ""
	}
	// SVG, an .ico holding only old-style bitmaps, or something unrecognised.
	// STR's logo stands in, because an empty tile next to a station name reads
	// as a fault rather than as a station having no picture.
	return iconPNG, "image/png", "not a format the display can draw"
}

// pngInsideICO returns the largest PNG embedded in an ICO container, or nil.
//
// Layout: a 6-byte header (reserved, type=1, image count) followed by one
// 16-byte directory entry per image, each ending in the entry's byte length
// and offset. Entries are either a PNG or an old-style bitmap; only the former
// is useful here, and there is no need to decode it, just to find it.
func pngInsideICO(b []byte) []byte {
	const hdr, entry = 6, 16
	if len(b) < hdr || b[0] != 0 || b[1] != 0 || b[2] != 1 || b[3] != 0 {
		return nil // not an ICO
	}
	count := int(b[4]) | int(b[5])<<8
	if count <= 0 || len(b) < hdr+count*entry {
		return nil
	}
	var best []byte
	for i := 0; i < count; i++ {
		e := b[hdr+i*entry:]
		size := int(e[8]) | int(e[9])<<8 | int(e[10])<<16 | int(e[11])<<24
		off := int(e[12]) | int(e[13])<<8 | int(e[14])<<16 | int(e[15])<<24
		if size <= 0 || off < 0 || off > len(b) || off+size > len(b) {
			continue
		}
		img := b[off : off+size]
		if len(img) >= 8 && string(img[:8]) == "\x89PNG\r\n\x1a\n" && len(img) > len(best) {
			best = img
		}
	}
	return best
}

// The address check, and why this endpoint needs one.
//
// The target URL arrives in a query parameter, so anything that can reach the
// agent's port can ask the SPEAKER to fetch a URL of its choosing. Left open
// that is a server-side request forgery: the speaker sits inside the user's
// network and can reach things the caller cannot, including its own loopback,
// where the Bose firmware answers on :8090 with endpoints that act on a plain
// GET (/removeGroup among them). Reported by CodeQL against this file on the
// day it was written.
//
// Station artwork lives on the public internet, so the fix is simply to refuse
// everything else. The preset-URL probe reuses this guard for the same reason,
// which is why the messages name the check and not one caller. The check sits in the DIALER rather than on the URL string,
// which is what makes it hold: it sees the address actually being connected
// to, so a hostname that resolves to 127.0.0.1, a redirect into the network,
// and an IPv6 or IPv4-mapped form of the same address are all caught, and none
// of them can be spelled around.
func publicOnlyControl(_ string, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("public-only dial: unparseable address %q", address)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("public-only dial: unresolved address %q", host)
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	switch {
	case ip.IsLoopback(), ip.IsPrivate(), ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(),
		ip.IsInterfaceLocalMulticast(), ip.IsMulticast(), ip.IsUnspecified():
		return fmt.Errorf("public-only dial: refusing %s (not a public address)", ip)
	}
	// Carrier-grade NAT (100.64.0.0/10). Not covered by IsPrivate, and it is
	// where a router's own management interface often lives.
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return fmt.Errorf("public-only dial: refusing %s (carrier-grade NAT range)", ip)
	}
	return nil
}

// artFetchClient is the only client this file uses. Redirects are followed but
// capped, and every hop goes through the same dialer check, so a redirect
// cannot walk the fetch back into the network.
var artFetchClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   8 * time.Second,
			KeepAlive: 30 * time.Second,
			Control:   publicOnlyControl,
		}).DialContext,
		TLSHandshakeTimeout: 8 * time.Second,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 4 {
			return fmt.Errorf("art proxy: too many redirects")
		}
		return nil
	},
}
