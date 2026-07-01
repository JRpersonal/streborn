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
	"time"
)

// minPlausible is the lower bound on a trustworthy wall clock. STR shipped in
// 2026, so a clock before 2025 is an unset RTC, not a real time.
var minPlausible = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

// maxPlausible guards against accepting a bogus far-future Date header.
var maxPlausible = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)

// dateHosts are queried over plain HTTP — no TLS, so there is no chicken-and-egg
// with the very clock we are trying to fix — for their Date response header.
// Same set as run.sh try_http_date_sync.
var dateHosts = []string{
	"http://www.google.com/",
	"http://www.cloudflare.com/",
	"http://www.bose.com/",
}

// Implausible reports whether now is too far in the past to be a real wall
// clock (an unset RTC), meaning TLS and time-sensitive logic cannot be trusted.
func Implausible(now time.Time) bool { return now.Before(minPlausible) }

// plausible reports whether a fetched network time is itself a sane wall clock,
// so a broken or malicious Date header cannot move the clock somewhere absurd.
func plausible(t time.Time) bool { return t.After(minPlausible) && t.Before(maxPlausible) }

// fetchNetworkTime returns the current time from the first reachable host's HTTP
// Date header. It only accepts a plausible value.
func fetchNetworkTime(ctx context.Context, client *http.Client, hosts []string) (time.Time, error) {
	lastErr := errors.New("clocksync: no hosts to query")
	for _, h := range hosts {
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, h, nil)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
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
