// Package clocksync corrects an implausibly old system clock from a network
// HTTP Date header.
//
// SoundTouch speakers have no battery-backed RTC. After a cold boot, before
// NTP has synced (or when NTP is blocked), the wall clock reads the firmware's
// build epoch, observed as mid-2015. A clock that far in the past breaks every
// TLS handshake — Go's verifier rejects live certificates as "not yet valid" —
// so HTTPS radio (see internal/streamproxy) and the go-librespot Spotify
// sidecar both fail until the clock is corrected.
//
// usb-stick/run.sh does a one-shot HTTP Date sync at agent start, but that runs
// once and can miss a network that is not up yet. This package lets the agent
// re-attempt the same correction later — in particular when it notices the
// Spotify engine crash-looping, a classic wrong-clock symptom (#296).
package clocksync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/internet"
)

// minPlausible is the lower bound on a trustworthy wall clock. STR shipped in
// 2026, so a clock before 2025 is an unset RTC, not a real time.
var minPlausible = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

// maxPlausible guards against accepting a bogus far-future Date header.
var maxPlausible = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)

// dateHosts are queried over plain HTTP — no TLS, so there is no chicken-and-egg
// with the very clock we are trying to fix — for their Date response header.
// Same set as run.sh try_http_date_sync.
// The IP literals at the end are the escape hatch for a box with no working
// name resolution at all: a speaker whose DHCP lease carries no DNS server
// could never fix its clock, which in turn hard-gated autopair and left the
// box displaying "SoundTouch not configured" forever, with radio dead on top
// (#487 field bundle, 2026-07-27). These answer plain HTTP on the bare
// address, so the clock heals even before the DNS bootstrap lands.
var dateHosts = []string{
	"http://www.google.com/",
	"http://www.cloudflare.com/",
	"http://www.bose.com/",
	"http://1.1.1.1/",
	"http://8.8.8.8/",
}

// Implausible reports whether now is too far in the past to be a real wall
// clock (an unset RTC), meaning TLS and time-sensitive logic cannot be trusted.
func Implausible(now time.Time) bool { return now.Before(minPlausible) }

// plausible reports whether a fetched network time is itself a sane wall clock,
// so a broken or malicious Date header cannot move the clock somewhere absurd.
func plausible(t time.Time) bool { return t.After(minPlausible) && t.Before(maxPlausible) }

// fetchNetworkTime returns the current time from the first reachable host's HTTP
// Date header. It only accepts a plausible value.
//
// Every attempt also feeds the internet reachability verdict (internal/internet)
// at no extra cost: an HTTP answer that looks genuine (genuineAnswer, so not a
// captive portal) proves the box reaches the internet, and
// every host failing, the IP literals included, is the strongest evidence the
// agent ever gets that it does not (2026-10-14 field case: DNS SERVFAIL for the
// names, timeouts for the IP literals, clock stuck in 2015 for an hour, and
// nothing on screen saying why).
func fetchNetworkTime(ctx context.Context, client *http.Client, hosts []string) (time.Time, error) {
	lastErr := errors.New("clocksync: no hosts to query")
	failReason, answered := "", false
	var failErr error
	defer func() {
		// Reported once per attempt, after all hosts, and only when none of
		// them answered at all, so one dead host in the list never reads as
		// "no internet". The reason is that of the LAST host that failed for a
		// network reason: the list ends with IP literals, so a timeout there
		// (the internet is gone) wins over the DNS failure of the named hosts.
		if !answered && failReason != "" {
			internet.Default.Failure("clocksync", failReason, failErr)
		}
	}()
	for _, h := range hosts {
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, h, nil)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() == nil {
				if r := internet.Classify(err, false); r != "" {
					failReason, failErr = r, err
				}
			}
			continue
		}
		// Any HTTP answer at all means this attempt is no evidence of a dead
		// internet. Only an answer that looks like it came from the host asked
		// is evidence of a working one: a captive portal (an open hotspot's
		// login page) answers too, so it must not read as "internet ok".
		answered = true
		if genuineAnswer(req, resp) {
			internet.ReportSuccess("clocksync")
		}
		dateHdr := resp.Header.Get("Date")
		_ = resp.Body.Close()
		if dateHdr == "" {
			lastErr = fmt.Errorf("clocksync: no Date header from %s", h)
			continue
		}
		t, err := http.ParseTime(dateHdr)
		if err != nil {
			lastErr = fmt.Errorf("clocksync: unparsable Date %q from %s: %w", dateHdr, h, err)
			continue
		}
		if !plausible(t) {
			lastErr = fmt.Errorf("clocksync: implausible Date %q from %s", dateHdr, h)
			continue
		}
		return t, nil
	}
	return time.Time{}, lastErr
}

// genuineAnswer reports whether resp looks like the real answer of the host
// req asked, and so proves the speaker reaches the internet. Support case
// 2026-10-15: speakers on a Vodafone Homespot get an HTTP answer from the
// hotspot's login page for every plain HTTP request, which used to read as
// "internet ok" while radio was dead.
//
// Rejected: 511 Network Authentication Required (RFC 6585, the captive portal
// status), an answer that ended on (or points to) a different host than the
// one asked, and an answer without a Date header (every real web server sends
// one). A 204 No Content is the generate_204 style connectivity check answer a
// portal never gives, so it always counts. A portal that intercepts the
// request in place, same host and no redirect, cannot be told apart from a
// HEAD answer; that remains a known blind spot.
func genuineAnswer(req *http.Request, resp *http.Response) bool {
	switch {
	case resp.StatusCode == http.StatusNoContent:
		return true
	case resp.StatusCode == http.StatusNetworkAuthenticationRequired:
		return false
	}
	asked := strings.ToLower(req.URL.Host)
	// resp.Request is the last request of a followed redirect chain.
	if resp.Request != nil && resp.Request.URL != nil && strings.ToLower(resp.Request.URL.Host) != asked {
		return false
	}
	// A redirect the client did not follow: judge where it points.
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		if loc, err := resp.Location(); err == nil && strings.ToLower(loc.Host) != asked {
			return false
		}
	}
	return resp.Header.Get("Date") != ""
}

// RunUntilSynced keeps correcting an implausibly old system clock from a network
// HTTP Date header, retrying every interval until it succeeds or ctx is
// cancelled. It returns immediately when the clock is already plausible, so it
// is cheap to start unconditionally (best in a goroutine) at agent boot.
//
// This is the standing fallback for run.sh's one-shot boot sync, which can miss
// a network that is not up yet: without a retry, a radio-only or idle box whose
// boot sync failed would stay stuck in the past — no HTTPS radio, no Spotify —
// until the next reboot. Once a valid time is set the clock stays put, so this
// returns after the first success. A non-positive interval defaults to 30s.
func RunUntilSynced(ctx context.Context, logger *slog.Logger, interval time.Duration) {
	if !Implausible(time.Now()) {
		return
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	client := &http.Client{Timeout: 5 * time.Second}
	for {
		synced, err := SyncIfImplausible(ctx, client, logger)
		if synced || !Implausible(time.Now()) {
			return
		}
		if err != nil && logger != nil {
			logger.Debug("clock-sync: clock still unset, will retry", "err", err, "retryIn", interval.String())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// SyncNowIfImplausible makes a single best-effort attempt to correct an
// implausibly old clock, with its own short-lived HTTP client. It returns true
// when it set the clock. Meant to be called synchronously at agent startup,
// before the agent serves requests, so the very first request already has a
// sane clock on a stick-free network install (where run.sh never ran at install
// time to do the boot Date sync). The RunUntilSynced goroutine still runs after
// this to keep retrying if the network was not up yet (#375).
func SyncNowIfImplausible(ctx context.Context, logger *slog.Logger) bool {
	if !Implausible(time.Now()) {
		return false
	}
	client := &http.Client{Timeout: 5 * time.Second}
	ok, _ := SyncIfImplausible(ctx, client, logger)
	return ok
}

// SyncIfImplausible sets the system clock from a network HTTP Date header, but
// only when the local clock is implausibly old and a plausible, strictly later
// network time is reachable — it never moves the clock backward. It returns true
// when it set the clock. Best-effort: a returned error is informational and not
// fatal to the caller (radio and Spotify simply keep failing until a later
// attempt or an NTP sync succeeds).
func SyncIfImplausible(ctx context.Context, client *http.Client, logger *slog.Logger) (bool, error) {
	now := time.Now()
	if !Implausible(now) {
		return false, nil
	}
	netTime, err := fetchNetworkTime(ctx, client, dateHosts)
	if err != nil {
		return false, err
	}
	if !netTime.After(now) {
		// Never move the clock backward.
		return false, nil
	}
	if err := setSystemTime(netTime); err != nil {
		return false, err
	}
	if logger != nil {
		logger.Info("clock-sync: system clock corrected from HTTP Date",
			"was", now.UTC().Format(time.RFC3339), "now", netTime.UTC().Format(time.RFC3339))
	}
	return true, nil
}
