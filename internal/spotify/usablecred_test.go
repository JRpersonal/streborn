package spotify

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

func usableCredManager(t *testing.T) *Manager {
	t.Helper()
	m := New("", filepath.Join(t.TempDir(), "cfg"), "", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.apiAddr = "127.0.0.1:0" // no engine listening: no live session
	return m
}

// A login Spotify already refused is on disk but cannot start a recall, so the
// save notice and the recall gate must both treat the speaker as not picked
// (field, ST10 stereo pair, 2026-10-04).
func TestARefusedLoginDoesNotCountForRecall(t *testing.T) {
	m := usableCredManager(t)
	dead := seedDeadLogin(t, m)
	if !m.CanRecall(context.Background()) {
		t.Fatal("an untested saved login must still allow a recall")
	}
	// The refusal backup as the supervisor writes it, with the quarantine of
	// state.json failed: the refused login is still the active one.
	writeJSON(t, filepath.Join(m.configDir, rejectedCredentialFile), dead)
	if !m.LoggedIn() {
		t.Fatal("precondition: the refused login is still on disk")
	}
	if m.CanRecall(context.Background()) {
		t.Fatal("a login Spotify refused was counted as one a recall can use")
	}
	if m.ensureSession(context.Background()) {
		t.Fatal("ensureSession tried to revive a login Spotify refused")
	}
}

// A new tap after the refusal writes a different login, and that one counts.
func TestANewLoginAfterARefusalCounts(t *testing.T) {
	m := usableCredManager(t)
	dead := seedDeadLogin(t, m)
	writeJSON(t, filepath.Join(m.configDir, rejectedCredentialFile), dead)
	fresh := storedCredential{Username: "user-id-here", Data: []byte("fresh-blob")}
	writeJSON(t, filepath.Join(m.configDir, "state.json"), map[string]any{"credentials": fresh})
	if !m.CanRecall(context.Background()) {
		t.Fatal("the login from the new tap was not counted")
	}
}

// Another account's login on the same speaker stays usable when one account's
// login was refused.
func TestAnotherAccountsLoginStaysUsable(t *testing.T) {
	m := usableCredManager(t)
	dead := seedDeadLogin(t, m)
	writeJSON(t, filepath.Join(m.configDir, rejectedCredentialFile), dead)
	writeJSON(t, filepath.Join(m.configDir, "state.json"), map[string]any{"device_id": "device-id-here"})
	writeJSON(t, filepath.Join(m.credStore, "other-user.json"), storedCredential{Username: "other-user", Data: []byte("other-blob")})
	if !m.CanRecall(context.Background()) {
		t.Fatal("a second account's login was thrown out with the refused one")
	}
}

func TestNoLoginAtAllCannotRecall(t *testing.T) {
	m := usableCredManager(t)
	if m.CanRecall(context.Background()) {
		t.Fatal("a speaker never picked in Spotify was allowed to recall")
	}
}
