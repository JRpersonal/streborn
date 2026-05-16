// Package tlsgen erstellt und verwaltet die selbst signierten Zertifikate
// fuer die TLS Termination der Bose Cloud Domains.
//
// Konzept:
//
//  1. Beim ersten Start wird eine Root CA generiert und in /mnt/nv/...
//     persistiert (siehe DefaultCADir).
//  2. Mit dieser Root CA wird ein Server Cert signiert das die Bose Cloud
//     Domains als SubjectAltNames enthaelt (streaming.bose.com,
//     content.api.bose.io, events.api.bosecm.com, worldwide.bose.com).
//  3. Bei weiteren Starts werden die Files geladen statt neu generiert.
//
// Damit die Bose Box unsere Certs akzeptiert, muss die Root CA als
// vertrauenswuerdig im System Trust Store eingetragen sein. Das macht
// das setup-tls.sh Skript via bind mount auf /etc/ssl/certs/ca-certificates.crt
// und /etc/pki/tls/certs/ca-bundle.crt.
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

// DefaultCADir ist der Persistenz Pfad auf der Box.
const DefaultCADir = "/mnt/nv/streborn/ca"

// DefaultDomains sind die SANs die das Server Cert abdeckt. Identisch zu
// internal/hosts.DefaultEntries damit alles passt.
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

	// CA Cert gilt 10 Jahre (Box wird vermutlich nicht laenger leben).
	rootValidity = 10 * 365 * 24 * time.Hour
	// Server Cert gilt 9 Jahre, also etwas weniger als die CA.
	serverValidity = 9 * 365 * 24 * time.Hour
)

// Bundle haelt die geladenen Zertifikate und Keys zusammen.
type Bundle struct {
	RootCAPEM     []byte
	RootKeyPEM    []byte
	ServerCertPEM []byte
	ServerKeyPEM  []byte
}

// TLSCert wandelt das Server Cert plus Key in ein tls.Certificate fuer
// die direkte Verwendung im http.Server.
func (b *Bundle) TLSCert() (tls.Certificate, error) {
	return tls.X509KeyPair(b.ServerCertPEM, b.ServerKeyPEM)
}

// Manager kapselt das Laden und ggf. Generieren der Cert Files.
type Manager struct {
	dir     string
	domains []string
	logger  *slog.Logger
}

// New erstellt einen Manager. Wenn dir leer ist wird DefaultCADir genommen.
// Wenn domains leer ist DefaultDomains.
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

// EnsureBundle laedt die Bundle aus dem Verzeichnis oder generiert ein neues
// wenn nichts da ist. Idempotent.
func (m *Manager) EnsureBundle() (*Bundle, error) {
	bundle, err := m.load()
	if err == nil {
		m.logger.Info("TLS Bundle geladen", "dir", m.dir)
		return bundle, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("Bundle Laden fehlgeschlagen, regeneriere nicht weil Fehler: %w", err)
	}

	m.logger.Info("TLS Bundle nicht vorhanden, generiere neu", "dir", m.dir)
	bundle, err = m.generate()
	if err != nil {
		return nil, fmt.Errorf("generieren: %w", err)
	}
	if err := m.save(bundle); err != nil {
		return nil, fmt.Errorf("speichern: %w", err)
	}
	m.logger.Info("TLS Bundle persistiert", "dir", m.dir)
	return bundle, nil
}

// load liest alle vier Files aus dem Verzeichnis. ErrNotExist wenn auch nur
// eine Datei fehlt.
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

// save schreibt das Bundle auf die Platte mit restriktiven Permissions.
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

// generate erzeugt ein komplett neues Bundle.
func (m *Manager) generate() (*Bundle, error) {
	// Root CA Schluessel
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("Root Key: %w", err)
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
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(rootValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTpl, rootTpl, &rootKey.PublicKey, rootKey)
	if err != nil {
		return nil, fmt.Errorf("Root CA signieren: %w", err)
	}

	// Server Schluessel
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("Server Key: %w", err)
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
		NotBefore:   time.Now().Add(-1 * time.Hour),
		NotAfter:    time.Now().Add(serverValidity),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    m.domains,
	}
	// Mit Root CA signieren
	rootCert, err := x509.ParseCertificate(rootDER)
	if err != nil {
		return nil, fmt.Errorf("Root Cert parsen: %w", err)
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTpl, rootCert, &serverKey.PublicKey, rootKey)
	if err != nil {
		return nil, fmt.Errorf("Server Cert signieren: %w", err)
	}

	// PEM kodieren
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
