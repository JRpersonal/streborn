package webui

// Updates that meet a network outage.
//
// A tester unplugged the router a third of the way into "update all speakers"
// (discussion #1339, three ST10s, 2026-10-20). What the logs showed:
//
//   - The speaker that was downloading lost GitHub at 35 %. The stall timer
//     cut the download 90 s later and the update failed with the bare
//     "download: context canceled". The router was off for seven minutes, so
//     nothing could have finished, but the failure was final: nobody tried
//     again once the network was back, and the update had already dropped the
//     Spotify engine to make room, so that speaker was left without Spotify.
//   - When the router came back, the LAN was up some 35 s before its DNS
//     forwarder. The next speaker's check and the last speaker's download both
//     ran inside that window and failed within 200 ms on "lookup github.com:
//     server misbehaving". A retry a minute later would have worked.
//
// So a failure that only means "no internet right now" is now waited out and
// retried, bounded, inside the update the owner started: the check retries
// for a short while, a download that lost the network is tried again a few
// times with growing pauses, and the run that drives several speakers starts
// a speaker over once its network is back. Anything else (a signature, a hash,
// a refusal) still fails at once.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/updatesig"
)

var (
	// selfUpdateNetRetry are the pauses before a download that lost the
	// network is tried again. About eight minutes in all: long enough for a
	// router restart, short of the twelve minutes a driving phone waits.
	selfUpdateNetRetry = []time.Duration{10 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 2 * time.Minute, 2 * time.Minute}
	// selfUpdateCheckRetryEvery and selfUpdateCheckRetryFor bound the retries
	// of a release check that hit a network error.
	selfUpdateCheckRetryEvery = 5 * time.Second
	selfUpdateCheckRetryFor   = 30 * time.Second
	// fleetNetRetries is how often the driving speaker starts a peer over
	// after a network failure. Before each start it waits at least
	// fleetNetPause, and then until the network is back (waitFleetNetBack),
	// fleetNetWaitFor at most: a fixed 30 s pause used to start the peer over
	// into the same outage, so a router that was off for a few minutes failed
	// the peer for good after about three.
	fleetNetRetries = 2
	fleetNetPause   = 30 * time.Second
	fleetNetWaitFor = 10 * time.Minute
	// fleetNetProbeEvery is how often the wait asks whether the network is back.
	fleetNetProbeEvery = 10 * time.Second
	// fleetReleaseReachable reports whether this speaker reaches the release
	// host again. The peers sit behind the same router, so this is the best
	// guess at whether a peer's own check would get through. A seam for tests.
	fleetReleaseReachable = releaseHostReachable
)

// releaseHostReachable resolves the release host and opens a connection to
// it. No request is sent: it only answers "is the internet back".
func releaseHostReachable(ctx context.Context) bool {
	base, _ := selfUpdateSourceNow()
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" {
		return false
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// waitFleetNetBack waits until the peer answers on the LAN again and this
// speaker reaches the release host, fleetNetWaitFor at most. False when the
// network did not come back in time or ctx ended.
func waitFleetNetBack(ctx context.Context, base string) bool {
	if !sleepCtx(ctx, fleetNetPause) {
		return false
	}
	until := time.Now().Add(fleetNetWaitFor)
	for {
		var st selfUpdateStatus
		if _, err := fleetGetJSON(ctx, base+"/api/agent/self-update", 6*time.Second, &st); err == nil && fleetReleaseReachable(ctx) {
			return true
		}
		if time.Now().After(until) || !sleepCtx(ctx, fleetNetProbeEvery) {
			return false
		}
	}
}

// errDownloadStalled replaces the bare "context canceled" a stalled download
// used to end with.
var errDownloadStalled = errors.New("the download stalled, nothing arrived for 90 seconds")

// networkErrorText reports whether an error message, possibly from another
// speaker's agent, only says that the network was not there. Peers run older
// agents, so this works on the text, not on error types.
func networkErrorText(msg string) bool {
	m := strings.ToLower(msg)
	if m == "" {
		return false
	}
	// A signed release that does not match is never a network matter, even
	// when the message also carries a connection detail.
	for _, never := range []string{"does not match", "signature", "already runs"} {
		if strings.Contains(m, never) {
			return false
		}
	}
	for _, s := range []string{
		"lookup ", "dial tcp", "i/o timeout", "server misbehaving", "no such host",
		"network is unreachable", "no route to host", "connection reset", "connection refused",
		"context canceled", "deadline exceeded", "tls handshake timeout", "download stalled",
		"download ended after", "unexpected eof", "could not reach the internet",
	} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

// isNetworkError is networkErrorText for an error of this agent.
func isNetworkError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errDownloadStalled) {
		return true
	}
	var dns *net.DNSError
	var op *net.OpError
	if errors.As(err, &dns) || errors.As(err, &op) {
		return true
	}
	return networkErrorText(err.Error())
}

// httpStatusError is a release file that answered with something other than
// 200. The text is the one the check always reported ("update.json.sig: HTTP
// 404").
type httpStatusError struct {
	name string
	code int
}

func (e *httpStatusError) Error() string { return fmt.Sprintf("%s: HTTP %d", e.name, e.code) }

// isTransientHTTP reports an answer worth asking again: GitHub's release
// download now and then answers 404 for an asset that exists, and the next
// request gets it (live, ST10, 2026-10-22: update.json loaded, its signature
// 404'd, and the same check a minute later worked on three speakers). A 404
// also shows up while a release is being published and its files are still
// uploading. 408, 429 and 5xx are the host saying "not now".
func isTransientHTTP(err error) bool {
	var he *httpStatusError
	if !errors.As(err, &he) {
		return false
	}
	return he.code == http.StatusNotFound || he.code == http.StatusRequestTimeout ||
		he.code == http.StatusTooManyRequests || he.code >= 500
}

// sleepCtx waits d, or less when ctx ends first. False when ctx ended.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

var (
	// engineRestoreProbeEvery and engineRestoreWaitFor bound the wait for the
	// network after an update that failed on it and had already dropped the
	// Spotify engine. Only that rare case starts the wait, and it writes
	// nothing while it waits.
	engineRestoreProbeEvery = 30 * time.Second
	engineRestoreWaitFor    = time.Hour
	// errEngineRestoreBusy: another check or install holds the updater, so
	// the restore asks again on the next round instead of racing it.
	errEngineRestoreBusy = errors.New("another update is running on this speaker")
)

// retryWhenOnline calls try each time the release host is reachable again,
// until try succeeds, fails for a reason other than the network, or
// engineRestoreWaitFor is over. It returns try's last error.
//
// #1339 (ST10, 2026-10-25): the router was off for about eighteen minutes.
// The update gave up after its own retries, tried once to put the Spotify
// engine back, still inside the outage, and failed. Seven minutes later the
// network was back, but nothing asked again, so the speaker stayed without
// Spotify until the owner started another update.
func retryWhenOnline(ctx context.Context, try func() error) error {
	until := time.Now().Add(engineRestoreWaitFor)
	err := errors.New("the network did not come back")
	for {
		if !sleepCtx(ctx, engineRestoreProbeEvery) {
			return err
		}
		if fleetReleaseReachable(ctx) {
			err = try()
			if err == nil || (!errors.Is(err, errEngineRestoreBusy) && !isNetworkError(err)) {
				return err
			}
		}
		if time.Now().After(until) {
			return err
		}
	}
}

// restoreEngineWhenOnline puts back the engine a failed update dropped, once
// the network is back. agentErr is what the update itself failed on: the
// card keeps saying that, without the engine part once the engine is back.
func (s *Server) restoreEngineWhenOnline(base, version string, want updatesig.File, agentErr error) {
	err := retryWhenOnline(context.Background(), func() error {
		if present, _ := goLibrespotStamp(); present {
			return nil
		}
		if !s.selfUpd.claim() {
			return errEngineRestoreBusy
		}
		defer s.selfUpd.release()
		// ensureEngine shows "engine" on the card while it runs. Afterwards
		// the card goes back to what it said: the failed update, or, when
		// the owner has moved on since (a new check), nothing.
		s.selfUpd.mu.Lock()
		wasFailed := s.selfUpd.state == "failed"
		s.selfUpd.mu.Unlock()
		back := func(engineErr error) {
			switch {
			case !wasFailed:
				s.selfUpd.set("idle", "")
			case engineErr != nil:
				s.selfUpd.fail(fmt.Errorf("%w (and the Spotify engine could not be restored: %w)", agentErr, engineErr))
			default:
				s.selfUpd.fail(agentErr)
			}
		}
		if err := s.ensureEngine(base, version, want); err != nil {
			back(err)
			return err
		}
		back(nil)
		s.logger.Info("self-update: Spotify engine restored once the network was back")
		return nil
	})
	if err != nil {
		s.logger.Warn("self-update: gave up restoring the Spotify engine after the failed update", "err", err)
	}
}
