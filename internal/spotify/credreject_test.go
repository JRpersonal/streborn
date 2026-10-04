package spotify

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The engine's own words, as a field bundle logged them (ST10, v1.0.2,
// 2026-10-04), lowercased the way noteLibrespotLine sees them.
const (
	fatalStoredCredLine = `time="2026-10-04T09:23:48-05:00" level=fatal msg="daemon exited with error" error="failed authenticating accesspoint with stored credentials: failed authenticating: accesspoint login failed: BadCredentials <nil>"`
	retryBadCredLine    = `time="2026-10-04T09:23:47-05:00" level=warning msg="failed connecting to accesspoint, retrying" error="failed authenticating: accesspoint login failed: BadCredentials <nil>"`
)

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// seedDeadLogin gives m a state.json holding a saved login plus the matching
// per-account copy, the layout a speaker that was once tapped has.
func seedDeadLogin(t *testing.T, m *Manager) storedCredential {
	t.Helper()
	cred := storedCredential{Username: "user-id-here", Data: []byte("dead-blob")}
	writeJSON(t, filepath.Join(m.configDir, "state.json"), map[string]any{
		"device_id":   "device-id-here",
		"last_volume": 42,
		"credentials": cred,
	})
	writeJSON(t, filepath.Join(m.credStore, sanitizeUser(cred.Username)+".json"), cred)
	return cred
}

func TestStoredCredentialRejectionIsRecognised(t *testing.T) {
	if !isStoredCredentialRejection(strings.ToLower(fatalStoredCredLine)) {
		t.Fatal("the fatal stored-credential refusal was not recognised")
	}
	// The retries before it are not the verdict: only the exit is.
	if isStoredCredentialRejection(strings.ToLower(retryBadCredLine)) {
		t.Fatal("a retry warning was taken for the final refusal")
	}
	if isStoredCredentialRejection(`level=info msg="authenticated ap" username=x`) {
		t.Fatal("a successful login was taken for a refusal")
	}
}

// The field case: Spotify refuses the saved login. The supervisor must set it
// aside (backup kept, the rest of state.json intact) and restart quickly into
// a tap-ready engine instead of relaunching into the same refusal every minute.
func TestRefusedLoginIsSetAsideAndEngineRestartsTapReady(t *testing.T) {
	m := newTestManagerAt(t, "http://127.0.0.1:1")
	cred := seedDeadLogin(t, m)

	m.resetCredRejectRun()
	m.noteLibrespotLine(retryBadCredLine)
	m.noteLibrespotLine(fatalStoredCredLine)
	wait, handled := m.afterEngineExit()
	if !handled {
		t.Fatal("a refused saved login was not handled")
	}
	if wait != credRejectQuickRestart {
		t.Fatalf("first refusal with the login set aside waits %s, want %s", wait, credRejectQuickRestart)
	}

	var st map[string]json.RawMessage
	b, err := os.ReadFile(filepath.Join(m.configDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	if _, ok := st["credentials"]; ok {
		t.Fatal("the refused login is still in state.json")
	}
	if string(st["device_id"]) != `"device-id-here"` || string(st["last_volume"]) != "42" {
		t.Fatalf("other engine state was not preserved: %s", b)
	}

	var backup storedCredential
	bb, err := os.ReadFile(filepath.Join(m.configDir, rejectedCredentialFile))
	if err != nil {
		t.Fatalf("no backup of the refused login: %v", err)
	}
	if err := json.Unmarshal(bb, &backup); err != nil || backup.Username != cred.Username || string(backup.Data) != string(cred.Data) {
		t.Fatalf("backup does not hold the refused login: %s", bb)
	}

	copyPath := filepath.Join(m.credStore, sanitizeUser(cred.Username)+".json")
	if _, err := os.Stat(copyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the identical per-account copy is still switchable")
	}
	if _, err := os.Stat(copyPath + ".rejected"); err != nil {
		t.Fatal("the per-account copy was deleted instead of set aside")
	}
	if m.LoggedIn() {
		t.Fatal("LoggedIn still true with the only login refused")
	}

	snap := m.CredentialSnapshot()
	if snap["waitingForSpotifyTap"] != true || snap["savedLoginPresent"] != false ||
		snap["rejectedTotal"] != 1 || snap["rejectedBackupKept"] != true {
		t.Fatalf("snapshot does not show the handled refusal: %v", snap)
	}
	for k, v := range snap {
		if s, ok := v.(string); ok && (s == cred.Username || s == string(cred.Data)) {
			t.Fatalf("snapshot leaks the account in %q", k)
		}
	}
}

// A newer login of the same account in the per-account store is not the
// refused one and must survive.
func TestDifferentAccountCopySurvives(t *testing.T) {
	m := newTestManagerAt(t, "http://127.0.0.1:1")
	cred := seedDeadLogin(t, m)
	copyPath := filepath.Join(m.credStore, sanitizeUser(cred.Username)+".json")
	writeJSON(t, copyPath, storedCredential{Username: cred.Username, Data: []byte("newer-blob")})

	if moved, err := m.quarantineRejectedCredential(); err != nil || !moved {
		t.Fatalf("quarantine: moved=%v err=%v", moved, err)
	}
	if _, err := os.Stat(copyPath); err != nil {
		t.Fatal("a different login of the account was set aside")
	}
}

// The legacy layout: no login in state.json, the engine read credentials.json.
func TestLegacyCredentialsFileIsSetAside(t *testing.T) {
	m := newTestManagerAt(t, "http://127.0.0.1:1")
	legacy := filepath.Join(m.configDir, "credentials.json")
	writeJSON(t, legacy, storedCredential{Username: "user-id-here", Data: []byte("dead-blob")})

	moved, err := m.quarantineRejectedCredential()
	if err != nil || !moved {
		t.Fatalf("quarantine: moved=%v err=%v", moved, err)
	}
	if _, err := os.Stat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("legacy credentials.json still in the engine's way")
	}
	if _, err := os.Stat(legacy + ".rejected"); err != nil {
		t.Fatal("legacy credentials.json was not kept as a backup")
	}
}

// Fallback: the same refused login keeps coming back (or nothing could be set
// aside). The waits must grow to the cap, never stay at a minute.
func TestRepeatedRefusalsBackOffExponentially(t *testing.T) {
	m := newTestManagerAt(t, "http://127.0.0.1:1")
	cred := seedDeadLogin(t, m)

	var waits []time.Duration
	for range 8 {
		// Something (a credential sync, an account switch) puts the same
		// login back before the next start.
		if err := m.writeActiveCredential(cred); err != nil {
			t.Fatal(err)
		}
		m.resetCredRejectRun()
		m.noteLibrespotLine(fatalStoredCredLine)
		w, handled := m.afterEngineExit()
		if !handled {
			t.Fatal("refusal not handled")
		}
		waits = append(waits, w)
	}
	want := []time.Duration{3 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute,
		8 * time.Minute, 16 * time.Minute, 30 * time.Minute, 30 * time.Minute}
	for i := range want {
		if waits[i] != want[i] {
			t.Fatalf("waits = %v, want %v", waits, want)
		}
	}

	// A run that ends any other way resets the streak.
	m.resetCredRejectRun()
	if _, handled := m.afterEngineExit(); handled {
		t.Fatal("a normal exit was handled as a refusal")
	}
	if m.CredentialSnapshot()["rejectStreak"] != 0 {
		t.Fatal("streak not reset by a normal exit")
	}
}

func TestBackoffWithoutAnythingToSetAside(t *testing.T) {
	cases := []struct {
		streak      int
		quarantined bool
		want        time.Duration
	}{
		{1, true, credRejectQuickRestart},
		{1, false, time.Minute},
		{2, false, 2 * time.Minute},
		{6, false, 30 * time.Minute},
		{500, false, 30 * time.Minute},
		{500, true, 30 * time.Minute},
	}
	for _, c := range cases {
		if got := credRejectBackoff(c.streak, c.quarantined); got != c.want {
			t.Errorf("credRejectBackoff(%d, %v) = %s, want %s", c.streak, c.quarantined, got, c.want)
		}
	}
}

// A credential sync from another speaker must not stage the very login Spotify
// refused here; a different one still imports.
func TestImportRefusesTheRejectedLogin(t *testing.T) {
	m := newTestManagerAt(t, "http://127.0.0.1:1")
	cred := seedDeadLogin(t, m)
	if _, err := m.quarantineRejectedCredential(); err != nil {
		t.Fatal(err)
	}
	blob, _ := json.Marshal(cred)
	if err := m.ImportCredential(context.Background(), blob); !errors.Is(err, errCredentialRejected) {
		t.Fatalf("importing the refused login: err=%v, want errCredentialRejected", err)
	}
	if stateHasCredential(filepath.Join(m.configDir, "state.json")) {
		t.Fatal("the refused login was staged again")
	}
	fresh, _ := json.Marshal(storedCredential{Username: cred.Username, Data: []byte("fresh-blob")})
	if err := m.ImportCredential(context.Background(), fresh); err != nil {
		t.Fatalf("a fresh login was refused: %v", err)
	}
}
