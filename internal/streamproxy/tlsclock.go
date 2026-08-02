// tlsclock.go: clock-tolerant TLS verification for upstream HTTPS streams on
// boxes whose wall clock reset after a cold boot (#296).

package streamproxy

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"time"
)

// minPlausibleClock is a lower bound on a trustworthy wall clock. STR shipped in
// 2026, so a box reporting a time before this has an unset clock. SoundTouch
// speakers have no battery-backed RTC: after a cold boot, before NTP has synced
// (or when NTP is blocked), the clock lands in the firmware's build epoch, often
// mid-2015. See #296 and docs/FIRMWARE-NOTES.md.
var minPlausibleClock = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

// clockUntrustworthy reports whether the local wall clock is implausibly old,
// which would make certificate time-validity checks fail spuriously. It reads
// time.Now() live, so strict verification resumes automatically the moment NTP
// corrects the clock.
func clockUntrustworthy() bool { return time.Now().Before(minPlausibleClock) }

// clockTolerantTLSConfig builds the per-connection TLS config the proxy uses for
// an upstream HTTPS radio stream dialed at host (a hostname, or a bare-IP
// literal). It always verifies the certificate chain to the system roots and the
// host, but tolerates a wrong box clock: when the local clock is implausibly
// old, a chain that is valid except for the certificate's time window is still
// accepted. Without this, a speaker whose clock reset to 2015 rejected every
// HTTPS station as "certificate is not yet valid" (#296: Virgin Radio and other
// HTTPS streams would not play, while plain-HTTP BBC streams still did).
//
// Only the expiry/not-yet-valid window is relaxed, and only while the clock is
// untrustworthy; chain-to-root and host are always enforced, so this does not
// weaken MITM protection. host is passed explicitly (not read from
// ConnectionState.ServerName) so a bare-IP upstream, which sends no SNI, is
// still verified against the certificate's IP SANs.
func clockTolerantTLSConfig(host string) *tls.Config {
	return &tls.Config{
		ServerName: host, // SNI for a hostname; ignored on the wire for an IP literal
		// h2 stays available (DialTLSContext otherwise defaults to HTTP/1.1 only).
		NextProtos: []string{"h2", "http/1.1"},
		// The default verifier is disabled so the time window can be relaxed in
		// VerifyConnection; chain and host are still checked there manually.
		InsecureSkipVerify: true, //nolint:gosec // VerifyConnection re-implements chain+host verification below.
		VerifyConnection: func(cs tls.ConnectionState) error {
			roots, _ := x509.SystemCertPool()
			return verifyChainClockTolerant(cs.PeerCertificates, host, roots, time.Now(), clockUntrustworthy())
		},
	}
}

// verifyChainClockTolerant verifies leaf (certs[0]) against roots and host at
// time now. host is matched against the certificate's DNS names, or, for an IP
// literal, its IP SANs. If verification fails solely because the certificate is
// outside its time window and clockBad is true, it retries with the time check
// pinned to the leaf's NotBefore (so the window always passes) while still
// requiring a valid chain and host. It is split out from clockTolerantTLSConfig
// so the policy can be unit-tested without a live TLS handshake.
func verifyChainClockTolerant(certs []*x509.Certificate, host string, roots *x509.CertPool, now time.Time, clockBad bool) error {
	if len(certs) == 0 {
		return errors.New("tls: no peer certificates")
	}
	// Require a host: x509.Verify silently skips host checking when DNSName is
	// empty, so an empty host here would drop the host guard entirely. The
	// caller derives it from the dial address, so an empty value means something
	// is wrong and we must reject rather than relax.
	if host == "" {
		return errors.New("tls: empty host")
	}
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	leaf := certs[0]
	opts := x509.VerifyOptions{DNSName: host, Roots: roots, Intermediates: inter, CurrentTime: now}
	if _, err := leaf.Verify(opts); err == nil {
		return nil
	} else {
		// Relax the time window only when the clock is untrustworthy and the
		// failure was a time-validity problem (x509.Expired covers both
		// not-yet-valid and expired). Any other failure (unknown authority,
		// hostname mismatch) still rejects the connection.
		var invalid x509.CertificateInvalidError
		if clockBad && errors.As(err, &invalid) && invalid.Reason == x509.Expired {
			opts.CurrentTime = leaf.NotBefore
			if _, err2 := leaf.Verify(opts); err2 == nil {
				return nil
			}
		}
		return err
	}
}
