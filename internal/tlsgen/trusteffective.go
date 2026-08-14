package tlsgen

import (
	"crypto/x509"
	"encoding/pem"
	"os"
)

// Whether the trust store on disk is the trust store this PROCESS uses.
//
// These are not the same question, and a field bundle cost two releases to
// teach that. Go reads the system trust store once per process and caches it.
// The boot script mounts STR's overlay AFTER the agent has started, and the
// agent reaches for TLS within seconds of starting (the Spotify engine
// launches almost immediately). So a speaker can hold a perfectly healthy
// store on disk while every handshake in the running process still fails
// against the copy it cached at startup.
//
// The first version of the repair only restarted when it had CHANGED a file.
// On the reporter's ST20 nothing needed changing at the moment it looked: the
// store it reads was already healthy, the other one was beyond saving, and the
// agent went on failing every https station with a diagnostic that said both
// stores were fine. Nothing in the file state could have revealed that. Only
// asking the process itself can.
//
// The check is deliberately network-free. A handshake against some public host
// would answer the same question, but it would also fail for a speaker with no
// route, a captive portal or a slow DNS, and turn every one of those into a
// spurious restart.

// maxRootsProbed bounds the work: a store holds well over a hundred roots and
// parsing all of them on a speaker CPU buys nothing. Several are tried rather
// than one because an individual root may legitimately be expired, which would
// make a single sample say "stale cache" when the cache is fine.
const maxRootsProbed = 10

// systemPool is x509.SystemCertPool, behind a seam so the tests can drive the
// direction that actually carries risk: proving a HEALTHY speaker is never
// restarted needs a pool that provably contains the roots on disk, and on a
// dev host the real system pool contains neither them nor anything we can
// place there.
var systemPool = x509.SystemCertPool

// RootsEffectiveInProcess reports whether the roots currently on disk are the
// roots this process would actually trust.
//
// checked is false when the question could not be put: no readable store, or
// nothing in it that parses as a certificate. Callers must not read that as
// either answer.
func RootsEffectiveInProcess(paths []string) (effective, checked bool) {
	pool, err := systemPool()
	if err != nil || pool == nil {
		return false, false
	}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, root := range parseRoots(body, maxRootsProbed) {
			// A self-signed root verifies against a pool that contains it and
			// fails with "unknown authority" against one that does not, which
			// is exactly the distinction being drawn. One success is enough:
			// it proves the process is reading the same material as the disk.
			if _, err := root.Verify(x509.VerifyOptions{
				Roots:     pool,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
			}); err == nil {
				return true, true
			}
		}
	}
	// Something was readable but nothing in it verified. Report it as a real
	// answer only if a certificate was actually parsed, so an empty or
	// unreadable store does not masquerade as a stale cache.
	for _, path := range paths {
		if body, err := os.ReadFile(path); err == nil && len(parseRoots(body, 1)) > 0 {
			return false, true
		}
	}
	return false, false
}

// parseRoots decodes up to max certificates from a PEM bundle, skipping
// entries that do not parse. A trust store with one unreadable block in it is
// still a trust store.
func parseRoots(body []byte, max int) []*x509.Certificate {
	var out []*x509.Certificate
	rest := body
	for len(out) < max {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		out = append(out, cert)
	}
	return out
}
