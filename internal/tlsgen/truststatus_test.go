package tlsgen

import (
	"os"
	"path/filepath"
	"testing"
)

const fakeRoot = "-----BEGIN CERTIFICATE-----\nSFRSUk9PVA==\n-----END CERTIFICATE-----\n"
const fakePublic = "-----BEGIN CERTIFICATE-----\nUFVCTElD\n-----END CERTIFICATE-----\n"

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
	healthy := filepath.Join(dir, "healthy.crt")
	broken := filepath.Join(dir, "broken.crt")
	writeFile(t, healthy, fakePublic+fakePublic+fakeRoot)
	writeFile(t, broken, fakeRoot)

	got := trustStoreSnapshotPaths([]string{healthy, broken}, []byte(fakeRoot))
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d", len(got))
	}
	if got[0].RootCount != 3 || got[0].PublicRootsMissing || !got[0].HasSTRRoot {
		t.Errorf("healthy store misreported: %+v", got[0])
	}
	if got[1].RootCount != 1 || !got[1].PublicRootsMissing || !got[1].HasSTRRoot {
		t.Errorf("broken store misreported: %+v", got[1])
	}
}

// A store that never got our root is a different fault (Bose rejects our
// server cert) and must not be reported as the one above.
func TestTrustStoreSnapshotSeparatesMissingSTRRootFromMissingPublicRoots(t *testing.T) {
	dir := t.TempDir()
	noSTR := filepath.Join(dir, "nostr.crt")
	writeFile(t, noSTR, fakePublic)

	got := trustStoreSnapshotPaths([]string{noSTR}, []byte(fakeRoot))
	if got[0].HasSTRRoot {
		t.Errorf("STR root reported present although it is not: %+v", got[0])
	}
	if got[0].PublicRootsMissing {
		t.Errorf("public roots reported missing although one is there: %+v", got[0])
	}
}

func TestTrustStoreSnapshotMarksAnAbsentBundle(t *testing.T) {
	got := trustStoreSnapshotPaths([]string{filepath.Join(t.TempDir(), "nope.crt")}, []byte(fakeRoot))
	if got[0].Exists || got[0].Err != "" {
		t.Errorf("absent bundle should read as not existing without an error: %+v", got[0])
	}
}

func TestReadRootCAPEMReturnsNilWithoutACA(t *testing.T) {
	if b := ReadRootCAPEM(t.TempDir()); b != nil {
		t.Errorf("want nil for a dir without a CA, got %d bytes", len(b))
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, rootCAFile), fakeRoot)
	if b := ReadRootCAPEM(dir); string(b) != fakeRoot {
		t.Errorf("root CA not read back verbatim: %q", string(b))
	}
}
