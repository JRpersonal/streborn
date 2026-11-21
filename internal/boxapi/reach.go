package boxapi

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
)

// Reachability of the speaker's own firmware webserver (:8090).
//
// The agent reads :8090 all the time on its own: the autopair heartbeat, the
// preset reconciler, the standby clock, the us-services source list, the phone
// remote's status. Every one of those reads already knows whether BoseApp
// answered. This file collects that verdict in one place, so an outage of the
// webserver is noticed without a probe or timer of its own (standing rule:
// spare the speaker, never poll).
//
// Field case (SoundTouch 30, 2026-11-21): every :8090 request timed out for
// ten minutes and more while UPnP (:8091) and the agent answered, and nothing
// in STR said so, because the only outage tracker was fed by the app's status
// poll and nobody was polling.
//
// Only a transport failure is "down". Any HTTP answer, a firmware error reply
// (*BoxError, a 4xx or 5xx) included, proves the webserver is alive. A request
// the caller cancelled is no verdict at all: the caller gave up, the speaker
// did not.

type reachHook struct {
	host string
	fn   func(answered bool)
}

var reach atomic.Pointer[reachHook]

// ObserveReachability registers fn to receive one verdict per round trip to
// host's :8090 (true: the webserver answered; false: it did not). Requests to
// any other host (group members, peers) are not reported. One observer per
// process; a nil fn removes it. The agent registers its own speaker here.
//
// fn runs on the requesting goroutine, so it must be cheap and must not call
// back into the speaker.
func ObserveReachability(host string, fn func(answered bool)) {
	if fn == nil || host == "" {
		reach.Store(nil)
		return
	}
	reach.Store(&reachHook{host: host, fn: fn})
}

// noteReach reports one round trip's outcome to the observer when host is the
// observed one.
func noteReach(host string, err error) {
	h := reach.Load()
	if h == nil || h.host != host {
		return
	}
	if err == nil {
		h.fn(true)
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	h.fn(false)
}

// do is the single place a Client sends a request, so every boxapi call to the
// observed speaker feeds the reachability verdict, whatever http.Client a
// caller swapped in.
func (c *Client) do(req *http.Request) (*http.Response, error) {
	resp, err := c.HTTP.Do(req)
	noteReach(c.Host, err)
	return resp, err
}

// ObservedTransport wraps base (http.DefaultTransport when nil) so every round
// trip feeds the reachability verdict for host. For code that talks to :8090
// with its own http.Client instead of a boxapi Client (the autopair heartbeat).
func ObservedTransport(host string, base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return observedTransport{host: host, base: base}
}

type observedTransport struct {
	host string
	base http.RoundTripper
}

func (t observedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	noteReach(t.host, err)
	return resp, err
}
