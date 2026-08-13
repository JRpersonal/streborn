package tlsgen

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

// ReadRootCAPEM returns the persisted root CA of the given CA dir, or
// nil when it is not there yet (first boot, before EnsureBundle ran).
// Callers that only need it to enrich a status report treat nil as
// "unknown", never as an error.
func ReadRootCAPEM(caDir string) []byte {
	b, err := os.ReadFile(filepath.Join(caDir, rootCAFile))
	if err != nil {
		return nil
	}
	return b
}

// TrustStoreStatus describes one system trust store file as the agent
// sees it at runtime.
//
// Why this exists: a box whose overlaid trust store lost the vendor's
// public roots fails EVERY outbound TLS handshake with the same terse
// "x509: certificate signed by unknown authority" — https radio streams
// and the Spotify engine alike (support mail 2026-08-13, ST20 on
// v0.9.42: Deutschlandfunk and apresolve.spotify.com both refused, both
// chaining to DigiCert Global Root G2, which every healthy box trusts).
// From a diagnostic bundle that error is indistinguishable from a
// station-side problem, because nothing reported what the box actually
// trusts. RootCount does exactly that in one number.
type TrustStoreStatus struct {
	Path string `json:"path"`
	// Exists is false when the firmware has no such bundle at all
	// (the two paths serve libcurl and openssl and not every chassis
	// carries both).
	Exists bool `json:"exists"`
	// Bytes is the size the agent reads THROUGH any bind mount, i.e.
	// the overlay's size, not the pristine firmware bundle's.
	Bytes int64 `json:"bytes"`
	// RootCount is the number of PEM certificates in the file. A
	// healthy box shows a three-digit count; 1 means the overlay
	// contains STR's own root and nothing else, which is the broken
	// state described above.
	RootCount int `json:"root_count"`
	// HasSTRRoot reports whether STR's own root CA made it in. It has
	// to be there for the box to accept our Bose-domain server cert.
	HasSTRRoot bool `json:"has_str_root"`
	// PublicRootsMissing is the verdict, so a bundle can be triaged
	// without counting certificates by hand.
	PublicRootsMissing bool `json:"public_roots_missing"`
	// Err carries a read failure verbatim (a missing file is reported
	// via Exists instead).
	Err string `json:"err,omitempty"`
}

// pemCertHeader is what we count. Counting headers rather than parsing
// every certificate keeps this cheap enough to run on every
// /api/debug/state call on a speaker CPU.
const pemCertHeader = "-----BEGIN CERTIFICATE-----"

// TrustStoreSnapshot inspects the trust store paths STR overlays and
// returns one entry per path, in the order of DefaultTrustStorePaths.
// rootCAPEM is STR's own root; pass nil if it is not loaded yet, then
// HasSTRRoot stays false without changing the rest of the verdict.
func TrustStoreSnapshot(rootCAPEM []byte) []TrustStoreStatus {
	return trustStoreSnapshotPaths(DefaultTrustStorePaths, rootCAPEM)
}

func trustStoreSnapshotPaths(paths []string, rootCAPEM []byte) []TrustStoreStatus {
	out := make([]TrustStoreStatus, 0, len(paths))
	for _, p := range paths {
		s := TrustStoreStatus{Path: p}
		body, err := os.ReadFile(p)
		if err != nil {
			if !os.IsNotExist(err) {
				s.Err = err.Error()
				s.Exists = true
			}
			out = append(out, s)
			continue
		}
		s.Exists = true
		s.Bytes = int64(len(body))
		s.RootCount = strings.Count(string(body), pemCertHeader)
		if len(bytes.TrimSpace(rootCAPEM)) > 0 {
			s.HasSTRRoot = bytes.Contains(body, bytes.TrimSpace(rootCAPEM))
		}
		// The public roots are what a stream or the Spotify engine needs.
		// Subtracting STR's own root avoids calling a store healthy just
		// because we put one certificate in it.
		public := s.RootCount
		if s.HasSTRRoot && public > 0 {
			public--
		}
		s.PublicRootsMissing = public == 0
		out = append(out, s)
	}
	return out
}
