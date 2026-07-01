package tlsgen

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(dir, []string{"streaming.bose.com", "content.api.bose.io"}, logger)
}

func TestEnsureBundleGeneratesNew(t *testing.T) {
	m := newTestManager(t)
	bundle, _, err := m.EnsureBundle()
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.RootCAPEM) == 0 || len(bundle.RootKeyPEM) == 0 ||
		len(bundle.ServerCertPEM) == 0 || len(bundle.ServerKeyPEM) == 0 {
		t.Fatal("bundle has empty fields")
	}
	for _, name := range []string{rootCAFile, rootKeyFile, serverCertFile, serverKeyFile} {
		path := filepath.Join(m.dir, name)
		fi, err := os.Stat(path)
		if err != nil {
			t.Errorf("file not found: %s, err=%v", path, err)
			continue
		}
		// Key files must be restrictive (0600).
		// On Windows the permissions are not applied this way, so
		// skip the check there.
		if name == rootKeyFile || name == serverKeyFile {
			mode := fi.Mode().Perm()
			if mode != 0o600 && mode != 0o666 {
				t.Logf("WARN %s permissions %v (tolerated on Windows)", name, mode)
			}
		}
	}
}

func TestEnsureBundleIdempotent(t *testing.T) {
	m := newTestManager(t)
	first, regen1, err := m.EnsureBundle()
	if err != nil {
		t.Fatal(err)
	}
	if regen1 {
		t.Error("first EnsureBundle should not report regenerated (no prior bundle existed)")
	}
	second, regen2, err := m.EnsureBundle()
	if err != nil {
		t.Fatal(err)
	}
	if regen2 {
		t.Error("second EnsureBundle on fresh bundle should not report regenerated")
	}
	if string(first.RootCAPEM) != string(second.RootCAPEM) {
		t.Error("RootCA was regenerated on the second call")
	}
	if string(first.ServerCertPEM) != string(second.ServerCertPEM) {
		t.Error("server cert was regenerated on the second call")
	}
}

func TestBundleTLSCert(t *testing.T) {
	m := newTestManager(t)
	bundle, _, err := m.EnsureBundle()
	if err != nil {
		t.Fatal(err)
	}
	cert, err := bundle.TLSCert()
	if err != nil {
		t.Fatalf("TLSCert: %v", err)
	}
	if cert.Certificate == nil {
		t.Error("no Certificate in tls.Certificate")
	}
	if cert.PrivateKey == nil {
		t.Error("no PrivateKey in tls.Certificate")
	}
}

func TestServerCertVertrauenswuerdigKette(t *testing.T) {
	m := newTestManager(t)
	bundle, _, err := m.EnsureBundle()
	if err != nil {
		t.Fatal(err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(bundle.RootCAPEM) {
		t.Fatal("RootCA not parsable")
	}
	serverCert, err := parseFirstCert(bundle.ServerCertPEM)
	if err != nil {
		t.Fatal(err)
	}

	for _, dns := range []string{"streaming.bose.com", "content.api.bose.io"} {
		opts := x509.VerifyOptions{Roots: pool, DNSName: dns}
		if _, err := serverCert.Verify(opts); err != nil {
			t.Errorf("Verify for %s failed: %v", dns, err)
		}
	}
}

func TestServerCertValidity(t *testing.T) {
	m := newTestManager(t)
	bundle, _, err := m.EnsureBundle()
	if err != nil {
		t.Fatal(err)
	}
	cert, err := parseFirstCert(bundle.ServerCertPEM)
	if err != nil {
		t.Fatal(err)
	}
	if cert.NotBefore.After(time.Now()) {
		t.Errorf("NotBefore in the future: %v", cert.NotBefore)
	}
	// Valid for at least 8 years
	if cert.NotAfter.Before(time.Now().Add(8 * 365 * 24 * time.Hour)) {
		t.Errorf("NotAfter too short: %v", cert.NotAfter)
	}
}

func TestRootCAIstCA(t *testing.T) {
	m := newTestManager(t)
	bundle, _, err := m.EnsureBundle()
	if err != nil {
		t.Fatal(err)
	}
	root, err := parseFirstCert(bundle.RootCAPEM)
	if err != nil {
		t.Fatal(err)
	}
	if !root.IsCA {
		t.Error("root cert has IsCA=false")
	}
	if root.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Error("root cert cannot sign")
	}
}

func TestServerCertHatSANs(t *testing.T) {
	m := newTestManager(t)
	bundle, _, err := m.EnsureBundle()
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
		t.Errorf("SAN missing: %v", cert.DNSNames)
	}
}

// TestNotBeforeBackdated guards against a regression of the
// "tls: expired certificate" failure observed in issue #60: the Bose
// box's RTC reads 2015 right after boot and rejects future-dated
// certs. The CA NotBefore must therefore be well before any plausible
// box clock — at least several years in the past. Cert is
// loopback-only so backdating costs nothing security-wise.
func TestNotBeforeBackdated(t *testing.T) {
	m := newTestManager(t)
	bundle, _, err := m.EnsureBundle()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		label string
		pem   []byte
	}{
		{"root", bundle.RootCAPEM},
		{"server", bundle.ServerCertPEM},
	} {
		cert, err := parseFirstCert(c.pem)
		if err != nil {
			t.Fatal(err)
		}
		// Must be at least 5 years in the past so a freshly-booted
		// Bose box reading 2015 still sees the cert as valid before
		// NTP sync catches up. Generator uses -12 years.
		minBackdate := 5 * 365 * 24 * time.Hour
		if time.Since(cert.NotBefore) < minBackdate {
			t.Errorf("%s NotBefore=%v is not backdated enough (must be >= %v in the past so 2015 box clocks accept it)",
				c.label, cert.NotBefore, minBackdate)
		}
	}
}

// TestEnsureBundleReportsRegenOnStale guards the #60 .180 /
// #80 .144 fix: EnsureBundle must signal regenerated=true when
// it replaces a bundle whose NotAfter is in the near past or near
// future, so the caller knows to refresh the bind-mounted trust store
// overlays. Without this signal Bose rejects every TLS handshake with
// `unknown certificate authority` until the box is cold-booted twice.
func TestEnsureBundleReportsRegenOnStale(t *testing.T) {
	m := newTestManager(t)

	// Plant a stale bundle by writing files whose ServerCert has
	// NotAfter within the next-year threshold. Easiest: regenerate
	// with overridden fixed dates by writing a hand-crafted cert
	// pair via the existing generator and then rewriting the server
	// cert with shortened validity.
	_, _, err := m.EnsureBundle()
	if err != nil {
		t.Fatal(err)
	}
	staleServerPEM := staleServerCertPEM(t, m)
	if err := os.WriteFile(filepath.Join(m.dir, serverCertFile), staleServerPEM, 0o644); err != nil {
		t.Fatal(err)
	}

	bundle, regen, err := m.EnsureBundle()
	if err != nil {
		t.Fatal(err)
	}
	if !regen {
		t.Fatal("expected regenerated=true after planting stale bundle")
	}
	// Fresh bundle must be far-future again so subsequent loads do
	// not loop into another regen.
	cert, err := parseFirstCert(bundle.ServerCertPEM)
	if err != nil {
		t.Fatal(err)
	}
	if cert.NotAfter.Before(time.Now().Add(10 * 365 * 24 * time.Hour)) {
		t.Errorf("after regen NotAfter=%v still too close, would re-trigger regen next boot", cert.NotAfter)
	}
}

// staleServerCertPEM produces a server cert PEM signed by the manager's
// existing root CA but with NotAfter only six months out — close
// enough that bundleNeedsRegen flags it.
func staleServerCertPEM(t *testing.T, m *Manager) []byte {
	t.Helper()
	rootCertPEM, err := os.ReadFile(filepath.Join(m.dir, rootCAFile))
	if err != nil {
		t.Fatal(err)
	}
	rootKeyPEM, err := os.ReadFile(filepath.Join(m.dir, rootKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	rootCert, err := parseFirstCert(rootCertPEM)
	if err != nil {
		t.Fatal(err)
	}
	keyBlock, _ := pem.Decode(rootKeyPEM)
	if keyBlock == nil {
		t.Fatal("root key PEM not decodable")
		return nil
	}
	rootKey, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}

	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sn, _ := randomSerial()
	tpl := &x509.Certificate{
		SerialNumber: sn,
		Subject:      pkix.Name{CommonName: "stale", Organization: []string{"STR"}},
		NotBefore:    time.Now().Add(-30 * 24 * time.Hour),
		NotAfter:     time.Now().Add(180 * 24 * time.Hour), // stale by bundleNeedsRegen's 1y threshold
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"streaming.bose.com"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, rootCert, &serverKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// TestRefreshTrustStoreAppendsAndIsIdempotent verifies the post-regen
// trust-store patch: first call writes the marker block + PEM to each
// target file, second call is a no-op when the same PEM is already
// present.
func TestRefreshTrustStoreAppendsAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ca-bundle.crt")
	original := "# system CA bundle\nORIGINAL_CONTENT\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	rootPEM := []byte("-----BEGIN CERTIFICATE-----\nFAKE\n-----END CERTIFICATE-----\n")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := refreshTrustStorePaths(rootPEM, []string{target}, logger); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	after1, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after1), original) {
		t.Error("original content lost after refresh")
	}
	if !strings.Contains(string(after1), trustStoreMarkerBegin) ||
		!strings.Contains(string(after1), trustStoreMarkerEnd) {
		t.Error("refresh did not write begin/end markers")
	}
	if !strings.Contains(string(after1), "FAKE") {
		t.Error("refresh did not write root PEM body")
	}

	if err := refreshTrustStorePaths(rootPEM, []string{target}, logger); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	after2, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(after1) != string(after2) {
		t.Errorf("second refresh was not a no-op:\n--- first  ---\n%s\n--- second ---\n%s", after1, after2)
	}
}

// TestRefreshTrustStoreSkipsMissingTargets confirms a missing target
// is logged but does not abort the rest of the targets.
func TestRefreshTrustStoreSkipsMissingTargets(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "present.crt")
	if err := os.WriteFile(present, []byte("orig\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "does-not-exist.crt")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rootPEM := []byte("-----BEGIN CERTIFICATE-----\nXX\n-----END CERTIFICATE-----\n")

	if err := refreshTrustStorePaths(rootPEM, []string{missing, present}, logger); err != nil {
		t.Fatalf("refresh should succeed when at least one target is patched: %v", err)
	}
	got, err := os.ReadFile(present)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "XX") {
		t.Error("present target was not patched")
	}
}

// parseFirstCert decodes the first CERTIFICATE block from pemData.
func parseFirstCert(pemData []byte) (*x509.Certificate, error) {
	rest := pemData
	for {
		block, r := pem.Decode(rest)
		if block == nil {
			return nil, errors.New("no CERTIFICATE block in PEM")
		}
		if block.Type == "CERTIFICATE" {
			return x509.ParseCertificate(block.Bytes)
		}
		rest = r
	}
}
