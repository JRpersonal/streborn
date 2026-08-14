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
	"testing"
	"time"
)

// The whole point of this check is that a HEALTHY FILE is not proof. The store
// on the reporter's ST20 held 158 roots and every handshake still failed,
// because the process had cached an older copy. So: roots that are on disk but
// not in the process's pool must read as "not effective".
func TestRootsOnDiskThatTheProcessDoesNotTrustAreNotEffective(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "ca-bundle.crt")
	// A self-signed root the system pool cannot possibly contain.
	rootPEM := makeSelfSignedRootPEM(t)
	if err := os.WriteFile(store, rootPEM, 0o644); err != nil {
		t.Fatal(err)
	}

	effective, checked := RootsEffectiveInProcess([]string{store})

	if !checked {
		t.Fatal("a store holding a parseable certificate must produce an answer")
	}
	if effective {
		t.Error("a root the process does not trust must not count as effective")
	}
}

// And the other direction, which is the one that carries the risk: a store
// whose roots ARE what the process trusts must read as effective, because a
// false negative here restarts a perfectly healthy speaker.
func TestRootsTheProcessTrustsAreEffective(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "ca-bundle.crt")
	rootPEM := makeSelfSignedRootPEM(t)
	if err := os.WriteFile(store, rootPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	// A pool that DOES contain the root on disk, i.e. a process reading the
	// same material as the file.
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(rootPEM) {
		t.Fatal("could not seed the test pool")
	}
	prev := systemPool
	systemPool = func() (*x509.CertPool, error) { return pool, nil }
	t.Cleanup(func() { systemPool = prev })

	effective, checked := RootsEffectiveInProcess([]string{store})

	if !checked || !effective {
		t.Errorf("effective=%v checked=%v, want both true: a healthy speaker must never be restarted", effective, checked)
	}
}

// An unreadable or empty store is not evidence of a stale cache and must not
// be reported as an answer, or every speaker missing one of the two paths
// would restart itself.
func TestAnEmptyStoreProducesNoAnswer(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.crt")
	if err := os.WriteFile(empty, []byte("not a certificate\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, checked := RootsEffectiveInProcess([]string{empty}); checked {
		t.Error("a store with no parseable certificate must not produce a verdict")
	}
	if _, checked := RootsEffectiveInProcess([]string{filepath.Join(dir, "missing.crt")}); checked {
		t.Error("a missing store must not produce a verdict")
	}
}

func TestParseRootsSkipsJunkAndHonoursTheLimit(t *testing.T) {
	blob := append([]byte("garbage\n-----BEGIN NOTACERT-----\nx\n-----END NOTACERT-----\n"), makeSelfSignedRootPEM(t)...)
	got := parseRoots(blob, 10)
	if len(got) != 1 {
		t.Fatalf("parsed %d certificates, want the 1 real one", len(got))
	}
	if n := len(parseRoots(append(blob, makeSelfSignedRootPEM(t)...), 1)); n != 1 {
		t.Errorf("limit ignored: parsed %d, want 1", n)
	}
}

// makeSelfSignedRootPEM builds a throwaway self-signed CA, which by
// construction is not in any real system trust store.
func makeSelfSignedRootPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "STR test root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
