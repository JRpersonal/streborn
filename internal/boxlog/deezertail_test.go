package boxlog

import (
	"strings"
	"testing"
	"time"
)

// Invented line shapes: nobody has seen the firmware log a Deezer login yet
// (docs/streaming/deezer.md, A2). The placeholders are the repo's own.
const (
	lineDeezerLogin  = `Nov 21 20:01:02 taigan local0.info BoseApp[1959]: [(002011):MusicServiceAccount:INFO]setMusicServiceAccount source=DEEZER user=user@example.com`
	lineDeezerClient = `Nov 21 20:01:03 taigan local0.err BoseApp[1959]: [(002011):DeezerClient:ERROR]login failed access_token=abc123secret expires=0`
	lineDeezerPass   = `Nov 21 20:01:03 taigan local0.info BoseApp[1959]: [(002011):Sources:INFO]DEEZER credentials pass=hunter2 accepted`
)

// The Deezer tail keeps every Deezer or account-call line, apart from the raw
// tail, and only allocates once one arrives.
func TestDeezerTailKeepsTheLoginLines(t *testing.T) {
	f := newForensics()
	now := time.Date(2026, 11, 21, 20, 1, 2, 0, time.UTC)
	f.observe(lineBadURL, now)
	if f.deezer != nil {
		t.Fatal("the Deezer tail was allocated without a Deezer line")
	}
	if got := f.deezerSnapshot(); got["seen"].(uint64) != 0 || len(got["lines"].([]string)) != 0 {
		t.Fatalf("empty snapshot = %v", got)
	}
	f.observe(lineDeezerLogin, now)
	f.observe(lineDeezerClient, now)
	f.observe(lineDeezerPass, now)
	snap := f.deezerSnapshot()
	lines := snap["lines"].([]string)
	if len(lines) != 3 || snap["seen"].(uint64) != 3 {
		t.Fatalf("Deezer tail = %v", snap)
	}
	joined := strings.Join(lines, "\n")
	for _, secret := range []string{"user@example.com", "abc123secret", "hunter2"} {
		if strings.Contains(joined, secret) {
			t.Errorf("the Deezer tail carries %q: %s", secret, joined)
		}
	}
	if !strings.Contains(joined, "us***@example.com") {
		t.Errorf("the account is not masked to its first two characters and domain: %s", joined)
	}
	// The raw tail redacts the same way.
	if raw := strings.Join(f.tailSnapshot(), "\n"); strings.Contains(raw, "hunter2") || strings.Contains(raw, "user@example.com") {
		t.Errorf("the raw tail carries a secret: %s", raw)
	}
}

func TestDeezerTailIsBounded(t *testing.T) {
	f := newForensics()
	now := time.Now()
	for i := 0; i < deezerRingLen+25; i++ {
		f.observe(lineDeezerLogin, now)
	}
	snap := f.deezerSnapshot()
	if n := len(snap["lines"].([]string)); n != deezerRingLen {
		t.Fatalf("Deezer tail holds %d lines, want %d", n, deezerRingLen)
	}
	if snap["seen"].(uint64) != uint64(deezerRingLen+25) {
		t.Fatalf("seen = %v", snap["seen"])
	}
}

func TestRedactMasksAccountsAndTokens(t *testing.T) {
	for in, want := range map[string]string{
		"login for jane.doe@example.com ok": "login for ja***@example.com ok",
		"a@example.com":                     "a***@example.com",
		"Authorization token: eyJhbGciOi":   "Authorization token: <secret-redacted>",
		"refresh_token=xyz rest":            "refresh_token=<secret-redacted>",
		"arl=0123456789abcdef":              "arl=<secret-redacted>",
		// BoseApp's own music-service login line (#2040).
		`STSServiceProxy::SendCreateAccountRequest(usr:jane.doe@example.com, pw:hunter2*DEEZER), resp:<proto{ user: "x"`: "STSServiceProxy::SendCreateAccountRequest(usr:ja***@example.com, pw:<secret-redacted>",
		"pwd=hunter2":           "pwd=<secret-redacted>",
		"secretType=token kept": "secretType=token kept",
		"no account here":       "no account here",
	} {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}
