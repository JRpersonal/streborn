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

	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	// Bounded: a station logo is small, and an unbounded copy onto a speaker
	// with little memory is not worth the risk.
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 2<<20))
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
// everything else. The check sits in the DIALER rather than on the URL string,
// which is what makes it hold: it sees the address actually being connected
// to, so a hostname that resolves to 127.0.0.1, a redirect into the network,
// and an IPv6 or IPv4-mapped form of the same address are all caught, and none
// of them can be spelled around.
func publicOnlyControl(_ string, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("art proxy: unparseable address %q", address)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("art proxy: unresolved address %q", host)
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	switch {
	case ip.IsLoopback(), ip.IsPrivate(), ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(),
		ip.IsInterfaceLocalMulticast(), ip.IsMulticast(), ip.IsUnspecified():
		return fmt.Errorf("art proxy: refusing to fetch from %s (not a public address)", ip)
	}
	// Carrier-grade NAT (100.64.0.0/10). Not covered by IsPrivate, and it is
	// where a router's own management interface often lives.
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return fmt.Errorf("art proxy: refusing to fetch from %s (carrier-grade NAT range)", ip)
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
