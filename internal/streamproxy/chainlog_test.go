package streamproxy

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"log/slog"
	"math/big"
	"strings"
	"testing"
	"time"
)

func issuedCert(t *testing.T, cn, issuerCN string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		Issuer:       pkix.Name{CommonName: issuerCN},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("certificate: %v", err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// CreateCertificate self-signs, so the parsed issuer is the subject. The
	// log only reads the field, so set it to the name under test.
	c.Issuer = pkix.Name{CommonName: issuerCN}
	return c
}

// "certificate signed by unknown authority" reads identically whether the
// speaker lacks one authority, has a broken trust store, or sits behind
// something on the owner's network that substitutes certificates. Three
// releases went to the wrong one of those. The issuer name is what separates
// them, so it has to be in the bundle.
func TestARefusedStationIsLoggedWithTheAuthorityThatSignedIt(t *testing.T) {
	var logged bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logged, nil))

	leaf := issuedCert(t, "*.rndfnk.com", "DigiCert Global G2 TLS RSA SHA256 2020 CA1")
	inter := issuedCert(t, "DigiCert Global G2 TLS RSA SHA256 2020 CA1", "DigiCert Global Root G2")

	logRejectedChain(logger, "f131.rndfnk.com", []*x509.Certificate{leaf, inter},
		x509.NewCertPool(), errors.New("x509: certificate signed by unknown authority"))

	out := logged.String()
	for _, want := range []string{
		"f131.rndfnk.com",
		"*.rndfnk.com",
		"DigiCert Global G2 TLS RSA SHA256 2020 CA1",
		"DigiCert Global Root G2", // the authority that decides the case
		"rootsInPool=0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the failure line must carry %q, got: %s", want, out)
		}
	}
}

// A speaker whose system pool could not be built at all is a different fault
// from one missing a single authority, and the line has to say which.
func TestAnUnavailableRootPoolIsReportedAsSuch(t *testing.T) {
	var logged bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logged, nil))

	logRejectedChain(logger, "example.invalid",
		[]*x509.Certificate{issuedCert(t, "leaf", "some-ca")}, nil, errors.New("boom"))

	if !strings.Contains(logged.String(), "rootsInPool=-1") {
		t.Errorf("a missing system pool must be distinguishable from an empty one: %s", logged.String())
	}
}

// A handshake that fails before any certificate arrives has nothing to report,
// and must not add a line that looks like a rejected chain.
func TestNothingIsLoggedWithoutAChain(t *testing.T) {
	var logged bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logged, nil))
	logRejectedChain(logger, "example.invalid", nil, x509.NewCertPool(), errors.New("boom"))
	if logged.Len() != 0 {
		t.Errorf("want no line without peer certificates, got: %s", logged.String())
	}
}
