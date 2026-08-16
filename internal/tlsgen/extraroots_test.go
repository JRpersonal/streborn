package tlsgen

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// withComposedIn points the composition at a temporary directory, so a test
// never writes to the real /tmp path and never leaves SSL_CERT_FILE set.
func withComposedIn(t *testing.T) {
	t.Helper()
	prevPath, prevMarker := composedBundlePath, optOutMarker
	composedBundlePath = filepath.Join(t.TempDir(), "composed.crt")
	optOutMarker = filepath.Join(t.TempDir(), "no-extra-roots")
	prevEnv, had := os.LookupEnv("SSL_CERT_FILE")
	t.Cleanup(func() {
		composedBundlePath, optOutMarker = prevPath, prevMarker
		if had {
			_ = os.Setenv("SSL_CERT_FILE", prevEnv)
		} else {
			_ = os.Unsetenv("SSL_CERT_FILE")
		}
		lastSupplement = SupplementResult{}
	})
	_ = os.Unsetenv("SSL_CERT_FILE")
}

// The embedded bundle has to be real. A build that shipped an empty or
// unparseable file would silently do nothing on every speaker.
func TestTheShippedRootsParse(t *testing.T) {
	n := usableRoots(supplementalRootsPEM)
	if n < 10 {
		t.Fatalf("only %d supplemental roots parse, the embedded bundle is broken", n)
	}
	needed := map[string]bool{"DigiCert Global Root G2": false, "ISRG Root X1": false}
	for _, r := range wellKnownRootsInBytes(supplementalRootsPEM) {
		if _, want := needed[r]; want {
			needed[r] = true
		}
	}
	for name, present := range needed {
		if !present {
			t.Errorf("%q is missing from the shipped roots, and it is one of the two the field case needs", name)
		}
	}
}

// The whole promise: the composed bundle is what the speaker had PLUS what was
// missing. If it could ever be smaller, pointing the process at it would take
// trust away instead of adding it.
func TestTheComposedBundleIsASuperset(t *testing.T) {
	withComposedIn(t)
	dir := t.TempDir()
	store := filepath.Join(dir, "ca-certificates.crt")
	thin := filepath.Join(dir, "ca-bundle.crt")
	// A believable store that is missing the modern roots, which is the scm
	// chassis in miniature.
	writeFile(t, store, publicRoots(t, 20))
	writeFile(t, thin, mustRoot("str-root"))

	before := certFingerprints([]byte(publicRoots(t, 0) + readAll(t, store) + readAll(t, thin)))
	_ = before

	res := applySupplementalRoots([]string{store, thin}, quiet())
	if !res.Applied {
		t.Fatalf("not applied: %s", res.Reason)
	}
	if res.ComposedRoots < res.StoreRoots {
		t.Fatalf("the composed bundle is smaller than the store: %d < %d", res.ComposedRoots, res.StoreRoots)
	}
	if len(res.Added) == 0 {
		t.Error("a store without the modern roots must gain some")
	}
	// Every certificate the speaker had must still be in the result.
	had := certFingerprints([]byte(readAll(t, store) + readAll(t, thin)))
	got := certFingerprints([]byte(readAll(t, composedBundlePath)))
	for fp := range had {
		if !got[fp] {
			t.Fatalf("the composed bundle lost a certificate the speaker already had (%s)", fp[:16])
		}
	}
	if os.Getenv("SSL_CERT_FILE") != composedBundlePath {
		t.Errorf("SSL_CERT_FILE = %q, want the composed bundle", os.Getenv("SSL_CERT_FILE"))
	}
}

// A speaker whose store is complete gains nothing and must be left completely
// alone, including the environment.
func TestAHealthySpeakerIsNotTouched(t *testing.T) {
	withComposedIn(t)
	dir := t.TempDir()
	store := filepath.Join(dir, "ca-certificates.crt")
	writeFile(t, store, string(supplementalRootsPEM)+publicRoots(t, 12))

	res := applySupplementalRoots([]string{store}, quiet())
	if res.Applied {
		t.Error("a speaker that already has every one of them must not be pointed anywhere")
	}
	if len(res.Added) != 0 {
		t.Errorf("nothing should be added, got %v", res.Added)
	}
	if _, err := os.Stat(composedBundlePath); err == nil {
		t.Error("no file should be written for a healthy speaker")
	}
	if os.Getenv("SSL_CERT_FILE") != "" {
		t.Errorf("SSL_CERT_FILE was set on a healthy speaker: %q", os.Getenv("SSL_CERT_FILE"))
	}
}

// The dangerous case. If the speaker's own stores cannot be read, composing a
// file out of our roots ALONE would replace a store we merely failed to open,
// turning a readable store into 23 roots.
func TestAnUnreadableStoreIsNeverReplaced(t *testing.T) {
	withComposedIn(t)
	res := applySupplementalRoots([]string{filepath.Join(t.TempDir(), "nope.crt")}, quiet())
	if res.Applied {
		t.Fatal("with no readable store there is nothing to extend, and replacing it is the one way to make things worse")
	}
	if !strings.Contains(res.Reason, "could not be read") {
		t.Errorf("reason = %q, want it to name the unreadable store", res.Reason)
	}
	if os.Getenv("SSL_CERT_FILE") != "" {
		t.Error("SSL_CERT_FILE must stay unset when the composition was refused")
	}
}

// The owner can refuse it, and then nothing happens at all.
func TestTheOptOutMarkerStopsEverything(t *testing.T) {
	withComposedIn(t)
	if err := os.WriteFile(optOutMarker, []byte("1"), 0o644); err != nil {
		t.Fatalf("marker: %v", err)
	}
	dir := t.TempDir()
	store := filepath.Join(dir, "ca-certificates.crt")
	writeFile(t, store, publicRoots(t, 20))

	res := applySupplementalRoots([]string{store}, quiet())
	if res.Applied {
		t.Fatal("the opt-out marker must stop it")
	}
	if os.Getenv("SSL_CERT_FILE") != "" {
		t.Error("SSL_CERT_FILE must stay unset when the owner refused")
	}
}

// Certificates are matched by their bytes. Matching by CommonName would let a
// certificate with a copied name pass as one we already trust.
func TestRootsAreMatchedByBytesNotByName(t *testing.T) {
	same := mustRoot("DigiCert Global Root G2") // same NAME, different key
	have := certFingerprints([]byte(same))
	extra, added := missingRoots(have)
	if len(extra) == 0 {
		t.Fatal("a certificate that merely shares a name must not satisfy the check")
	}
	var sawIt bool
	for _, n := range added {
		if n == "DigiCert Global Root G2" {
			sawIt = true
		}
	}
	if !sawIt {
		t.Error("the real root must still be added when only a same-named impostor is present")
	}
}

func readAll(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
