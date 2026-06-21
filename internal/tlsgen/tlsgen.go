// Package tlsgen creates and manages the self-signed certificates
// for the TLS termination of the Bose cloud domains.
//
// Concept:
//
//  1. On the first start a root CA is generated and persisted in
//     /mnt/nv/... (see DefaultCADir).
//  2. With this root CA a server cert is signed that contains the Bose
//     cloud domains as SubjectAltNames (streaming.bose.com,
//     content.api.bose.io, events.api.bosecm.com, worldwide.bose.com).
//  3. On subsequent starts the files are loaded instead of regenerated.
//
// For the Bose box to accept our certs, the root CA must be entered as
// trusted in the system trust store. The setup-tls.sh script does that
// via a bind mount onto /etc/ssl/certs/ca-certificates.crt
// and /etc/pki/tls/certs/ca-bundle.crt.
package tlsgen

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// DefaultCADir is the persistence path on the box.
const DefaultCADir = "/mnt/nv/streborn/ca"

// DefaultDomains are the SANs the server cert covers. Identical to
// internal/hosts.DefaultEntries so everything matches.
var DefaultDomains = []string{
	"streaming.bose.com",
	"content.api.bose.io",
	"events.api.bosecm.com",
	"worldwide.bose.com",
	"7f5055e9ff15f2a5035a488b81ec10f4.api.radiotime.com",
	"localhost",
}

const (
	rootCAFile     = "root.crt"
	rootKeyFile    = "root.key"
	serverCertFile = "server.crt"
	serverKeyFile  = "server.key"
)

// Fixed-date validity window. The Bose box's RTC reads 2015-07-06
// right after a power-on and only catches up once HTTP-Date sync
// runs a few seconds later. The previous "now + 10y" approach broke
// when the cert was generated before clock sync — NotAfter ended up
// in 2024 from the box's 2015 viewpoint, and the cert appeared
// genuinely expired once the clock jumped to 2026. Live evidence:
// #60 on .180, 2026-05-22.
//
// Fixed absolute dates eliminate the dependency on time.Now(). The
// cert is loopback-only and signed by an STR-internal CA that we
// also install into the box's trust store, so a wide validity window
// has zero real security cost. Picked to safely cover every box
// clock state we have ever observed (2014 pre-NTP, present day, and
// a generous future).
var (
	notBeforeFixed = time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC)
	notAfterFixed  = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
)

// Bundle holds the loaded certificates and keys together.
type Bundle struct {
	RootCAPEM     []byte
	RootKeyPEM    []byte
	ServerCertPEM []byte
	ServerKeyPEM  []byte
}

// TLSCert converts the server cert plus key into a tls.Certificate for
// direct use in the http.Server.
func (b *Bundle) TLSCert() (tls.Certificate, error) {
	return tls.X509KeyPair(b.ServerCertPEM, b.ServerKeyPEM)
}

// Manager encapsulates loading and, if needed, generating the cert files.
type Manager struct {
	dir     string
	domains []string
	logger  *slog.Logger
}

// New creates a Manager. If dir is empty, DefaultCADir is used.
// If domains is empty, DefaultDomains is used.
func New(dir string, domains []string, logger *slog.Logger) *Manager {
	if dir == "" {
		dir = DefaultCADir
	}
	if domains == nil {
		domains = DefaultDomains
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{dir: dir, domains: domains, logger: logger}
}

// EnsureBundle loads the bundle from the directory or generates a new one
// if nothing is there. Idempotent.
//
// Also re-generates an existing bundle if its server cert was signed
// with the old "now + 9y" scheme and now has NotAfter in the past or
// in the immediate future. Otherwise stale bundles generated before
// the clock synced (cold-boot RTC=2015) carry forward and present as
// "expired" once NTP catches up — observed on #60 .180,
// 2026-05-22. The fresh bundle uses the fixed 2010-2099 window and
// is safe forever.
//
// The second return value is true when an existing bundle was
// replaced (i.e. the on-NAND root CA changed). First-ever generation
// returns false: in that case run.sh's bind-mount block on the stick
// reads the just-written root.crt and the trust store is consistent
// from the start. The regenerated=true signal is what the agent uses
// to trigger RefreshTrustStore, because the trust store overlay was
// already populated with the now-superseded root.crt before EnsureBundle
// ran — see #60 .180 and #80 .144 (May 2026).
func (m *Manager) EnsureBundle() (*Bundle, bool, error) {
	regenerated := false
	bundle, err := m.load()
	if err == nil {
		if m.bundleNeedsRegen(bundle) {
			m.logger.Warn("TLS bundle has stale validity window, regenerating",
				"dir", m.dir)
			regenerated = true
		} else {
			m.logger.Info("TLS bundle loaded", "dir", m.dir)
			return bundle, false, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, false, fmt.Errorf("loading bundle failed (not regenerating because the failure was not a missing file): %w", err)
	} else {
		m.logger.Info("TLS bundle not present, generating", "dir", m.dir)
	}

	bundle, err = m.generate()
	if err != nil {
		return nil, false, fmt.Errorf("generate: %w", err)
	}
	if err := m.save(bundle); err != nil {
		return nil, false, fmt.Errorf("save: %w", err)
	}
	m.logger.Info("TLS bundle persisted", "dir", m.dir)
	return bundle, regenerated, nil
}

// bundleNeedsRegen returns true if the loaded server cert has a
// NotAfter that is in the past or within the next year, which is a
// good proxy for "this was generated with the pre-2026-05 relative
// validity scheme and is unsafe to keep". A bundle generated with
// the fixed 2010-2099 window always has NotAfter way out in 2099 and
// passes this check trivially.
func (m *Manager) bundleNeedsRegen(b *Bundle) bool {
	if b == nil || len(b.ServerCertPEM) == 0 {
		return true
	}
	block, _ := pem.Decode(b.ServerCertPEM)
	if block == nil {
		return true
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return true
	}
	// Threshold: regenerate if the cert is "soon" expiring or already
	// expired by box clock standards. One year is plenty given the
	// new bundles last decades.
	threshold := time.Now().Add(365 * 24 * time.Hour)
	return cert.NotAfter.Before(threshold)
}

// load reads all four files from the directory. ErrNotExist if even a
// single file is missing.
func (m *Manager) load() (*Bundle, error) {
	bundle := &Bundle{}
	files := []struct {
		name string
		dst  *[]byte
	}{
		{rootCAFile, &bundle.RootCAPEM},
		{rootKeyFile, &bundle.RootKeyPEM},
		{serverCertFile, &bundle.ServerCertPEM},
		{serverKeyFile, &bundle.ServerKeyPEM},
	}
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(m.dir, f.name))
		if err != nil {
			return nil, err
		}
		*f.dst = data
	}
	return bundle, nil
}

// save writes the bundle to disk with restrictive permissions.
func (m *Manager) save(b *Bundle) error {
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return err
	}
	files := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{rootCAFile, b.RootCAPEM, 0o644},
		{rootKeyFile, b.RootKeyPEM, 0o600},
		{serverCertFile, b.ServerCertPEM, 0o644},
		{serverKeyFile, b.ServerKeyPEM, 0o600},
	}
	for _, f := range files {
		dst := filepath.Join(m.dir, f.name)
		tmp := dst + ".new"
		if err := os.WriteFile(tmp, f.data, f.mode); err != nil {
			return err
		}
		if err := os.Rename(tmp, dst); err != nil {
			return err
		}
	}
	return nil
}

// generate produces a completely new bundle.
func (m *Manager) generate() (*Bundle, error) {
	// Root CA key
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("root key: %w", err)
	}

	rootSerial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	rootTpl := &x509.Certificate{
		SerialNumber: rootSerial,
		Subject: pkix.Name{
			CommonName:   "STR Root CA",
			Organization: []string{"STR"},
		},
		NotBefore:             notBeforeFixed,
		NotAfter:              notAfterFixed,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTpl, rootTpl, &rootKey.PublicKey, rootKey)
	if err != nil {
		return nil, fmt.Errorf("sign root CA: %w", err)
	}

	// Server key
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("server key: %w", err)
	}
	serverSerial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	serverTpl := &x509.Certificate{
		SerialNumber: serverSerial,
		Subject: pkix.Name{
			CommonName:   m.domains[0],
			Organization: []string{"STR"},
		},
		NotBefore:   notBeforeFixed,
		NotAfter:    notAfterFixed,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    m.domains,
	}
	// Sign with the root CA
	rootCert, err := x509.ParseCertificate(rootDER)
	if err != nil {
		return nil, fmt.Errorf("parse root cert: %w", err)
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTpl, rootCert, &serverKey.PublicKey, rootKey)
	if err != nil {
		return nil, fmt.Errorf("sign server cert: %w", err)
	}

	// PEM encode
	bundle := &Bundle{
		RootCAPEM:     pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER}),
		ServerCertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}),
	}
	bundle.RootKeyPEM, err = encodeECKey(rootKey)
	if err != nil {
		return nil, err
	}
	bundle.ServerKeyPEM, err = encodeECKey(serverKey)
	if err != nil {
		return nil, err
	}
	return bundle, nil
}

func encodeECKey(k *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

func randomSerial() (*big.Int, error) {
	max := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, max)
}
