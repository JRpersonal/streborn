package tlsgen

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// realRoot mints an actual self-signed certificate. The fixtures used to be
// certificate-SHAPED text, which is precisely the state that made a broken
// speaker read as healthy in a bundle, so the tests now use certificates a
// TLS stack would accept.
func realRoot(t *testing.T, cn string) string {
	t.Helper()
	return mustRoot(cn)
}

// mustRoot is the same thing for package-level fixtures, which have no
// *testing.T to fail on.
func mustRoot(cn string) string {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// publicRoots returns enough real roots to clear minPlausiblePublicRoots, so
// a test store looks like a firmware bundle rather than like a repair that
// only put our own root back.
func publicRoots(t *testing.T, n int) string {
	t.Helper()
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(realRoot(t, "public-"+strings.Repeat("x", i+1)))
	}
	return b.String()
}

// certificateShapedText parses as PEM but not as a certificate. This is what
// a header count cannot tell from a working trust store.
const certificateShapedText = "-----BEGIN CERTIFICATE-----\nUFVCTElD\n-----END CERTIFICATE-----\n"

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// The whole point of the section: tell a store that lost the vendor's
// public roots apart from a healthy one, because on the box both fail
// with the identical "unknown authority" error.
func TestTrustStoreSnapshotFlagsAStoreWithOnlyOurOwnRoot(t *testing.T) {
	dir := t.TempDir()
	strRoot := realRoot(t, "str")
	healthy := filepath.Join(dir, "healthy.crt")
	broken := filepath.Join(dir, "broken.crt")
	writeFile(t, healthy, publicRoots(t, minPlausiblePublicRoots)+strRoot)
	writeFile(t, broken, strRoot)

	got := trustStoreSnapshotPaths([]string{healthy, broken}, []byte(strRoot))
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d", len(got))
	}
	if got[0].UsableRootCount != minPlausiblePublicRoots+1 || got[0].PublicRootsMissing || !got[0].HasSTRRoot {
		t.Errorf("healthy store misreported: %+v", got[0])
	}
	if got[1].UsableRootCount != 1 || !got[1].PublicRootsMissing || !got[1].HasSTRRoot {
		t.Errorf("broken store misreported: %+v", got[1])
	}
}

// The support case that cost three releases: a store full of text that looks
// like certificates counted as healthy while every handshake on that speaker
// failed. The header count and the usable count have to disagree here, and
// the verdict has to follow the usable one.
func TestAStoreOfCertificateShapedTextIsNotHealthy(t *testing.T) {
	dir := t.TempDir()
	junk := filepath.Join(dir, "junk.crt")
	writeFile(t, junk, strings.Repeat(certificateShapedText, 158))

	got := trustStoreSnapshotPaths([]string{junk}, nil)
	if got[0].RootCount != 158 {
		t.Errorf("header count should still report what is in the file: %+v", got[0])
	}
	if got[0].UsableRootCount != 0 {
		t.Errorf("none of this parses, so nothing is usable: %+v", got[0])
	}
	if !got[0].PublicRootsMissing {
		t.Errorf("a store Go cannot use must read as broken, this is the bug that cost three releases: %+v", got[0])
	}
}

// A store that never got our root is a different fault (Bose rejects our
// server cert) and must not be reported as the one above.
func TestTrustStoreSnapshotSeparatesMissingSTRRootFromMissingPublicRoots(t *testing.T) {
	dir := t.TempDir()
	noSTR := filepath.Join(dir, "nostr.crt")
	writeFile(t, noSTR, publicRoots(t, minPlausiblePublicRoots))

	got := trustStoreSnapshotPaths([]string{noSTR}, []byte(realRoot(t, "str")))
	if got[0].HasSTRRoot {
		t.Errorf("STR root reported present although it is not: %+v", got[0])
	}
	if got[0].PublicRootsMissing {
		t.Errorf("public roots reported missing although they are there: %+v", got[0])
	}
}

// A handful of surviving roots is not a working trust store either. The
// verdict and the repair now use one threshold, so a bundle cannot call a
// store fine that the agent itself considers broken.
func TestAFewSurvivingRootsStillCountAsMissing(t *testing.T) {
	dir := t.TempDir()
	thin := filepath.Join(dir, "thin.crt")
	writeFile(t, thin, publicRoots(t, 2))

	got := trustStoreSnapshotPaths([]string{thin}, nil)
	if got[0].UsableRootCount != 2 {
		t.Fatalf("want the two roots counted: %+v", got[0])
	}
	if !got[0].PublicRootsMissing {
		t.Errorf("two roots is not a trust store, and the repair agrees: %+v", got[0])
	}
}

func TestTrustStoreSnapshotMarksAnAbsentBundle(t *testing.T) {
	got := trustStoreSnapshotPaths([]string{filepath.Join(t.TempDir(), "nope.crt")}, nil)
	if got[0].Exists || got[0].Err != "" {
		t.Errorf("absent bundle should read as not existing without an error: %+v", got[0])
	}
}

func TestReadRootCAPEMReturnsNilWithoutACA(t *testing.T) {
	if b := ReadRootCAPEM(t.TempDir()); b != nil {
		t.Errorf("want nil for a dir without a CA, got %d bytes", len(b))
	}
	dir := t.TempDir()
	root := realRoot(t, "str")
	writeFile(t, filepath.Join(dir, rootCAFile), root)
	if b := ReadRootCAPEM(dir); string(b) != root {
		t.Errorf("root CA not read back verbatim: %q", string(b))
	}
}
