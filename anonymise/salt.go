package anonymise

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// Where the salt lives, and why it is a file rather than a constant.
//
// A salt compiled into the binary is public the moment the binary is, which
// would make it decoration. A salt generated per export would break the one
// property these pseudonyms exist for: recognising the same speaker across two
// reports months apart. So it is generated once per installation, kept next to
// the program's own configuration, and never travels with a bundle.
//
// Losing it costs nothing that matters: the next export simply starts a new
// series of tokens, and a reader compares within a bundle, not against a
// stranger's.

const saltFileName = "anonymise-salt"

// LoadOrCreateSalt reads the salt from dir, creating it on first use, and
// installs it. A failure is not fatal and not silent: it returns the error so a
// caller can log it, and leaves the salt EMPTY, which is the old unsalted
// behaviour rather than a broken export.
func LoadOrCreateSalt(dir string) error {
	if dir == "" {
		return nil
	}
	path := filepath.Join(dir, saltFileName)
	if b, err := os.ReadFile(path); err == nil {
		if raw, derr := hex.DecodeString(strings.TrimSpace(string(b))); derr == nil && len(raw) >= 16 {
			SetSalt(raw)
			return nil
		}
		// A short or unreadable salt file is replaced rather than trusted: a
		// truncated write would otherwise weaken every token from then on.
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// 0600: this file is the only thing standing between a published bundle and
	// the addresses behind its tokens.
	if err := os.WriteFile(path, []byte(hex.EncodeToString(raw)), 0o600); err != nil {
		return err
	}
	SetSalt(raw)
	return nil
}
