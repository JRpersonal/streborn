package webui

// Is that URL a radio station, or the station's home page?
//
// A field bundle (2026-08-02) carried three presets whose stored URL was the
// station's WEBSITE, e.g. www.radiohamburg.de/. Those save without complaint,
// map onto a hardware key, and can never play: the box fetches the proxy, the
// proxy fetches a web page, and the user is left pressing a dead button with no
// idea why. The save is the only moment where that is cheap to catch.
//
// The check is deliberately ONE-SIDED. It refuses a save only on positive
// evidence that the URL serves a web page; every other outcome, including every
// error, allows the save. That asymmetry is the whole design:
//
//   - A station that is temporarily down, rate-limiting, or behind a slow CDN
//     must still be savable. Refusing there would be a far worse bug than the
//     one this fixes, and it would be intermittent, which is worse again.
//   - Legacy SHOUTcast v1 servers answer "ICY 200 OK" instead of an HTTP status
//     line. Go's net/http rejects that as malformed (the whole class of old
//     stations that PR #458 fixed in the stream proxy, which has its own
//     ICY-rewriting dialer this probe deliberately does not reuse). Here that
//     error simply reads as "inconclusive", so those stations keep saving.
//   - A playlist (.pls/.m3u/.m3u8) is a legitimate target that some servers
//     mislabel as text/html, so playlists skip the probe entirely.

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/netutil"
)

// presetProbeTimeout bounds the added latency of a preset save. A station's
// response headers arrive in well under a second; anything slower is treated as
// inconclusive rather than making the user wait.
const presetProbeTimeout = 4 * time.Second

// playlistExts are resolved elsewhere and are never a web page even when the
// server labels them like one.
var playlistExts = map[string]bool{".pls": true, ".m3u": true, ".m3u8": true, ".asx": true, ".xspf": true}

// probeClient dials PUBLIC addresses only, a stricter rule than the stream
// proxy's, which deliberately allows private LAN ranges so a user's own local
// Icecast or DLNA server keeps playing.
//
// The probe can afford the stricter rule precisely because it fails open: a
// preset pointing at a LAN stream comes back "could not tell" and saves exactly
// as before. Nothing legitimate is lost, and in exchange the save endpoint stops
// being a way for anyone who can reach the agent's port to make the SPEAKER
// probe arbitrary hosts inside the network. CodeQL flagged that
// (go/request-forgery) the first time this shipped, and it was right to.
//
// The check lives in the DIALER, not on the URL string: it sees the address
// actually being connected to, so a hostname resolving to 127.0.0.1, an
// IPv4-mapped IPv6 form, and a redirect back into the network are all caught.
var probeClient = &http.Client{
	Transport: &http.Transport{
		DialContext:         (&net.Dialer{Timeout: presetProbeTimeout, Control: publicOnlyControl}).DialContext,
		TLSHandshakeTimeout: presetProbeTimeout,
	},
	CheckRedirect: func(_ *http.Request, via []*http.Request) error {
		if len(via) >= 4 {
			return errors.New("preset probe: too many redirects")
		}
		return nil
	},
}

// presetProbeClient is a seam for the tests and nothing else. The real client
// refuses loopback, and a test server IS loopback, so without this every test
// would exercise the "unreachable, allow it" branch and prove nothing about the
// classification. Production always uses probeClient.
var presetProbeClient = func(timeout time.Duration) *http.Client {
	c := *probeClient
	c.Timeout = timeout
	return &c
}

// looksLikeWebPage reports whether url positively answers as a web page rather
// than as audio. False means "no, or we could not tell" - the caller must treat
// both the same way and allow the save.
func looksLikeWebPage(ctx context.Context, rawURL string) bool {
	if rawURL == "" || netutil.SafeHTTPURL(rawURL) != nil {
		return false // a bad scheme is caught by the existing gates, not here
	}
	if u, err := url.Parse(rawURL); err == nil {
		if playlistExts[strings.ToLower(path.Ext(u.Path))] {
			return false
		}
	}
	ctx, cancel := context.WithTimeout(ctx, presetProbeTimeout)
	defer cancel()
	// GET, not HEAD: plenty of Icecast and SHOUTcast servers answer HEAD with
	// 405 or with headers that do not match what a real listener gets. The body
	// is closed the moment the headers are in, so a live stream is not drained.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return false
	}
	// Some servers only send icy-metaint (and their true content type) when the
	// client identifies as a stream player.
	req.Header.Set("Icy-MetaData", "1")
	req.Header.Set("User-Agent", "STR/preset-check")
	resp, err := presetProbeClient(presetProbeTimeout).Do(req)
	if err != nil {
		return false // unreachable, malformed (ICY), TLS trouble: inconclusive
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return false // a 403/404/503 says nothing about what the URL normally is
	}
	// An audio content type, or ICY metadata, settles it: this is a stream.
	ct := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if resp.Header.Get("icy-metaint") != "" || resp.Header.Get("icy-name") != "" {
		return false
	}
	if ct != "" && !strings.HasPrefix(ct, "text/html") && !strings.HasPrefix(ct, "application/xhtml") {
		return false
	}
	if strings.HasPrefix(ct, "text/html") || strings.HasPrefix(ct, "application/xhtml") {
		return true
	}
	// No content type at all: sniff the first bytes. A web page starts with a
	// doctype or an html tag; audio does not.
	head := make([]byte, 512)
	n, _ := io.ReadFull(io.LimitReader(resp.Body, int64(len(head))), head)
	s := strings.ToLower(strings.TrimSpace(string(head[:n])))
	return strings.HasPrefix(s, "<!doctype html") || strings.HasPrefix(s, "<html")
}
