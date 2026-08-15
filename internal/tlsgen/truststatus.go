package tlsgen

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
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
	// RootCount is the number of PEM certificate HEADERS in the file. It
	// says how much looks like a certificate, not how much is one, so
	// never triage on it alone: see UsableRootCount.
	RootCount int `json:"root_count"`
	// UsableRootCount is the number of certificates Go actually accepts
	// out of this file, parsed the same way crypto/x509 parses it when
	// it builds the system pool.
	//
	// The two numbers came apart on a support case and cost three
	// releases (ST20, 2026-08-15). The bundle reported 158 roots and was
	// read as healthy, so all three fixes went looking elsewhere, while
	// every TLS handshake on that speaker kept failing. A count of PEM
	// headers cannot distinguish a working store from a file full of
	// certificate-shaped text, and the failing store is exactly the one
	// where that distinction decides the case.
	UsableRootCount int `json:"usable_root_count"`
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

const pemCertHeader = "-----BEGIN CERTIFICATE-----"

// usableRoots counts the certificates crypto/x509 would take out of this
// file when it builds the system pool. AppendCertsFromPEM does exactly
// this walk and silently drops whatever fails to parse, so counting the
// same way is the only number that predicts whether a handshake works.
//
// It parses on every call. On a speaker CPU a full 166-root bundle costs
// single-digit milliseconds, which the earlier header count was not worth
// saving.
func usableRoots(body []byte) int {
	n := 0
	for rest := body; len(rest) > 0; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			continue
		}
		if _, err := x509.ParseCertificate(block.Bytes); err == nil {
			n++
		}
	}
	return n
}

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
		s.UsableRootCount = usableRoots(body)
		if len(bytes.TrimSpace(rootCAPEM)) > 0 {
			s.HasSTRRoot = bytes.Contains(body, bytes.TrimSpace(rootCAPEM))
		}
		// The public roots are what a stream or the Spotify engine needs,
		// and only the ones that parse count. Subtracting STR's own root
		// avoids calling a store healthy just because we put one
		// certificate in it.
		public := s.UsableRootCount
		if s.HasSTRRoot && public > 0 {
			public--
		}
		// Same threshold the repair uses. The two disagreed until now, so
		// a store the repair considered broken was reported as fine in the
		// bundle, which is the worst of both.
		s.PublicRootsMissing = public < minPlausiblePublicRoots
		out = append(out, s)
	}
	return out
}

// WellKnownRoot names one widely used certificate authority and whether this
// speaker has it.
//
// "certificate signed by unknown authority" names the authority the STATION
// presented; it cannot say whether the speaker has it. On an ST20 whose store
// held 157 usable certificates, two of the most common roots in the world were
// nevertheless absent, and there was no way to see that from a bundle. These
// few names are the ones behind most internet radio and the streaming
// services, so their presence answers "is this speaker's set simply too old?"
// in one line.
type WellKnownRoot struct {
	Name    string `json:"name"`
	Present bool   `json:"present"`
}

// wellKnownRootCNs are matched against a certificate's Subject CommonName.
var wellKnownRootCNs = []string{
	"DigiCert Global Root G2",
	"DigiCert Global Root CA",
	"ISRG Root X1",
	"Amazon Root CA 1",
	"Baltimore CyberTrust Root",
	"GlobalSign Root CA",
	"USERTrust RSA Certification Authority",
	"Go Daddy Root Certificate Authority - G2",
}

// WellKnownRoots reports which of the common authorities this speaker's trust
// stores carry, across ALL of them: crypto/x509 reads one store file and then
// unions both certificate directories, so a root present in any of them counts.
func WellKnownRoots() []WellKnownRoot {
	return wellKnownRootsIn(DefaultTrustStorePaths)
}

func wellKnownRootsIn(paths []string) []WellKnownRoot {
	have := map[string]bool{}
	for _, p := range paths {
		body, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for rest := body; len(rest) > 0; {
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
			have[strings.TrimSpace(c.Subject.CommonName)] = true
		}
	}
	out := make([]WellKnownRoot, 0, len(wellKnownRootCNs))
	for _, cn := range wellKnownRootCNs {
		out = append(out, WellKnownRoot{Name: cn, Present: have[cn]})
	}
	return out
}
