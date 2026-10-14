package updatesig

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

const goodIndex = `{"version":"v1.2.3","files":{"streborn-armv7l":{"sha256":"` +
	"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `","size":100}}}`

func testKey(t *testing.T) (ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv, base64.StdEncoding.EncodeToString(pub)
}

func TestVerifyAcceptsSignedIndex(t *testing.T) {
	priv, pub := testKey(t)
	sig := Sign(priv, []byte(goodIndex))
	ix, err := Verify([]byte(goodIndex), sig+"\n", []string{"not-a-key", pub})
	if err != nil {
		t.Fatal(err)
	}
	if ix.Version != "v1.2.3" || ix.Files[AgentFile].Size != 100 {
		t.Fatalf("parsed %+v", ix)
	}
}

func TestVerifyRejectsTamperingAndForeignKeys(t *testing.T) {
	priv, pub := testKey(t)
	_, other := testKey(t)
	sig := Sign(priv, []byte(goodIndex))
	tampered := strings.Replace(goodIndex, "v1.2.3", "v9.9.9", 1)
	if _, err := Verify([]byte(tampered), sig, []string{pub}); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("tampered index: %v", err)
	}
	if _, err := Verify([]byte(goodIndex), sig, []string{other}); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("foreign key: %v", err)
	}
	if _, err := Verify([]byte(goodIndex), "garbage", []string{pub}); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("garbage signature: %v", err)
	}
	// The placeholder shipped before the release key exists verifies nothing.
	if _, err := Verify([]byte(goodIndex), sig, PublicKeys); err == nil && PublicKeys[0] != pub {
		t.Fatal("the compiled-in keys accepted a test signature")
	}
}

func TestParseRejectsBadIndexes(t *testing.T) {
	for name, body := range map[string]string{
		"no version tag": strings.Replace(goodIndex, "v1.2.3", "latest", 1),
		"bad hash":       strings.Replace(goodIndex, "aaaa", "zzzz", 1),
		"no agent":       `{"version":"v1.2.3","files":{}}`,
		"zero size":      strings.Replace(goodIndex, `"size":100`, `"size":0`, 1),
	} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestVersionLess(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v1.0.12", "v1.0.13", true},
		{"v1.0.13", "v1.0.13", false},
		{"v1.0.12-64-ge7068773-dirty", "v1.0.13", true},
		{"v1.0.13-rc1", "v1.0.13", false},
		{"v1.0.9", "v1.0.10", true},
		{"v1.1.0", "v1.0.99", false},
		{"1.0.0", "v1.0.1", true},
	} {
		if got := VersionLess(c.a, c.b); got != c.want {
			t.Errorf("VersionLess(%q, %q) = %v", c.a, c.b, got)
		}
	}
}
