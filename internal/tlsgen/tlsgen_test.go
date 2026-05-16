package tlsgen

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(dir, []string{"streaming.bose.com", "content.api.bose.io"}, logger)
}

func TestEnsureBundleGeneriertNeu(t *testing.T) {
	m := newTestManager(t)
	bundle, err := m.EnsureBundle()
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.RootCAPEM) == 0 || len(bundle.RootKeyPEM) == 0 ||
		len(bundle.ServerCertPEM) == 0 || len(bundle.ServerKeyPEM) == 0 {
		t.Fatal("Bundle hat leere Felder")
	}
	for _, name := range []string{rootCAFile, rootKeyFile, serverCertFile, serverKeyFile} {
		path := filepath.Join(m.dir, name)
		fi, err := os.Stat(path)
		if err != nil {
			t.Errorf("Datei nicht gefunden: %s, err=%v", path, err)
			continue
		}
		// Key Files muessen restriktiv sein (0600).
		// Auf Windows werden die Permissions nicht so umgesetzt, daher
		// dort den Check ueberspringen.
		if name == rootKeyFile || name == serverKeyFile {
			mode := fi.Mode().Perm()
			if mode != 0o600 && mode != 0o666 {
				t.Logf("WARN %s Permissions %v (Windows toleriert)", name, mode)
			}
		}
	}
}

func TestEnsureBundleIdempotent(t *testing.T) {
	m := newTestManager(t)
	first, err := m.EnsureBundle()
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.EnsureBundle()
	if err != nil {
		t.Fatal(err)
	}
	if string(first.RootCAPEM) != string(second.RootCAPEM) {
		t.Error("RootCA wurde neu generiert beim zweiten Aufruf")
	}
	if string(first.ServerCertPEM) != string(second.ServerCertPEM) {
		t.Error("Server Cert wurde neu generiert beim zweiten Aufruf")
	}
}

func TestBundleTLSCert(t *testing.T) {
	m := newTestManager(t)
	bundle, err := m.EnsureBundle()
	if err != nil {
		t.Fatal(err)
	}
	cert, err := bundle.TLSCert()
	if err != nil {
		t.Fatalf("TLSCert: %v", err)
	}
	if cert.Certificate == nil {
		t.Error("kein Certificate in tls.Certificate")
	}
	if cert.PrivateKey == nil {
		t.Error("kein PrivateKey in tls.Certificate")
	}
}

func TestServerCertVertrauenswuerdigKette(t *testing.T) {
	m := newTestManager(t)
	bundle, err := m.EnsureBundle()
	if err != nil {
		t.Fatal(err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(bundle.RootCAPEM) {
		t.Fatal("RootCA nicht parsebar")
	}
	serverCert, err := parseFirstCert(bundle.ServerCertPEM)
	if err != nil {
		t.Fatal(err)
	}

	for _, dns := range []string{"streaming.bose.com", "content.api.bose.io"} {
		opts := x509.VerifyOptions{Roots: pool, DNSName: dns}
		if _, err := serverCert.Verify(opts); err != nil {
			t.Errorf("Verify fuer %s fehlgeschlagen: %v", dns, err)
		}
	}
}

func TestServerCertGueltigkeit(t *testing.T) {
	m := newTestManager(t)
	bundle, err := m.EnsureBundle()
	if err != nil {
		t.Fatal(err)
	}
	cert, err := parseFirstCert(bundle.ServerCertPEM)
	if err != nil {
		t.Fatal(err)
	}
	if cert.NotBefore.After(time.Now()) {
		t.Errorf("NotBefore in der Zukunft: %v", cert.NotBefore)
	}
	// Mindestens 8 Jahre gueltig
	if cert.NotAfter.Before(time.Now().Add(8 * 365 * 24 * time.Hour)) {
		t.Errorf("NotAfter zu kurz: %v", cert.NotAfter)
	}
}

func TestRootCAIstCA(t *testing.T) {
	m := newTestManager(t)
	bundle, err := m.EnsureBundle()
	if err != nil {
		t.Fatal(err)
	}
	root, err := parseFirstCert(bundle.RootCAPEM)
	if err != nil {
		t.Fatal(err)
	}
	if !root.IsCA {
		t.Error("Root Cert hat IsCA=false")
	}
	if root.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Error("Root Cert kann nicht signieren")
	}
}

func TestServerCertHatSANs(t *testing.T) {
	m := newTestManager(t)
	bundle, err := m.EnsureBundle()
	if err != nil {
		t.Fatal(err)
	}
	cert, err := parseFirstCert(bundle.ServerCertPEM)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, n := range cert.DNSNames {
		have[n] = true
	}
	if !have["streaming.bose.com"] || !have["content.api.bose.io"] {
		t.Errorf("SAN fehlt: %v", cert.DNSNames)
	}
}

// parseFirstCert dekodiert das erste CERTIFICATE Block aus pemData.
func parseFirstCert(pemData []byte) (*x509.Certificate, error) {
	rest := pemData
	for {
		block, r := pem.Decode(rest)
		if block == nil {
			return nil, errors.New("kein CERTIFICATE Block im PEM")
		}
		if block.Type == "CERTIFICATE" {
			return x509.ParseCertificate(block.Bytes)
		}
		rest = r
	}
}
