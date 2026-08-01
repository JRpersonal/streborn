// failures.go: upstream failure handling — WARN dedup, failure classification
// and recording, the outbound-connectivity probe, and the stream-status
// endpoint the desktop app polls.

package streamproxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// failLogSuppressWindow is the minimum spacing between identical
// "upstream fail" warnings for the same URL. Bose's UPnP player
// re-hits the proxy when a station is unreachable, so without this
// the agent log fills with the same NXDOMAIN line several times a
// minute for a single dead preset.
const failLogSuppressWindow = 30 * time.Second

// shouldLogFail returns true if a fresh WARN line should be emitted
// for this URL. Within failLogSuppressWindow of the previous emit it
// returns false so the agent log does not repeat the same
// "upstream fail" line every time Bose's UPnP player retries an
// unreachable station.
func (s *Server) shouldLogFail(url string) bool {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	now := time.Now()
	if last, ok := s.lastFail[url]; ok && now.Sub(last) < failLogSuppressWindow {
		return false
	}
	s.lastFail[url] = now
	return true
}

// upstreamStatusError carries the upstream HTTP status of a non-200 response so
// the reconnect loops can tell a permanent client-side rejection (403 geo-block,
// 404/410 gone) from a transient one (5xx) via errors.As, instead of parsing a
// formatted string.
type upstreamStatusError struct {
	Code   int
	Status string
}

func (e *upstreamStatusError) Error() string {
	if e.Status != "" {
		return "upstream status " + e.Status
	}
	return fmt.Sprintf("upstream status %d", e.Code)
}

// isPermanentUpstream reports whether err is an upstream failure that retrying
// the SAME URL cannot fix: a client-side HTTP rejection (forbidden, not found,
// gone, unavailable-for-legal-reasons) or an HLS/DASH playlist we cannot play.
// For these the reconnect loop gives up immediately so the desktop app can fall
// back to another radio-browser entry of the station within a second instead of
// after a 30s retry storm against a URL that will never serve audio.
func isPermanentUpstream(err error) bool {
	var se *upstreamStatusError
	if errors.As(err, &se) {
		switch se.Code {
		case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
			http.StatusNotFound, http.StatusGone, http.StatusUnavailableForLegalReasons:
			return true
		}
	}
	return false
}

// streamFailure is the most recent terminal upstream error, surfaced via
// /api/stream-status. url is the upstream stream URL (not the proxy wrapper) so
// the app can confirm the error belongs to the station it just started.
type streamFailure struct {
	when   time.Time
	code   int    // upstream HTTP status, 0 for a network-level failure
	reason string // coarse class the UI maps to a message: blocked|gone|unavailable|unreachable|hls
	url    string
}

// classifyFailure maps an upstream error to a coarse reason the desktop app
// turns into a localized, human message. Network errors (DNS, refused, reset)
// land in "unreachable"; HTTP statuses split into blocked/gone/unavailable.
func classifyFailure(err error) (code int, reason string) {
	var se *upstreamStatusError
	if errors.As(err, &se) {
		switch se.Code {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusUnavailableForLegalReasons:
			return se.Code, "blocked"
		case http.StatusNotFound, http.StatusGone:
			return se.Code, "gone"
		default:
			// Everything else (5xx, and any other non-200) is a transient
			// "currently unavailable" from the station's side.
			return se.Code, "unavailable"
		}
	}
	if strings.Contains(strings.ToLower(errStr(err)), "hls") || strings.Contains(strings.ToLower(errStr(err)), "dash") {
		return 0, "hls"
	}
	return 0, "unreachable"
}

// recordFailure stores the latest terminal upstream failure for url so the
// desktop app can poll it and react (message + alternative-source fallback).
func (s *Server) recordFailure(url string, err error) {
	code, reason := classifyFailure(err)
	s.errMu.Lock()
	s.lastErr = streamFailure{when: time.Now(), code: code, reason: reason, url: url}
	s.errMu.Unlock()
}

// boxHasOutbound reports whether the speaker itself can reach the public
// internet, cached for a few seconds. It lets handleStreamStatus tell a single
// unreachable station apart from the speaker being offline entirely (#375): a
// box on a dead Wi-Fi fails EVERY station with a network-level error, which
// otherwise reads as each station being individually broken. Dials a couple of
// well-known hosts by NAME on :443 (so DNS, which radio also needs, is part of
// the test) plus a raw resolver IP; any success means online.
func (s *Server) boxHasOutbound() bool {
	s.netMu.Lock()
	if !s.netCheckAt.IsZero() && time.Since(s.netCheckAt) < 8*time.Second {
		v := s.netOnline
		s.netMu.Unlock()
		return v
	}
	s.netMu.Unlock()
	online := false
	for _, h := range []string{"www.google.com:443", "www.cloudflare.com:443", "1.1.1.1:53"} {
		c, err := net.DialTimeout("tcp", h, 2*time.Second)
		if err == nil {
			_ = c.Close()
			online = true
			break
		}
	}
	s.netMu.Lock()
	s.netOnline = online
	s.netCheckAt = time.Now()
	s.netMu.Unlock()
	return online
}

// clearFailure drops any recorded failure for url once it streams successfully,
// so a recovered station does not keep reporting a stale error to the app.
func (s *Server) clearFailure(url string) {
	s.errMu.Lock()
	if s.lastErr.url == url {
		s.lastErr = streamFailure{}
	}
	s.errMu.Unlock()
}

// handleStreamStatus reports the most recent terminal upstream failure as JSON:
//
//	{"error":true,"status":403,"reason":"blocked","url":"http://...","ageMs":1200}
//
// or {"error":false} when the last start streamed fine or is too old to matter.
// The desktop app polls this for a few seconds after starting a station: a fresh
// error tells it which message to show and that it should try another
// radio-browser entry of the same station. Only failures younger than
// streamStatusTTL are reported so a long-past error never blocks a fresh play.
func (s *Server) handleStreamStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	// boxState surfaces a SPEAKER-side condition ("wedged", "login-error")
	// that makes every station fail, so the app can say what is actually
	// wrong instead of blaming the station and cycling radio-browser
	// alternates ("Sender spielt nicht ... suche andere Quelle" while the box
	// was rejecting sources as not-logged-in). Additive: absent when the box
	// is fine, ignored by older frontends.
	boxState := ""
	if s.boxStateFn != nil {
		boxState = s.boxStateFn()
	}
	s.errMu.Lock()
	f := s.lastErr
	s.errMu.Unlock()
	if f.when.IsZero() || time.Since(f.when) > streamStatusTTL {
		if boxState != "" {
			fmt.Fprintf(w, `{"error":false,"boxState":%q}`, boxState)
			return
		}
		fmt.Fprint(w, `{"error":false}`)
		return
	}
	// A network-level "unreachable" is ambiguous: the one station may be down,
	// or the speaker has no internet at all. When outbound is also down, report a
	// distinct "offline" reason so the app can say "the speaker has no internet
	// connection" and offer to re-run Wi-Fi setup, instead of blaming every
	// station in turn (#375). Only checked on the "unreachable" catch-all, so a
	// clear 403/blocked/gone is never masked and the probe stays off the hot path.
	if f.reason == "unreachable" && !s.boxHasOutbound() {
		f.reason = "offline"
	}
	body, err := json.Marshal(struct {
		Error    bool   `json:"error"`
		Status   int    `json:"status"`
		Reason   string `json:"reason"`
		URL      string `json:"url"`
		AgeMs    int64  `json:"ageMs"`
		BoxState string `json:"boxState,omitempty"`
	}{
		Error:    true,
		Status:   f.code,
		Reason:   f.reason,
		URL:      f.url,
		AgeMs:    time.Since(f.when).Milliseconds(),
		BoxState: boxState,
	})
	if err != nil {
		fmt.Fprint(w, `{"error":false}`)
		return
	}
	w.Write(body)
}

// streamStatusTTL bounds how long a recorded upstream failure is reported. Long
// enough for the app's post-play poll window, short enough that a stale error
// from minutes ago never suppresses a fresh, healthy station.
const streamStatusTTL = 20 * time.Second

// errStr renders an error for a structured log field, empty when nil so
// a clean stream end does not log a misleading "lastErr=<nil>".
func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
