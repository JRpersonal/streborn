package streamproxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"
)

// mintChain returns a leaf certificate for dnsName signed by a fresh CA, valid
// in [notBefore, notAfter], plus a roots pool containing that CA. It is the
// minimum needed to exercise verifyChainClockTolerant without a live handshake.
func mintChain(t *testing.T, dnsName string, notBefore, notAfter time.Time) (*x509.Certificate, *x509.CertPool) {
	t.Helper()
	return mintChainSAN(t, []string{dnsName}, nil, notBefore, notAfter)
}

// mintChainSAN is mintChain with explicit DNS and IP SANs, so the bare-IP
// upstream path (no SNI) can be exercised too.
func mintChainSAN(t *testing.T, dnsNames []string, ipSANs []net.IP, notBefore, notAfter time.Time) (*x509.Certificate, *x509.CertPool) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ca key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("ca cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse ca: %v", err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("leaf key: %v", err)
	}
	cn := ""
	if len(dnsNames) > 0 {
		cn = dnsNames[0]
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     dnsNames,
		IPAddresses:  ipSANs,
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("leaf cert: %v", err)
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	return leaf, roots
}

func TestVerifyChainClockTolerant(t *testing.T) {
	const host = "radio.example.com"
	badClock := time.Date(2015, 7, 6, 21, 0, 0, 0, time.UTC)  // box clock reset to 2015 (#296)
	goodClock := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC) // clock synced
	certValidFrom := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)
	certValidTo := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	t.Run("not-yet-valid cert is accepted when the clock is untrustworthy", func(t *testing.T) {
		leaf, roots := mintChain(t, host, certValidFrom, certValidTo)
		if err := verifyChainClockTolerant([]*x509.Certificate{leaf}, host, roots, badClock, true); err != nil {
			t.Fatalf("expected tolerance for a wrong clock, got %v", err)
		}
	})

	t.Run("not-yet-valid cert is rejected when the clock is trustworthy", func(t *testing.T) {
		leaf, roots := mintChain(t, host, certValidFrom, certValidTo)
		// Same 2015 wall clock, but we assert the clock is trustworthy: strict
		// time checking must still reject a cert outside its window.
		if err := verifyChainClockTolerant([]*x509.Certificate{leaf}, host, roots, badClock, false); err == nil {
			t.Fatal("expected rejection when the clock is trusted but the cert is not yet valid")
		}
	})

	t.Run("valid cert within its window is accepted", func(t *testing.T) {
		leaf, roots := mintChain(t, host, certValidFrom, certValidTo)
		if err := verifyChainClockTolerant([]*x509.Certificate{leaf}, host, roots, goodClock, false); err != nil {
			t.Fatalf("expected a valid cert to verify, got %v", err)
		}
	})

	t.Run("hostname mismatch is rejected even with a bad clock", func(t *testing.T) {
		leaf, roots := mintChain(t, host, certValidFrom, certValidTo)
		if err := verifyChainClockTolerant([]*x509.Certificate{leaf}, "evil.example.com", roots, badClock, true); err == nil {
			t.Fatal("expected hostname mismatch to reject regardless of clock tolerance")
		}
	})

	t.Run("untrusted root is rejected even with a bad clock", func(t *testing.T) {
		leaf, _ := mintChain(t, host, certValidFrom, certValidTo)
		// Verify against an empty root pool: the signing CA is not trusted, so
		// this must fail even though clock tolerance is enabled.
		if err := verifyChainClockTolerant([]*x509.Certificate{leaf}, host, x509.NewCertPool(), badClock, true); err == nil {
			t.Fatal("expected an untrusted chain to reject regardless of clock tolerance")
		}
	})

	t.Run("empty chain is rejected", func(t *testing.T) {
		if err := verifyChainClockTolerant(nil, host, x509.NewCertPool(), badClock, true); err == nil {
			t.Fatal("expected an empty peer chain to be rejected")
		}
	})

	t.Run("empty host is rejected", func(t *testing.T) {
		leaf, roots := mintChain(t, host, certValidFrom, certValidTo)
		if err := verifyChainClockTolerant([]*x509.Certificate{leaf}, "", roots, badClock, true); err == nil {
			t.Fatal("expected an empty host to be rejected (would otherwise skip hostname checking)")
		}
	})

	t.Run("bare-IP host verifies against the cert IP SAN with a bad clock", func(t *testing.T) {
		// A radio stream URL with an IP-literal host sends no SNI; it must still
		// verify against the certificate's IP SANs (regression guard for the
		// DialTLSContext host-passing fix).
		const ip = "203.0.113.5"
		leaf, roots := mintChainSAN(t, nil, []net.IP{net.ParseIP(ip)}, certValidFrom, certValidTo)
		if err := verifyChainClockTolerant([]*x509.Certificate{leaf}, ip, roots, badClock, true); err != nil {
			t.Fatalf("expected an IP-SAN cert to verify for a bare-IP host, got %v", err)
		}
	})

	t.Run("relaxed retry still rejects an untrusted root masked by a time failure", func(t *testing.T) {
		// The cert is both not-yet-valid AND signed by an untrusted CA. The first
		// Verify fails with Reason==Expired (time is checked first), so the relax
		// branch fires — but the pinned-time re-verify must still reject on the
		// chain.
		leaf, _ := mintChain(t, host, certValidFrom, certValidTo)
		if err := verifyChainClockTolerant([]*x509.Certificate{leaf}, host, x509.NewCertPool(), badClock, true); err == nil {
			t.Fatal("relax branch must not accept an untrusted chain hidden behind a time failure")
		}
	})

	t.Run("relaxed retry still rejects a hostname mismatch masked by a time failure", func(t *testing.T) {
		leaf, roots := mintChain(t, host, certValidFrom, certValidTo)
		if err := verifyChainClockTolerant([]*x509.Certificate{leaf}, "evil.example.com", roots, badClock, true); err == nil {
			t.Fatal("relax branch must not accept a wrong-host cert hidden behind a time failure")
		}
	})
}
