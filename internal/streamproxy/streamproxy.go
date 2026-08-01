// Package streamproxy wraps third-party radio streams in a stable URL that
// Bose's UPnP player can no longer let go of.
//
// Background: many modern radio stations (1LIVE, SWR3, Rock Antenne via
// streamonkey) answer with an HTTP 302 redirect to a CDN URL carrying a
// signed token. Bose's UPnP player does follow the redirect, but holds on
// to the per-token URL. When the token expires after a few hours, the CDN
// kills the connection — Bose registers that as "stream dead" and goes into
// INVALID_SOURCE. The user's impression: "the station stops playing after a
// while".
//
// With this proxy Bose always sees the same URL
// `http://127.0.0.1:8888/stream/<slot>`. The stick agent internally resolves
// the redirect to the CDN and streams the bytes through. When the CDN kills
// the connection (token expiry), the proxy reconnects IMMEDIATELY — Bose's
// TCP connection stays open, so Bose never notices a drop.
package streamproxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/netutil"
	"github.com/JRpersonal/streborn/internal/presets"
)

type Server struct {
	store  *presets.Store
	logger *slog.Logger
	client *http.Client

	failMu   sync.Mutex
	lastFail map[string]time.Time

	// errMu guards lastErr, the most recent terminal upstream failure for the
	// stream the box is (or just was) pulling. The desktop app polls
	// /api/stream-status right after starting a station so it can show a clear
	// reason ("this stream is blocked / unavailable") and automatically try
	// another radio-browser entry of the same station. Radio failures are
	// asynchronous: the box accepts the UPnP URL instantly, then the 403/503
	// only surfaces here when it pulls the bytes, so a pollable record is the
	// only way the app learns why nothing plays.
	errMu   sync.Mutex
	lastErr streamFailure

	// fetchMu guards lastFetch, the last time the BOX opened any proxied
	// stream (slot or raw). The wedge detector uses it to tell "the box never
	// even pulled the URL it accepted" (control stack wedged, needs a
	// power-cycle) apart from "the box pulled it and the station failed".
	// slotFetch records the same moment PER preset slot, stamped only after
	// the slot validated and the store served a preset. The hardware recall
	// verify keys its success signal off the slot it pushed: the global stamp
	// let ANY proxied fetch (another slot's reconnect, a zone follower, even a
	// 404) certify a failed recall as healthy at the first tick, so no retry
	// ran and wedge strikes were falsely cleared (#252: station on the
	// display, no audio, clean log).
	fetchMu   sync.Mutex
	lastFetch time.Time
	// openFetches counts proxied stream connections currently being served
	// (slot or raw). While one is open, LastActivity reports activity "now":
	// the box is demonstrably pulling audio this very moment, regardless of
	// how long ago the connection was OPENED.
	openFetches int
	slotFetch   [7]time.Time // index 1..6: when the box last OPENED the slot
	// slotFetchEnd / slotOpen make the per-slot signal liveness-aware: a
	// 36ms-2.4s fetch that dies in the box's re-login source bounce used to
	// satisfy "opened since the press" and certified a dead recall as healthy
	// (field bundles 2026-07-22). A recall counts as pulled only while a
	// connection is OPEN or after it served a sustained stretch.
	slotFetchEnd [7]time.Time
	slotOpen     [7]int

	// boxStateFn reports a speaker-side condition that makes every station
	// fail ("wedged", "login-error"; "" = fine). Wired to webui.BoxStateHint;
	// surfaced in /api/stream-status so the app can distinguish a box problem
	// from a station problem. nil-safe.
	boxStateFn func() string

	// netMu guards a briefly-cached verdict on whether the SPEAKER itself can
	// reach the public internet. It lets /api/stream-status tell "this one
	// station is unreachable" apart from "the speaker has no internet at all"
	// (a box that landed on a dead Wi-Fi fails EVERY station this way, #375).
	netMu      sync.Mutex
	netCheckAt time.Time
	netOnline  bool

	// brMu guards the detected bitrate of the stream currently being
	// proxied. We learn it from the upstream Icecast/Shoutcast "icy-br"
	// header (exact, instant) or, when that is absent, by measuring
	// steady-state throughput. radio-browser's catalogue bitrate is often
	// missing or wrong, so this real value is what the UI shows for
	// now-playing. measuredBr locks in the value per stream URL so an
	// internal reconnect (token expiry) reuses it instead of re-measuring
	// and producing a different number on every UI poll.
	brMu       sync.Mutex
	curBitrate int
	curURL     string
	measuredBr map[string]int

	// onDisconnect, if set, is called when the Bose renderer closes a stream.
	// The argument is the last upstream error (nil = upstream was healthy, so
	// the box dropped the stream itself). Used by the auto-re-push.
	onDisconnect func(upstreamErr error)

	// titleMu guards the live ICY StreamTitle of the stream being proxied.
	// We always request ICY metadata from the upstream, de-interleave it out
	// of the byte stream the box receives (so the box gets clean audio), and
	// surface the parsed StreamTitle here. curTitleURL pins the title to its
	// stream so a station switch clears a stale title instead of showing the
	// previous station's track.
	titleMu     sync.Mutex
	curTitle    string
	curTitleURL string
	// onTitle, if set, is called whenever the live StreamTitle changes to a
	// non-empty value. Used to push the radio track text to the box display.
	onTitle func(title string)

	// gapMu guards lastByteToBox, the wall-clock time the proxy last handed
	// audio bytes to the box on the current stream. The reconnect loops read it
	// at the top of each retry to log how long the box went without audio: a gap
	// over ~1s is the audible dropout users report (#185).
	gapMu         sync.Mutex
	lastByteToBox time.Time
}

// SetOnDisconnect registers a callback invoked whenever the box closes a
// proxied stream (raw or slot). Set once at wiring time.
func (s *Server) SetOnDisconnect(fn func(upstreamErr error)) { s.onDisconnect = fn }

func New(store *presets.Store, logger *slog.Logger) *Server {
	// The SSRF guard (loopback/link-local/metadata blocked after DNS
	// resolution) is applied to every dial, plain or TLS, so a malicious
	// radio-browser URL cannot point the box at its own loopback services or
	// cloud metadata.
	baseDialer := &net.Dialer{Control: netutil.DialGuardSSRF}
	// Legacy SHOUTcast v1 servers answer the stream request with an "ICY 200 OK"
	// status line instead of "HTTP/1.0 200 OK". Go's net/http rejects that as a
	// malformed response ("Received HTTP/0.9 when not allowed"), so those stations
	// never play on STR even though media players (and other radio-browser apps)
	// handle them fine - the whole class of old SHOUTcast stations was silently
	// broken (field: "Radio Studio D" http://...:8018/;). This dialer wraps the
	// plain-HTTP connection so the first response line's "ICY" prefix is rewritten
	// to "HTTP/1.0" before Go parses it; everything else (headers, ICY metadata,
	// audio) is untouched. HTTPS is left to dialTLS (SHOUTcast-over-TLS is rare).
	icyDial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, err := baseDialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &icyConn{Conn: raw, br: bufio.NewReader(raw)}, nil
	}
	// DialTLSContext handles HTTPS itself so the clock-tolerant verification has
	// the real dial host (including a bare-IP host, for which the client sends
	// no SNI and tls.ConnectionState.ServerName would be empty). It reuses the
	// SSRF-guarded dialer, then does the handshake with a per-connection config
	// carrying that host. TLSClientConfig/TLSHandshakeTimeout are ignored once
	// DialTLSContext is set, so the handshake deadline is applied here.
	dialTLS := func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		raw, err := baseDialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		conn := tls.Client(raw, clockTolerantTLSConfig(host))
		if err := conn.HandshakeContext(hctx); err != nil {
			_ = raw.Close()
			return nil, err
		}
		return conn, nil
	}
	return &Server{
		store:  store,
		logger: logger,
		// Our own client so we control redirect behaviour. The default is
		// follow up to 10 — fine for Streamonkey & co. No timeout: streams
		// are endless, we read until EOF.
		client: &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           icyDial,
				DialTLSContext:        dialTLS,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          100,
				IdleConnTimeout:       90 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
		},
		lastFail:   make(map[string]time.Time),
		measuredBr: make(map[string]int),
	}
}

func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("/stream/raw", s.handleRaw)
	mux.HandleFunc("/stream/", s.handle)
	mux.HandleFunc("/api/stream/bitrate", s.handleBitrate)
	mux.HandleFunc("/api/stream/title", s.handleTitle)
	mux.HandleFunc("/api/stream-status", s.handleStreamStatus)
}

// handleRaw streams an arbitrary URL through — used by the radio search
// play path so Bose's UPnP can receive HTTPS streams via us as well. The
// URL arrives as a ?u=<base64url> parameter.
func (s *Server) handleRaw(w http.ResponseWriter, r *http.Request) {
	defer s.noteFetchOpen()()
	enc := r.URL.Query().Get("u")
	if enc == "" {
		http.Error(w, "u missing", http.StatusBadRequest)
		return
	}
	decoded, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		// Fallback: maybe plain URL-encoded
		decoded = []byte(enc)
	}
	url := unwrapSelfProxy(string(decoded))
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		http.Error(w, "invalid url scheme", http.StatusBadRequest)
		return
	}
	if isDASHURL(url) {
		s.logger.Warn("stream proxy: DASH not supported yet, refusing instead of reconnect-looping", "url", url)
		s.recordFailure(url, fmt.Errorf("dash not supported"))
		http.Error(w, hlsNotPlayableMsg, http.StatusUnsupportedMediaType)
		return
	}
	if isHLSURL(url) {
		// HLS: follow the playlist and demux its segments into a continuous stream
		// for the box (#124). serveHLS only returns an error before it has written
		// any audio, so http.Error here is always valid (never mid-stream).
		if err := s.serveHLS(r.Context(), w, r, url); err != nil {
			s.logger.Warn("stream proxy: HLS playback failed", "url", url, "err", err)
			s.recordFailure(url, err)
			http.Error(w, hlsNotPlayableMsg, http.StatusUnsupportedMediaType)
		}
		return
	}
	s.logger.Info("stream proxy raw start", "url", url)
	start := time.Now()
	s.resetAudioGap()
	headersSent := false
	var lastErr error
	for attempt := 0; attempt < 60; attempt++ {
		if r.Context().Err() != nil {
			s.logger.Info("stream proxy end: client gone", "kind", "raw", "elapsed", time.Since(start).Round(time.Second).String())
			return
		}
		if attempt > 0 {
			if gap := s.audioGap(); gap > 1*time.Second {
				s.logger.Warn("stream proxy audio gap before reconnect", "kind", "raw",
					"attempt", attempt, "gapMs", gap.Milliseconds(), "lastErr", errStr(lastErr))
			}
			s.logger.Info("stream proxy reconnect", "kind", "raw", "attempt", attempt, "lastErr", errStr(lastErr))
			select {
			case <-time.After(500 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		var boseAlive bool
		boseAlive, lastErr = s.streamOne(r.Context(), w, r, url, !headersSent)
		if errors.Is(lastErr, errPlaylistIsHLS) && !headersSent {
			// The URL had no .m3u8 suffix but its body is an HLS playlist; demux
			// it (#252). serveHLS only errors before writing audio, so http.Error
			// stays valid here.
			if err := s.serveHLS(r.Context(), w, r, url); err != nil {
				s.logger.Warn("stream proxy: HLS (via content-type) playback failed", "kind", "raw", "url", url, "err", err)
				s.recordFailure(url, err)
				http.Error(w, hlsNotPlayableMsg, http.StatusUnsupportedMediaType)
			}
			return
		}
		if !boseAlive {
			// Bose closed the connection (station switch, standby) — a
			// normal end, distinct from the give-up case below.
			s.logger.Info("stream proxy end: bose disconnected", "kind", "raw", "elapsed", time.Since(start).Round(time.Second).String(), "lastErr", errStr(lastErr))
			if s.onDisconnect != nil {
				s.onDisconnect(lastErr)
			}
			return
		}
		if isPermanentUpstream(lastErr) {
			// A client-side rejection (403 geo-block, 404/410 gone) will not
			// change on retry. Stop now so the desktop app can fall back to
			// another radio-browser entry of the station instead of waiting out
			// a 30s retry storm against a URL that will never serve audio.
			s.logger.Info("stream proxy end: permanent upstream rejection, not retrying", "kind", "raw", "lastErr", errStr(lastErr))
			return
		}
		headersSent = true
	}
	// 60 reconnects exhausted: the box still wanted bytes but upstream
	// kept failing. A network error in lastErr points at the box's
	// outbound path (e.g. a flaky wired link) rather than the box itself.
	s.logger.Warn("stream proxy gave up reconnecting", "kind", "raw", "attempts", 60, "elapsed", time.Since(start).Round(time.Second).String(), "lastErr", errStr(lastErr))
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	defer s.noteFetchOpen()()
	slotStr := strings.TrimPrefix(r.URL.Path, "/stream/")
	slot, err := strconv.Atoi(slotStr)
	if err != nil || slot < 1 || slot > 6 {
		http.Error(w, "invalid slot", http.StatusBadRequest)
		return
	}
	p, ok := s.store.Get(slot)
	if !ok || p.StreamURL == "" {
		// Log before 404ing: the box fetching a slot the store cannot serve is
		// exactly the "preset button does nothing" symptom, and this used to be
		// the only branch in the recall chain with no trace at all (#252).
		s.logger.Warn("stream proxy: box fetched a slot with no playable preset",
			"slot", slot, "found", ok)
		http.Error(w, "no preset", http.StatusNotFound)
		return
	}
	p.StreamURL = s.resolvePresetURL(slot, p.StreamURL)
	s.noteSlotFetch(slot)
	defer s.noteSlotFetchDone(slot)
	s.logger.Info("stream proxy start", "slot", slot, "name", p.Name)

	if isDASHURL(p.StreamURL) {
		s.logger.Warn("stream proxy: DASH preset not supported yet, refusing", "slot", slot, "url", p.StreamURL)
		s.recordFailure(p.StreamURL, fmt.Errorf("dash not supported"))
		http.Error(w, hlsNotPlayableMsg, http.StatusUnsupportedMediaType)
		return
	}
	if isHLSURL(p.StreamURL) {
		// HLS preset: follow the playlist and demux to the box (#124).
		if err := s.serveHLS(r.Context(), w, r, p.StreamURL); err != nil {
			s.logger.Warn("stream proxy: HLS preset playback failed", "slot", slot, "url", p.StreamURL, "err", err)
			s.recordFailure(p.StreamURL, err)
			http.Error(w, hlsNotPlayableMsg, http.StatusUnsupportedMediaType)
		}
		return
	}

	// We do exactly one GET to the CDN and copy bytes to Bose. When the CDN
	// returns EOF (token expiry), we reconnect internally and keep streaming —
	// Bose's connection stays open. We have a generous retry budget, but on a
	// client disconnect (context cancel) we stop immediately — otherwise we
	// would charge into an endless loop against the CDN.
	start := time.Now()
	s.resetAudioGap()
	headersSent := false
	var lastErr error
	for attempt := 0; attempt < 60; attempt++ {
		// If Bose has closed the connection, bail out immediately.
		if r.Context().Err() != nil {
			s.logger.Info("stream proxy end: client gone", "slot", slot, "elapsed", time.Since(start).Round(time.Second).String())
			return
		}
		if attempt > 0 {
			if gap := s.audioGap(); gap > 1*time.Second {
				s.logger.Warn("stream proxy audio gap before reconnect", "slot", slot,
					"attempt", attempt, "gapMs", gap.Milliseconds(), "lastErr", errStr(lastErr))
			}
			s.logger.Info("stream proxy reconnect", "slot", slot, "attempt", attempt, "lastErr", errStr(lastErr))
			// Wait briefly so we do not overload the CDN with reconnects
			select {
			case <-time.After(500 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		// Fetch the current URL — the user might have changed the preset in
		// the meantime.
		cur, ok := s.store.Get(slot)
		if !ok || cur.StreamURL == "" {
			return
		}
		curURL := s.resolvePresetURL(slot, cur.StreamURL)
		boseAlive, err := s.streamOne(r.Context(), w, r, curURL, !headersSent)
		lastErr = err
		if errors.Is(err, errPlaylistIsHLS) && !headersSent {
			// A preset whose URL had no .m3u8 suffix but serves an HLS playlist
			// body — demux it (#252). serveHLS only errors before writing audio.
			if herr := s.serveHLS(r.Context(), w, r, curURL); herr != nil {
				s.logger.Warn("stream proxy: HLS (via content-type) preset playback failed", "slot", slot, "url", curURL, "err", herr)
				s.recordFailure(curURL, herr)
				http.Error(w, hlsNotPlayableMsg, http.StatusUnsupportedMediaType)
			}
			return
		}
		if !boseAlive {
			// Bose closed the connection (standby, station switch). A normal
			// end, kept clearly distinct from the give-up case below, so the
			// log can tell a box stop from an outbound problem.
			s.logger.Info("stream proxy end: bose disconnected", "slot", slot, "elapsed", time.Since(start).Round(time.Second).String(), "lastErr", errStr(err))
			if s.onDisconnect != nil {
				s.onDisconnect(err)
			}
			return
		}
		if isPermanentUpstream(lastErr) {
			s.logger.Info("stream proxy end: permanent upstream rejection, not retrying", "slot", slot, "lastErr", errStr(lastErr))
			return
		}
		headersSent = true
	}
	// 60 reconnects exhausted: the box still wanted bytes, but upstream
	// kept failing. A network error in lastErr points at the box's outbound
	// path (e.g. a flaky cable) rather than the box itself.
	s.logger.Warn("stream proxy gave up reconnecting", "slot", slot, "attempts", 60, "elapsed", time.Since(start).Round(time.Second).String(), "lastErr", errStr(lastErr))
}

// streamOne does one round trip to the upstream and copies the body to w.
// It returns boseAlive=true when the connection to Bose is still open (a
// reconnect makes sense), false when Bose has disconnected. The second
// return value is the last upstream error of this attempt (nil on a clean
// EOF or a normal Bose disconnect); the caller logs it at stream end so a
// box stop can be told apart from outbound problems.
func (s *Server) streamOne(ctx context.Context, w http.ResponseWriter, r *http.Request, url string, sendHeaders bool) (bool, error) {
	return s.streamOneDepth(ctx, w, r, url, sendHeaders, 0)
}

// streamOneDepth is streamOne with a playlist-resolution recursion guard: an
// audio/x-mpegurl response that turns out to be a plain M3U/PLS pointer file is
// re-fetched at its first real stream URL (depth+1), capped so a playlist that
// points at itself or at another playlist cannot loop forever.
func (s *Server) streamOneDepth(ctx context.Context, w http.ResponseWriter, r *http.Request, url string, sendHeaders bool, depth int) (bool, error) {
	if err := safeHTTPURL(url); err != nil {
		s.logger.Warn("stream proxy refusing url", "url", url, "err", err)
		if sendHeaders {
			http.Error(w, "invalid stream url", http.StatusBadRequest)
		}
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		s.logger.Warn("stream proxy NewRequest fail", "err", err)
		return false, err
	}
	// Always request ICY metadata from the upstream, regardless of what the
	// box asked for. STR owns the metadata: it de-interleaves it out of the
	// stream (so the box gets clean audio) and reads StreamTitle to drive the
	// now-playing text. The box never sees the icy-metaint contract.
	req.Header.Set("Icy-MetaData", "1")
	req.Header.Set("User-Agent", "STR-Proxy/1.0")

	resp, err := s.client.Do(req)
	if err != nil {
		// If Bose has closed the connection, a retry makes no sense.
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return false, nil
		}
		// Dedupe identical failures: Bose's UPnP player re-hits the
		// proxy when a station is unreachable, so the same NXDOMAIN
		// would otherwise spam the agent log.
		if s.shouldLogFail(url) {
			s.logger.Warn("stream proxy upstream fail", "url", url, "err", err)
		} else {
			s.logger.Debug("stream proxy upstream fail (dedup)", "url", url, "err", err)
		}
		s.recordFailure(url, err)
		if sendHeaders {
			http.Error(w, "upstream unreachable", http.StatusBadGateway)
			return false, err
		}
		// Headers already sent — try a reconnect
		return true, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		statusErr := &upstreamStatusError{Code: resp.StatusCode, Status: resp.Status}
		if s.shouldLogFail(url) {
			s.logger.Warn("stream proxy upstream status", "status", resp.StatusCode, "url", url)
		} else {
			s.logger.Debug("stream proxy upstream status (dedup)", "status", resp.StatusCode, "url", url)
		}
		s.recordFailure(url, statusErr)
		if sendHeaders {
			http.Error(w, "upstream status: "+resp.Status, http.StatusBadGateway)
			return false, statusErr
		}
		return true, statusErr
	}

	// HLS/DASH/playlist detected by MIME type (a URL without the telltale
	// suffix). Reading a segment playlist or a pointer file as audio yields
	// instant EOF and a reconnect storm, so classify it here, before any bytes
	// are written, and switch paths:
	//   - DASH (dash+xml): still unsupported -> report not-playable and stop.
	//   - HLS body (#EXT-X- markers): re-run through serveHLS, which demuxes it.
	//   - plain M3U/PLS pointer: resolve to its first real stream URL and play
	//     that (#252: Absolut Relax et al. serve audio/x-mpegurl on a URL with
	//     no .m3u suffix, so it never reached the .m3u8 HLS branch upstream).
	if ct := resp.Header.Get("Content-Type"); isHLSorDASHContentType(ct) {
		if strings.Contains(strings.ToLower(ct), "dash") {
			if s.shouldLogFail(url) {
				s.logger.Warn("stream proxy: DASH content-type not supported yet", "url", url, "contentType", ct)
			}
			dashErr := fmt.Errorf("dash not supported (content-type %q)", ct)
			s.recordFailure(url, dashErr)
			if sendHeaders {
				http.Error(w, hlsNotPlayableMsg, http.StatusUnsupportedMediaType)
			}
			return false, dashErr
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
		text := string(body)
		if strings.Contains(text, "#EXT-X-") {
			// A real HLS playlist reached the raw path (URL had no .m3u8 suffix).
			// Let the caller re-serve it through serveHLS.
			return false, errPlaylistIsHLS
		}
		if media := firstMediaURLFromPlaylist(text, url); media != "" && media != url && depth < 2 {
			s.logger.Info("stream proxy: resolved playlist pointer to stream URL",
				"playlist", url, "stream", media, "depth", depth+1)
			resp.Body.Close()
			return s.streamOneDepth(ctx, w, r, media, sendHeaders, depth+1)
		}
		if s.shouldLogFail(url) {
			s.logger.Warn("stream proxy: playlist content-type not resolvable", "url", url, "contentType", ct)
		}
		plErr := fmt.Errorf("playlist not resolvable (content-type %q)", ct)
		s.recordFailure(url, plErr)
		if sendHeaders {
			http.Error(w, hlsNotPlayableMsg, http.StatusUnsupportedMediaType)
		}
		return false, plErr
	}

	// Successful reach — clear any dedup entry so a future failure
	// for this URL produces a fresh WARN immediately, and drop any recorded
	// stream-status failure so the app stops reporting a now-recovered station.
	s.failMu.Lock()
	delete(s.lastFail, url)
	s.failMu.Unlock()
	s.clearFailure(url)

	// Capture the real stream bitrate from the icy-br header, if the
	// station sends one (most Icecast/Shoutcast do). A single header read
	// outside the copy loop. beginStream also reuses a value already
	// measured for this URL, so stations without the header only fall back
	// to the throughput measurement below on the very first play.
	icyBr := icyBitrate(resp.Header)
	knownBitrate := s.beginStream(url, icyBr)

	// ICY metadata spacing. When set, the upstream interleaves StreamTitle
	// blocks every metaint bytes.
	metaint := icyMetaint(resp.Header)
	s.clearTitleForNewURL(url)

	// Did the box itself ask for ICY metadata? If so it can de-interleave and
	// display StreamTitle natively, with no stream re-fetch (the gap-free path,
	// unlike re-issuing SetAVTransportURI which makes the box drop+reconnect).
	// In that case pass the interleaved bytes AND the icy-metaint header
	// through unchanged, and tee a parse so STR's /api/stream/title still
	// updates too. If the box did NOT ask, strip the metadata so it gets clean
	// audio (it would otherwise mistake metadata bytes for audio).
	boxICY := r.Header.Get("Icy-MetaData")
	boxWantsICY := boxICY != "" && boxICY != "0"
	s.logger.Info("stream proxy ICY negotiation", "boxWantsICY", boxWantsICY, "boxIcyMetaData", boxICY, "upstreamMetaint", metaint)

	if sendHeaders {
		for k, vv := range resp.Header {
			// Do not pass hop-by-hop headers through
			switch strings.ToLower(k) {
			case "connection", "transfer-encoding":
				continue
			// icy-metaint only reaches the box when the box asked for ICY and
			// will de-interleave it. When we strip (box did not ask), the
			// header must not leak or the box would treat metadata bytes as
			// audio and get corrupted sound.
			case "icy-metaint":
				if !boxWantsICY {
					continue
				}
			}
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
	}

	// Flush continuously so Bose's player does not wait on a buffer
	flusher, _ := w.(http.Flusher)
	// Throughput fallback for stations that send no icy-br and have not
	// been measured before. The box fills its decode buffer fast at the
	// start, so a measurement taken immediately reads far above the real
	// bitrate (e.g. 1300 for a 192k stream). We therefore skip the first
	// brSettle of buffer-fill, then average bytes/elapsed over brWindow of
	// steady-state playback (when bytes arrive at real-time = the true
	// bitrate), snap to the nearest standard rate, store it once, and stop.
	// Bounded to this single active stream: a few counters and one division.
	const (
		brSettle = 4 * time.Second
		brWindow = 6 * time.Second
	)
	streamStart := time.Now()
	var winBytes int64
	var winStart time.Time
	measured := knownBitrate
	// De-interleave ICY metadata ONLY when the box did not ask for it: src
	// then yields clean audio and each StreamTitle block updates STR's live
	// title. When the box DID ask (boxWantsICY), pass the interleaved stream
	// through untouched so the box de-interleaves and displays the track
	// itself, gap-free. Without metaint the body passes through unchanged.
	var src io.Reader = resp.Body
	if metaint > 0 && !boxWantsICY {
		src = newICYReader(resp.Body, metaint, func(meta string) {
			if title, ok := parseStreamTitle(meta); ok {
				s.setTitle(url, title)
			}
		})
	}
	buf := make([]byte, 16*1024)
	// Connection-scoped telemetry so a diagnostic bundle can explain a dropout
	// without a live capture: how long this upstream connection lasted and how
	// many bytes it delivered to the box before it ended (#185).
	connStart := time.Now()
	var connBytes int64
	gotData := false
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				// Bose closed the connection
				return false, nil
			}
			if flusher != nil {
				flusher.Flush()
			}
			connBytes += int64(n)
			gotData = true
			s.markByteDelivery() // records "last byte to box" wall clock across reconnects
			if !measured && time.Since(streamStart) >= brSettle {
				if winStart.IsZero() {
					// First read past the settle point: start the window
					// here, do not count this partial chunk.
					winStart = time.Now()
				} else {
					winBytes += int64(n)
					if el := time.Since(winStart); el >= brWindow {
						if secs := el.Seconds(); secs > 0 {
							raw := int(float64(winBytes) * 8 / 1000 / secs)
							if br := roundStandardBitrate(raw); br > 0 {
								s.rememberBitrate(url, br)
							}
						}
						measured = true
					}
				}
			}
		}
		if readErr != nil {
			// Bose has closed — NO retry, otherwise an endless loop
			if errors.Is(readErr, context.Canceled) || ctx.Err() != nil {
				return false, nil
			}
			if readErr == io.EOF {
				// Clean EOF (typically CDN token expiry). Expected; the reconnect
				// is gap-free if it lands fast. INFO, with timing so a bundle shows
				// how often a station forces a reconnect.
				s.logger.Info("stream proxy upstream EOF, will reconnect", "url", url,
					"connectedSec", int(time.Since(connStart).Seconds()), "bytes", connBytes, "delivered", gotData)
				return true, nil
			}
			// Network-level read error mid-stream: this is the dropout cause for
			// the #185 class. WARN, with how long the connection survived and how
			// much it delivered, so the bundle pins the drop without a capture.
			s.logger.Warn("stream proxy upstream read fail, will reconnect", "url", url, "err", readErr,
				"connectedSec", int(time.Since(connStart).Seconds()), "bytes", connBytes, "delivered", gotData)
			return true, readErr
		}
	}
}
