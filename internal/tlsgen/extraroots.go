package tlsgen

import (
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// Supplemental root certificates, and why the agent carries any.
//
// The scm chassis ships /etc/pki/tls/certs/ca-bundle.crt with 2 certificates
// where sm2 ships 166. The thin set lacks DigiCert Global Root G2 and ISRG
// Root X1, so those speakers refuse most https stations and the Spotify engine
// dies on its first call. The file belongs to the read-only firmware, a
// factory reset cannot change it, and Bose's update servers are gone, so
// nothing on the speaker can restore it.
//
// What this does NOT do is change what the SPEAKER trusts. Nothing is
// mounted, nothing under /etc is touched, nothing is written to NAND. The
// agent composes a bundle in tmpfs from the store the speaker already has
// PLUS the roots below, and points its OWN process at it. go-librespot
// inherits that through the environment; the Bose firmware keeps exactly the
// trust Bose shipped.
//
// The environment variable only replaces the FILE half of crypto/x509's
// loader: certDirectories is governed by SSL_CERT_DIR alone, which is never
// set here, so the directory scan stays live and the composed file is strictly
// additive (GOROOT/src/crypto/x509/root_unix.go, loadSystemRoots). This is
// specific to Go. OpenSSL reads the same variable differently, which is why
// nothing that is not Go may be pointed at this file.

//go:embed extraroots.pem
var supplementalRootsPEM []byte

// composedBundlePath is where the merged bundle lives. tmpfs, so it costs no
// NAND and is rebuilt from scratch on every boot, which is also how a
// downgrade undoes it: an older agent simply never writes it and never sets
// the variable.
var composedBundlePath = "/tmp/streborn-ca-bundle.crt"

// optOutMarker lets an owner refuse the supplement. Read once, never polled,
// the same shape as the other on-box switches.
var optOutMarker = "/mnt/nv/streborn/no-extra-roots"

// SupplementalRootsPEM returns the roots the agent carries, for the diagnostic.
func SupplementalRootsPEM() []byte { return supplementalRootsPEM }

// certFingerprints returns the SHA-256 of every parseable certificate in body.
// Certificates are compared by their bytes, never by CommonName: a name is the
// one field anybody can put anything into.
func certFingerprints(body []byte) map[string]bool {
	out := map[string]bool{}
	for rest := body; len(rest) > 0; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			continue
		}
		sum := sha256.Sum256(block.Bytes)
		out[hex.EncodeToString(sum[:])] = true
	}
	return out
}

// missingRoots returns the supplemental roots that are not already in have,
// as PEM, in the order they appear in the embedded bundle.
func missingRoots(have map[string]bool) ([]byte, []string) {
	var out []byte
	var added []string
	for rest := supplementalRootsPEM; len(rest) > 0; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(block.Bytes)
		fp := hex.EncodeToString(sum[:])
		if have[fp] {
			continue
		}
		out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes})...)
		added = append(added, c.Subject.CommonName)
	}
	return out, added
}

// SupplementResult records what happened, for the diagnostic and the log.
type SupplementResult struct {
	// Applied is true when SSL_CERT_FILE now points at the composed bundle.
	Applied bool `json:"applied"`
	// Reason names why it did not, when it did not.
	Reason string `json:"reason,omitempty"`
	// Added lists the roots that were not already on the speaker. Empty on a
	// healthy speaker, which is the normal case and not a failure.
	Added []string `json:"added,omitempty"`
	// StoreRoots is what the speaker's own stores yielded, ComposedRoots what
	// the merged file yields. The second must never be smaller.
	StoreRoots    int    `json:"storeRoots"`
	ComposedRoots int    `json:"composedRoots"`
	Path          string `json:"path,omitempty"`
}

var lastSupplement SupplementResult

// LastSupplement returns what the last composition did, for /api/debug/state.
func LastSupplement() SupplementResult { return lastSupplement }

// ApplySupplementalRoots composes the bundle and points this process at it.
//
// MUST run before anything makes a TLS connection: crypto/x509 builds the
// system pool once per process and caches it, so a late call silently does
// nothing. That failure mode is invisible in a test and total on the box.
//
// Never returns an error. A speaker whose trust cannot be improved must still
// start, with exactly the trust it had before.
func ApplySupplementalRoots(logger *slog.Logger) SupplementResult {
	res := applySupplementalRoots(DefaultTrustStorePaths, logger)
	lastSupplement = res
	return res
}

func applySupplementalRoots(paths []string, logger *slog.Logger) SupplementResult {
	res := SupplementResult{}
	if logger == nil {
		logger = slog.Default()
	}
	if _, err := os.Stat(optOutMarker); err == nil {
		res.Reason = "switched off on this speaker"
		logger.Info("supplemental roots: switched off by " + optOutMarker)
		return res
	}
	// Everything the speaker already has, in the order crypto/x509 would read
	// it. Both paths, because the loader takes the first FILE and then unions
	// both directories, and those directories are where these two files live.
	var store []byte
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		store = append(store, b...)
		if len(store) > 0 && store[len(store)-1] != '\n' {
			store = append(store, '\n')
		}
	}
	have := certFingerprints(store)
	res.StoreRoots = len(have)
	if res.StoreRoots == 0 {
		// Nothing readable to build on. Composing a file out of our 23 roots
		// alone would REPLACE a store we simply failed to read, which is the
		// one way this could make a speaker worse.
		res.Reason = "the speaker's own stores could not be read"
		logger.Warn("supplemental roots: not applied, the speaker's own trust stores could not be read")
		return res
	}
	extra, added := missingRoots(have)
	res.Added = added
	if len(extra) == 0 {
		res.Reason = "the speaker already has all of them"
		logger.Info("supplemental roots: nothing to add, this speaker already trusts all of them", "storeRoots", res.StoreRoots)
		return res
	}

	merged := append(append([]byte{}, store...), extra...)
	if err := writeAtomic(composedBundlePath, merged); err != nil {
		res.Reason = "could not write the composed bundle: " + err.Error()
		logger.Warn("supplemental roots: not applied", "err", err)
		return res
	}
	// Read the finished file back and count what a TLS stack would get out of
	// it. Trusting the bytes we just wrote is how a truncated file would take
	// away the store instead of extending it.
	back, err := os.ReadFile(composedBundlePath)
	if err != nil {
		res.Reason = "the composed bundle could not be read back: " + err.Error()
		logger.Warn("supplemental roots: not applied", "err", err)
		return res
	}
	res.ComposedRoots = len(certFingerprints(back))
	if res.ComposedRoots < res.StoreRoots || res.ComposedRoots < minPlausiblePublicRoots {
		res.Reason = fmt.Sprintf("the composed bundle would be worse than what is there (%d vs %d)", res.ComposedRoots, res.StoreRoots)
		logger.Warn("supplemental roots: not applied, the composed bundle is not an improvement",
			"composed", res.ComposedRoots, "store", res.StoreRoots)
		_ = os.Remove(composedBundlePath)
		return res
	}
	// SSL_CERT_FILE only, never SSL_CERT_DIR: the directory scan has to stay
	// live so this remains additive.
	if err := os.Setenv("SSL_CERT_FILE", composedBundlePath); err != nil {
		res.Reason = "could not set SSL_CERT_FILE: " + err.Error()
		logger.Warn("supplemental roots: not applied", "err", err)
		return res
	}
	res.Applied = true
	res.Path = composedBundlePath
	logger.Warn("supplemental roots: this speaker was missing common certificate authorities, they were added for STR's own connections",
		"added", added, "storeRoots", res.StoreRoots, "composedRoots", res.ComposedRoots, "path", composedBundlePath)
	return res
}

// writeAtomic writes to a temporary file and renames it, so nothing ever sees
// a half-written bundle under the name the environment variable points at.
func writeAtomic(path string, body []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
