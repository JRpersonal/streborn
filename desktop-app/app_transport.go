package main

// This file was split out of app.go (wave-1 move-only refactor):
// agent HTTP transport: base URLs, the per-host port cache, and boxDo with port fallback.

import (
	"bufio"
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

// altAgentPortFor is a seam. The two agent ports are fixed numbers, so a test
// that wants to exercise the fallback across two servers cannot use httptest,
// which hands out a random loopback port. Tests replace this to name a port
// they control; nothing in the app ever assigns to it.
var altAgentPortFor = altAgentPort

// notTheAgent reports whether a response plainly did not come from the STR
// agent, so the port that produced it must not be cached or trusted.
//
// This only ever fires on /api/ paths, which are ours alone: any other path may
// legitimately be served by whatever else is on the box.
//
// Two shapes are recognised, both observed in the field:
//
//   - 404. The Bose firmware on :8090 answers unknown /api/ paths with it, and
//     caching that port made a post-OTA name/Wi-Fi write silently fail.
//   - A 4xx with no body at all. Every refusal the agent itself produces goes
//     through http.Error and therefore carries a reason; a bare status line
//     with an empty body is a minimal firmware listener, not us. Field report
//     2026-08-07: a speaker answered `status 400 ... body=""` to every request
//     for two days across three app versions, because the first such 400 was
//     cached as the agent's port and boxDo then returned it immediately
//     without ever trying the other candidate. The agent was on the other one.
//
// A 5xx is deliberately NOT included: the agent does return bodiless 5xx in
// places, and a struggling agent is still the agent. Treating it as a stranger
// would send the app to the wrong port at exactly the wrong moment.
func notTheAgent(resp *http.Response, path string) bool {
	if resp == nil || !strings.HasPrefix(path, "/api/") {
		return false
	}
	if resp.StatusCode == http.StatusNotFound {
		return true
	}
	return resp.StatusCode >= 400 && resp.StatusCode < 500 && bodyIsEmpty(resp)
}

// bodyIsEmpty reports whether resp has no body, without consuming it: the
// response may still be handed back to the caller as the best answer we got.
// A body of unknown length is peeked one byte deep and the byte is put back.
func bodyIsEmpty(resp *http.Response) bool {
	if resp.ContentLength == 0 {
		return true
	}
	if resp.ContentLength > 0 || resp.Body == nil {
		return false
	}
	br := bufio.NewReader(resp.Body)
	_, err := br.Peek(1)
	resp.Body = readCloser{Reader: br, Closer: resp.Body}
	return err == io.EOF
}

// readCloser rejoins a buffered reader to the original body's Closer, so the
// peeked byte is still delivered and the connection is still released.
type readCloser struct {
	io.Reader
	io.Closer
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
	order = append(order, port, altAgentPortFor(port))
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
	// stranger holds the best answer from a port that is NOT the STR agent (see
	// notTheAgent). It is kept only as a fallback, so a genuine agent 404 or 400
	// is still surfaced when no other port answers at all, and it is never
	// cached: caching it is what let one bare 400 capture a host for two days.
	var stranger *http.Response
	cands := a.candidatePorts(host, port)
	for _, p := range cands {
		url := fmt.Sprintf("http://%s:%d%s", host, p, path)
		var rdr io.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		}
		req, err := http.NewRequestWithContext(a.appCtx(), method, url, rdr)
		if err != nil {
			if stranger != nil {
				stranger.Body.Close()
			}
			return nil, err
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := client.Do(req)
		if err == nil {
			// Something answered, but not necessarily us. A port that is
			// demonstrably not the agent must neither be cached nor end the
			// search, or the real agent on the other port is never reached.
			// Keep the reply as a fallback and carry on.
			if notTheAgent(resp, path) {
				if stranger != nil {
					stranger.Body.Close()
				}
				stranger = resp
				a.forgetPort(host)
				continue
			}
			a.rememberPort(host, p)
			if stranger != nil {
				stranger.Body.Close()
			}
			return resp, nil
		}
		lastErr = err
		if !isTransportNotReady(err) {
			if stranger != nil {
				return stranger, nil
			}
			return nil, err
		}
		a.forgetPort(host)
	}
	if stranger != nil {
		return stranger, nil
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
	// A speaker that ANSWERED cannot be behind a firewall that blocks this app,
	// and the hint below then sends the user hunting through antivirus settings
	// for nothing. Field report 2026-08-06: an update failed with
	// `status 400 ... body=""` on every attempt over two days, and the advice
	// under it talked about firewalls and separate Wi-Fi networks while the
	// speaker was replying to each request. That is the same wrong-blame the
	// install path already had to fix.
	//
	// An HTTP status means the connection was made and something answered. What
	// answered was not STR: on a speaker whose agent is not up, the firmware's
	// own listener replies with a bare status. So say that instead.
	if answeredNotSTR(err) {
		return fmt.Errorf("%w\n\n%s", err, answeredNotSTRAdvice)
	}
	return fmt.Errorf("%w\n\n%s", err, firewallAdvice)
}

// The two closing paragraphs a user reads under a failed update. They are
// constants because the update report has to be able to recognise the wrong one
// and swap it for the right one (see stripWrongBlame): a copy of the text in
// two places would drift and the swap would quietly stop working.
const (
	firewallAdvice = "The app could not reach the speaker. This is usually a firewall or antivirus blocking ST Reborn, or this PC and the speaker being on different Wi-Fi networks. Allow ST Reborn through your firewall/antivirus (or turn it off briefly to test), and make sure both are on the same Wi-Fi network"

	answeredNotSTRAdvice = "The speaker answered, so this is not your firewall and not a Wi-Fi problem: something on the speaker replied to every request. What answered was not ST Reborn, which is what a speaker looks like while it is still starting up, or when its ST Reborn software did not come up at all. Unplug the speaker for ten seconds, plug it back in, wait about three minutes until it is fully up, and try the update again"
)

// answeredNotSTR reports whether the failure carries evidence that the speaker
// replied: an HTTP status line rather than a connection that never completed.
// Matched on the shapes this project's own errors carry ("status 400 on <ip>",
// "status 404", "unexpected status"), because those are the ones that reach a
// user, and deliberately NOT on timeouts or refusals, which really can be a
// firewall.
// A status anywhere in the chain wins even when the run also contains a
// timeout. The field report ends in an SSH timeout, but the preflight before it
// got a 400: the speaker demonstrably answered, so the firewall advice is wrong
// regardless of how the attempt finished.
func answeredNotSTR(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "status 4") ||
		strings.Contains(msg, "status 5") ||
		strings.Contains(msg, "unexpected status")
}
