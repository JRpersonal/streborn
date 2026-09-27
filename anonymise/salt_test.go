package anonymise

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The promise on the tin is "anonymised", and for the tokens it was a pseudonym
// at best: a MAC fell to a 24-bit sweep in about 25 seconds once the vendor
// prefix was known, and a 306-word list of room names recovered 14% of the
// speaker names in a corpus of 72 real bundles. All 72 said Anonymized: true.
func TestASaltMakesTheTokensUnguessable(t *testing.T) {
	const mac = "AA:BB:CC:DD:EE:FF"

	SetSalt(nil)
	bare := HashShort(mac)

	SetSalt([]byte("a-machine-keeps-this"))
	salted := HashShort(mac)

	if bare == salted {
		t.Fatal("the salt changed nothing; the token is still a plain hash of the address")
	}
	// Stable within one installation: the same speaker has to be recognisable
	// across two reports months apart, which is what the tokens are FOR.
	if again := HashShort(mac); again != salted {
		t.Fatal("the same speaker hashed differently twice under one salt")
	}
	// And two households cannot be joined, because their salts differ.
	SetSalt([]byte("another-machine"))
	if HashShort(mac) == salted {
		t.Fatal("two installations produced the same token for one address")
	}
}

// The salt is generated once, kept, and never shipped.
func TestTheSaltIsCreatedOnceAndKept(t *testing.T) {
	dir := t.TempDir()
	if err := LoadOrCreateSalt(dir); err != nil {
		t.Fatalf("first call: %v", err)
	}
	first := HashShort("AA:BB:CC:DD:EE:FF")

	path := filepath.Join(dir, saltFileName)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the salt was not written: %v", err)
	}
	if len(strings.TrimSpace(string(b))) < 32 {
		t.Fatalf("the salt is too short to be worth having: %q", b)
	}

	// A second start reads it back rather than starting a new series.
	SetSalt(nil)
	if err := LoadOrCreateSalt(dir); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if got := HashShort("AA:BB:CC:DD:EE:FF"); got != first {
		t.Fatal("a restart changed every token, so a speaker cannot be followed across reports")
	}

	// A truncated or corrupted salt is replaced, not trusted.
	if err := os.WriteFile(path, []byte("ab"), 0o600); err != nil {
		t.Fatal(err)
	}
	SetSalt(nil)
	if err := LoadOrCreateSalt(dir); err != nil {
		t.Fatalf("recovery call: %v", err)
	}
	if HashShort("AA:BB:CC:DD:EE:FF") == HashShort("") {
		t.Fatal("unexpected empty token")
	}
	b2, _ := os.ReadFile(path)
	if len(strings.TrimSpace(string(b2))) < 32 {
		t.Fatal("a corrupt salt file was kept instead of replaced")
	}
}

// Nothing in an exported bundle may carry the salt itself.
func TestTheSaltNeverAppearsInScrubbedOutput(t *testing.T) {
	dir := t.TempDir()
	if err := LoadOrCreateSalt(dir); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, saltFileName))
	saltHex := strings.TrimSpace(string(b))

	out := ScrubPII("deviceID=\"AABBCCDDEEFF\" macAddress=AA:BB:CC:DD:EE:FF at 192.168.178.31")
	if strings.Contains(out, saltHex) {
		t.Fatal("the scrubbed text carries the salt")
	}
}
