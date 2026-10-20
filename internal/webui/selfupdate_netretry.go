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
	"net"
	"strings"
	"time"
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
	// after a network failure, and fleetNetPause how long it waits first.
	fleetNetRetries = 2
	fleetNetPause   = 30 * time.Second
)

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
