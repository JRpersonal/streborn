// Package internet keeps a small, passive verdict on whether this speaker can
// reach the public internet.
//
// Why it exists: a field case (2026-10-14) had five speakers whose router
// answered every DNS query with SERVFAIL and dropped plain connections to
// public addresses, after a reboot. The LAN worked, so the desktop app found
// and controlled every box, and the only visible symptom was "radio stops
// after a few seconds". The owner spent two hours rebooting speakers because
// nothing said the speakers had no internet. The agent already had all the
// evidence (clock sync failing against five hosts, the stream proxy failing
// to resolve the station host); it just never put it in one place.
//
// The tracker owns NO timer and makes NO request of its own (standing rule:
// spare the box hardware, no new polling loops). It is fed by code that talks
// to the internet anyway:
//
//   - internal/clocksync, every attempt to fetch an HTTP Date header;
//   - internal/streamproxy, every upstream dial and the on-demand outbound
//     probe behind /api/stream-status.
//
// "unreachable" is only ever reported on positive evidence: a failure with no
// success since. Absence of data is "unknown", never "unreachable". A failure
// verdict also fades back to "unknown" after staleAfter without a fresh
// failure, so a single hiccup on an otherwise idle speaker cannot keep a
// "no internet" warning on screen for days.
package internet

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Status values reported in /api/agent/version ("internet").
const (
	StatusOK          = "ok"
	StatusUnreachable = "unreachable"
	StatusUnknown     = "unknown"
)

// Reason values ("internetReason"): short machine words the desktop app and a
// diagnostic reader can act on.
const (
	// ReasonDNS: the name server answered with an error (SERVFAIL, refused),
	// timed out, or there is none. NOT a plain "no such host" (NXDOMAIN),
	// which is one dead station host, not a dead internet.
	ReasonDNS = "dns"
	// ReasonTimeout: a connection to a public address timed out.
	ReasonTimeout = "timeout"
	// ReasonRoute: the kernel has no route to the internet at all.
	ReasonRoute = "route"
	// ReasonConnect: a connection to a public address was refused or reset.
	ReasonConnect = "connect"
)

// staleAfter is how long a failure verdict stands without a fresh failure.
// Comfortably longer than the clock sync retry (30 s) and the Bose player's
// stream retries, so a box that is really offline keeps reporting it, while a
// stale one-off falls back to "unknown".
const staleAfter = 10 * time.Minute

// Tracker is the in-memory verdict. The zero value is not usable; use New.
type Tracker struct {
	mu     sync.Mutex
	now    func() time.Time
	logger *slog.Logger

	lastOK       time.Time
	lastOKSource string

	lastFail       time.Time
	lastFailSource string
	lastFailReason string
	lastFailErr    string

	failures    int // failures since the last success
	transitions int
	reported    string // the status the last transition log line announced
}

// New returns an empty tracker ("unknown").
func New(logger *slog.Logger) *Tracker {
	return &Tracker{now: time.Now, logger: logger, reported: StatusUnknown}
}

// SetLogger sets the logger for the transition lines.
func (t *Tracker) SetLogger(l *slog.Logger) {
	t.mu.Lock()
	t.logger = l
	t.mu.Unlock()
}

// Success records that something reached the internet (an HTTP answer from a
// public host, a TCP connection to a public address).
func (t *Tracker) Success(source string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastOK = t.now()
	t.lastOKSource = source
	t.failures = 0
	t.logTransitionLocked()
}

// Failure records evidence that the internet is unreachable, with a reason
// from the constants above. An empty reason is ignored, so callers can pass
// Classify(err) straight through.
func (t *Tracker) Failure(source, reason string, err error) {
	if reason == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastFail = t.now()
	t.lastFailSource = source
	t.lastFailReason = reason
	t.lastFailErr = ""
	if err != nil {
		t.lastFailErr = truncate(err.Error(), 200)
	}
	t.failures++
	t.logTransitionLocked()
}

// statusLocked computes the current verdict. Caller holds mu.
func (t *Tracker) statusLocked() (status, reason string) {
	now := t.now()
	if !t.lastFail.IsZero() && t.lastFail.After(t.lastOK) {
		if now.Sub(t.lastFail) > staleAfter {
			return StatusUnknown, ""
		}
		return StatusUnreachable, t.lastFailReason
	}
	if !t.lastOK.IsZero() {
		return StatusOK, ""
	}
	return StatusUnknown, ""
}

// logTransitionLocked writes one INFO line when the verdict changed since the
// last line, never one per failure. Caller holds mu.
func (t *Tracker) logTransitionLocked() {
	status, reason := t.statusLocked()
	if status == t.reported {
		return
	}
	prev := t.reported
	t.reported = status
	t.transitions++
	if t.logger == nil {
		return
	}
	if status == StatusUnreachable {
		t.logger.Info("internet: speaker cannot reach the internet",
			"was", prev, "reason", reason, "source", t.lastFailSource, "err", t.lastFailErr)
		return
	}
	t.logger.Info("internet: speaker reaches the internet",
		"was", prev, "source", t.lastOKSource)
}

// Status returns the verdict for /api/agent/version: StatusOK, StatusUnreachable
// or StatusUnknown, and for unreachable the reason word.
func (t *Tracker) Status() (status, reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.statusLocked()
}

// Snapshot is the debug section: the verdict plus the evidence behind it.
func (t *Tracker) Snapshot() any {
	t.mu.Lock()
	defer t.mu.Unlock()
	status, reason := t.statusLocked()
	out := map[string]any{
		"status":          status,
		"failuresSinceOK": t.failures,
		"transitions":     t.transitions,
		"staleAfterSec":   int(staleAfter.Seconds()),
	}
	if reason != "" {
		out["reason"] = reason
	}
	now := t.now()
	if !t.lastOK.IsZero() {
		out["lastOK"] = t.lastOK.UTC().Format(time.RFC3339)
		out["lastOKAgeSec"] = int(now.Sub(t.lastOK).Seconds())
		out["lastOKSource"] = t.lastOKSource
	}
	if !t.lastFail.IsZero() {
		out["lastFail"] = t.lastFail.UTC().Format(time.RFC3339)
		out["lastFailAgeSec"] = int(now.Sub(t.lastFail).Seconds())
		out["lastFailSource"] = t.lastFailSource
		out["lastFailReason"] = t.lastFailReason
		if t.lastFailErr != "" {
			out["lastFailErr"] = t.lastFailErr
		}
	}
	return out
}

// Classify maps a network error to a reason word, or "" when the error is no
// evidence that the internet as a whole is unreachable. strict limits it to
// the classes that cannot be one bad far end: a DNS server failure and a
// missing route. The stream proxy uses strict, because one station's server
// timing out or refusing says nothing about the internet; the clock sync and
// the outbound probe, which try several well-known hosts, use the full set.
func Classify(err error, strict bool) string {
	if err == nil {
		return ""
	}
	// A cancelled request (the user switched station) is no evidence.
	if errors.Is(err, context.Canceled) {
		return ""
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsNotFound {
			// NXDOMAIN: the name does not exist. A dead station host, or the
			// whole internet down behind a resolver that still answers; the
			// first is far more common, so it is not counted.
			return ""
		}
		return ReasonDNS
	}
	if errors.Is(err, syscall.ENETUNREACH) {
		return ReasonRoute
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "network is unreachable") {
		return ReasonRoute
	}
	if strict {
		return ""
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return ReasonTimeout
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(msg, "timeout") {
		return ReasonTimeout
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		// Text fallback for hosts whose errno differs (Windows test runs).
		strings.Contains(msg, "connection refused") || strings.Contains(msg, "actively refused") ||
		strings.Contains(msg, "connection reset") {
		return ReasonConnect
	}
	return ""
}

// IsPublic reports whether ip is a public internet address, so a connection
// to a LAN media server or the speaker itself is never counted as proof the
// internet works.
func IsPublic(ip net.IP) bool {
	if ip == nil {
		return false
	}
	return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() && !ip.IsMulticast() && !ip.IsUnspecified()
}

// IsPublicAddr is IsPublic for a net.Addr (a connection's RemoteAddr).
func IsPublicAddr(a net.Addr) bool {
	if a == nil {
		return false
	}
	host, _, err := net.SplitHostPort(a.String())
	if err != nil {
		host = a.String()
	}
	return IsPublic(net.ParseIP(host))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Default is the process-wide tracker the agent feeds and reports.
var Default = New(nil)

// SetLogger sets the logger of Default.
func SetLogger(l *slog.Logger) { Default.SetLogger(l) }

// ReportSuccess records a success on Default.
func ReportSuccess(source string) { Default.Success(source) }

// ReportError classifies err and records it on Default when it counts.
func ReportError(source string, err error, strict bool) {
	Default.Failure(source, Classify(err, strict), err)
}

// Status returns Default's verdict.
func Status() (status, reason string) { return Default.Status() }

// Snapshot returns Default's debug section.
func Snapshot() any { return Default.Snapshot() }
