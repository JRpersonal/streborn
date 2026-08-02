package main

// This file was split out of app.go (wave-1 move-only refactor):
// agent HTTP transport: base URLs, the per-host port cache, and boxDo with port fallback.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func (a *App) baseURL(host string, port int) string {
	// Default to the chipset-whitelisted hijack port. Classic frontend
	// callers that pre-discovery hard-coded 8888 still work because
	// they pass port=8888 explicitly; this fallback only kicks in for
	// freshly-resolved boxes where port was left zero.
	if port == 0 {
		port = 17008
	}
	if cp, ok := a.cachedPort(host); ok {
		port = cp
	}
	return fmt.Sprintf("http://%s:%d", host, port)
}

func (a *App) cachedPort(host string) (int, bool) {
	a.portMu.Lock()
	defer a.portMu.Unlock()
	p, ok := a.portCache[host]
	return p, ok
}

func (a *App) rememberPort(host string, port int) {
	a.portMu.Lock()
	defer a.portMu.Unlock()
	if a.portCache == nil {
		a.portCache = map[string]int{}
	}
	a.portCache[host] = port
}

func (a *App) forgetPort(host string) {
	a.portMu.Lock()
	defer a.portMu.Unlock()
	delete(a.portCache, host)
}

// altAgentPort returns the other agent port. The two are the STR agent's
// direct :8888 and the BCO chipset-whitelisted redirect :17008.
func altAgentPort(p int) int {
	if p == 8888 {
		return 17008
	}
	return 8888
}

// candidatePorts is the ordered, deduped list of agent ports to try for a
// host: the cached working port first (if any), then the caller's port,
// then the alternate. So the common case is one direct hit; a wrong/stale
// port costs one extra fast attempt and then self-corrects via the cache.
func (a *App) candidatePorts(host string, port int) []int {
	if port == 0 {
		port = 17008
	}
	order := make([]int, 0, 3)
	if cp, ok := a.cachedPort(host); ok {
		order = append(order, cp)
	}
	order = append(order, port, altAgentPort(port))
	seen := map[int]bool{}
	out := order[:0]
	for _, p := range order {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// zoneCallTimeout is the budget for forming or dissolving a zone. The agent
// wakes every member first (up to 8 s each) and only then talks to the Bose
// firmware, so the shared 6 s client cut the call off before the work started.
// Generous on purpose: a zone call is a deliberate user action that happens
// rarely, and a slow success beats a fast lie.
const zoneCallTimeout = 45 * time.Second

// boxDo performs an HTTP request against the agent with transparent port
// fallback. It tries each candidate port in turn; the first that connects
// is cached for the host and its response returned. A transport-level
// failure (connection refused, timeout, reset) drops the cached port and
// moves to the next candidate, so a box that changed which port it answers
// on (reboot, freeze, OTA) self-heals on the very next call. A non-
// transport error (a real HTTP response the caller must see) is returned
// immediately without flailing across ports. Caller closes resp.Body.
func (a *App) boxDo(host string, port int, method, path, contentType, body string) (*http.Response, error) {
	return a.boxDoTimeout(host, port, method, path, contentType, body, 0)
}

// boxDoTimeout is boxDo with a per-call deadline. A timeout of 0 uses the shared
// client's 6 s, which is right for the reads and small writes that make up
// nearly every call.
//
// It exists because a few agent endpoints are legitimately slower than that, and
// silently failing them is worse than waiting. Forming a zone is the case that
// exposed it: handleZoneForm calls ensureBoxReady first, which alone may spend
// 8 s waking a speaker out of standby before the firmware call even starts. The
// app gave up at 6 s and told the user the group could not be formed, while the
// box went on to form it. The user then tried again and got
// GROUP_ALREADY_EXISTS from a group that had been created by the attempt they
// were told had failed (#442, and the zone timeouts in the 2026-07-28 ST10
// bundle).
func (a *App) boxDoTimeout(host string, port int, method, path, contentType, body string, timeout time.Duration) (*http.Response, error) {
	client := a.httpClient
	if timeout > 0 {
		c := *a.httpClient
		c.Timeout = timeout
		client = &c
	}
	var lastErr error
	// stale404 holds a 404 that came from a port which is NOT the STR agent (the
	// Bose stock firmware answers unknown /api/ paths on :8090 with 404). It is
	// kept only as a fallback so a genuine agent 404 (a real missing resource) is
	// still surfaced when no better port answers.
	var stale404 *http.Response
	cands := a.candidatePorts(host, port)
	for i, p := range cands {
		url := fmt.Sprintf("http://%s:%d%s", host, p, path)
		var rdr io.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		}
		req, err := http.NewRequestWithContext(a.appCtx(), method, url, rdr)
		if err != nil {
			if stale404 != nil {
				stale404.Body.Close()
			}
			return nil, err
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := client.Do(req)
		if err == nil {
			// A 404 on an /api/ path means this port is not the STR agent: the
			// Bose firmware on :8090 answers unknown /api/ paths with 404, and
			// caching :8090 here made a post-OTA name/Wi-Fi write silently fail
			// (the box still carried its pre-install stock port). Skip the port
			// and try the next candidate; only fall back to the 404 if nothing
			// better answers, so a real agent 404 is not masked.
			if resp.StatusCode == http.StatusNotFound && strings.HasPrefix(path, "/api/") && i < len(cands)-1 {
				if stale404 != nil {
					stale404.Body.Close()
				}
				stale404 = resp
				a.forgetPort(host)
				continue
			}
			a.rememberPort(host, p)
			if stale404 != nil {
				stale404.Body.Close()
			}
			return resp, nil
		}
		lastErr = err
		if !isTransportNotReady(err) {
			if stale404 != nil {
				return stale404, nil
			}
			return nil, err
		}
		a.forgetPort(host)
	}
	if stale404 != nil {
		return stale404, nil
	}
	return nil, reachabilityHint(lastErr)
}

// reachabilityHint turns a bare "cannot reach the speaker" connection error
// (every candidate port timed out or refused) into an actionable one by naming
// the two things that most often cause it: a firewall or antivirus blocking ST
// Reborn's own network access, or this PC and the speaker being on different
// Wi-Fi networks. A user (2026-07-11) hit exactly this - the app timed out to
// BOTH github.com and every speaker port while their browser downloaded fine,
// i.e. a security suite was filtering the app, not the network. Wrapped with %w so
// errors.Is and callers that match the original text still work; a cancelled
// context (app shutdown) is returned unchanged so it never shows the hint.
func reachabilityHint(err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return err
	}
	return fmt.Errorf("%w\n\nThe app could not reach the speaker. This is usually a firewall or antivirus blocking ST Reborn, or this PC and the speaker being on different Wi-Fi networks. Allow ST Reborn through your firewall/antivirus (or turn it off briefly to test), and make sure both are on the same Wi-Fi network", err)
}
