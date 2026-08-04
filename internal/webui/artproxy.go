package webui

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
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
	resp, err := http.DefaultClient.Do(req)
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
	ct := resp.Header.Get("Content-Type")
	if ct == "" || !strings.HasPrefix(ct, "image/") {
		ct = "image/png"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	// Bounded: a station logo is small, and an unbounded copy onto a speaker
	// with little memory is not worth the risk.
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 2<<20))
}
