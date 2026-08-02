package marge

// Developer forwarding: hand the box's cloud conversation to a marge stub
// running on a developer machine.
//
// The box speaks HTTPS to streaming.bose.com and trusts only the CA the agent
// generated on the box itself, so pointing the SPEAKER at a developer machine
// would mean exporting that CA - a private key STR must never hand out. This
// takes the other route: the on-box agent keeps terminating TLS exactly as
// before and relays the decrypted request to the developer machine, returning
// its answer verbatim. The box notices nothing; the developer edits responses
// and restarts a local process instead of building, OTA-ing and rebooting.
//
// Off by default, never persisted: a reboot clears it. The target must be a
// private address.

import (
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// SetForward points the stub at a developer machine ("192.168.1.5:9080"), or
// clears it when target is empty. Returns an error for a non-private target.
func (s *Server) SetForward(target string) error {
	target = strings.TrimSpace(target)
	if target != "" {
		host, _, err := net.SplitHostPort(target)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if ip == nil || (!ip.IsPrivate() && !ip.IsLoopback()) {
			return errNonPrivateForward
		}
	}
	s.mu.Lock()
	s.forward = target
	s.mu.Unlock()
	if target == "" {
		s.logger.Warn("marge forward cleared, answering locally again")
	} else {
		s.logger.Warn("marge forward active: the box's cloud traffic is relayed to a developer machine", "target", target)
	}
	return nil
}

type forwardErr string

func (e forwardErr) Error() string { return string(e) }

const errNonPrivateForward = forwardErr("forward target must be a private LAN address")

// forwardTarget reports the active relay target, empty when off.
func (s *Server) forwardTarget() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.forward
}

// relay sends the request to the developer machine and copies the answer back.
// Any failure falls through to the local handler, so a stopped lab process
// degrades to normal operation instead of bricking the box's cloud view.
func (s *Server) relay(w http.ResponseWriter, r *http.Request, target string) bool {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	url := "http://" + target + r.URL.RequestURI()
	req, err := http.NewRequestWithContext(r.Context(), r.Method, url, strings.NewReader(string(body)))
	if err != nil {
		return false
	}
	for k, vs := range r.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("X-STR-Forwarded-Host", r.Host)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		s.logger.Warn("marge forward failed, answering locally", "target", target, "err", err)
		return false
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	n, _ := io.Copy(w, io.LimitReader(resp.Body, 4<<20))
	s.logger.Info("marge forwarded", "path", r.URL.Path, "status", resp.StatusCode, "bytes", n)
	return true
}
