// Package updatesig signs and verifies the release index a speaker reads when
// it updates itself (no desktop app involved, idea #1091).
//
// The release workflow writes update.json next to the other release files: the
// version, and the SHA-256 and size of each file a speaker may install. It signs
// those exact bytes with an Ed25519 key that lives only in the repository's
// Actions secrets, and publishes the signature as update.json.sig. The agent
// carries the matching public key and installs nothing whose index does not
// verify.
//
// Why a key of our own when every release already carries a Sigstore bundle:
// checking Sigstore needs the Fulcio and Rekor trust roots, certificate
// transparency proofs and a correct clock, and a SoundTouch speaker has none of
// that to spare (no RTC, ~120 MB of RAM, a CA bundle that is thin on some
// chassis). One Ed25519 signature is a few lines of standard library and holds
// even when the speaker's TLS trust store does not.
//
// The signature covers the version as well as the hashes, so an attacker on the
// path cannot pass off an older, genuinely signed release as a newer one. The
// agent additionally refuses anything that is not newer than what it runs.
package updatesig

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// IndexName and SigName are the release file names the agent fetches.
const (
	IndexName = "update.json"
	SigName   = "update.json.sig"
)

// Files a speaker may install from a release. Anything else listed in the index
// is ignored by the agent.
const (
	AgentFile  = "streborn-armv7l"
	EngineFile = "go-librespot-armv7l"
)

// PublicKeys are the release signing keys the agent trusts, base64 encoded.
// More than one is allowed so a key can be rotated: a release signed with the
// old key ships an agent that already trusts the new one.
var PublicKeys = []string{
	releaseKey1,
}

// releaseKey1 is the first release signing key (2026-10-14). Its private half
// is the STR_UPDATE_SIGNING_KEY Actions secret.
const releaseKey1 = "xGh/Jl/719PSDFwu8gpBmvfsfxkbn8DPPqFeTK8VDWU="

// File is one installable release file.
type File struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Index is the signed content of update.json.
type Index struct {
	Version string          `json:"version"`
	Build   string          `json:"build,omitempty"`
	Commit  string          `json:"commit,omitempty"`
	Files   map[string]File `json:"files"`
}

var (
	versionRe = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)
	sha256Re  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ErrBadSignature means no trusted key verified the index.
var ErrBadSignature = errors.New("update index signature does not verify")

// Sign returns the base64 signature of payload, as published in update.json.sig.
func Sign(priv ed25519.PrivateKey, payload []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
}

// Verify checks sig (the content of update.json.sig) over payload against the
// given base64 public keys, then parses and validates the index. Keys that do
// not decode are skipped, so a placeholder never verifies anything.
func Verify(payload []byte, sig string, keys []string) (Index, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sig))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return Index{}, ErrBadSignature
	}
	ok := false
	for _, k := range keys {
		pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(k))
		if err != nil || len(pub) != ed25519.PublicKeySize {
			continue
		}
		if ed25519.Verify(ed25519.PublicKey(pub), payload, raw) {
			ok = true
			break
		}
	}
	if !ok {
		return Index{}, ErrBadSignature
	}
	return Parse(payload)
}

// Parse decodes and validates an index without checking any signature. Only
// the release tooling and tests should call it directly.
func Parse(payload []byte) (Index, error) {
	var ix Index
	if err := json.Unmarshal(payload, &ix); err != nil {
		return Index{}, fmt.Errorf("update index: %w", err)
	}
	if !versionRe.MatchString(ix.Version) {
		return Index{}, fmt.Errorf("update index: version %q is not a release tag", ix.Version)
	}
	for name, f := range ix.Files {
		if !sha256Re.MatchString(f.SHA256) || f.Size <= 0 {
			return Index{}, fmt.Errorf("update index: %s has no valid hash and size", name)
		}
	}
	if _, ok := ix.Files[AgentFile]; !ok {
		return Index{}, fmt.Errorf("update index: no %s", AgentFile)
	}
	return ix, nil
}

// VersionLess reports whether release version a is strictly older than b. Both
// may carry a leading "v" and a git-describe or pre-release suffix
// ("-3-gabc123-dirty", "-rc1"); only the leading numeric segments count, the
// same rule the desktop app's version comparison uses, so a dev build off
// v1.2.3 compares equal to the v1.2.3 release.
func VersionLess(a, b string) bool {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return x < y
		}
	}
	return false
}

func versionParts(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v = v[:i]
	}
	var parts []int
	for _, seg := range strings.Split(v, ".") {
		n := 0
		for _, r := range seg {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		parts = append(parts, n)
	}
	return parts
}
